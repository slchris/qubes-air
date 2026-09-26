package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Credential values are distinctive so a leak into the audit output is found
// by substring, not by guessing at a field name.
const (
	adminTokenValue    = "admin-token-SECRET-4f1c"
	auditorTokenValue  = "auditor-token-SECRET-9a2e"
	zoneTokenValue     = "zone-token-SECRET-77b0"
	auditedPeerAddress = "192.0.2.50:41234" // RFC 5737 documentation range
	auditedPeerIP      = "192.0.2.50"
	startRoute         = "/api/v1/qubes/:id/start"
)

// ownershipFixture answers qube ownership for RequireZones: q-a lives in
// zone-a, q-b in zone-b; anything else does not exist.
type ownershipFixture struct{}

func (ownershipFixture) ZoneOfQube(_ context.Context, qubeID string) (string, bool, error) {
	zone, ok := map[string]string{"q-a": "zone-a", "q-b": "zone-b"}[qubeID]
	return zone, ok, nil
}

func (ownershipFixture) ZoneOfJob(context.Context, string) (string, bool, error) {
	return "", false, nil
}

// auditedAPI mounts the production /api/v1 chain (apiMiddleware) over stub
// handlers, recording audit lines into the returned buffer. The stubs only
// answer; everything under test is the middleware order setupRouter uses.
func auditedAPI(t *testing.T, tune func(*config.Config)) (*gin.Engine, *bytes.Buffer, *middleware.SessionStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := config.DefaultConfig()
	cfg.Auth.APIToken = adminTokenValue
	cfg.Auth.Tokens = []config.ScopedToken{
		{Name: "auditor", Token: auditorTokenValue, Scope: string(middleware.ScopeReadOnly)},
		{Name: "zone-a-control", Token: zoneTokenValue, Scope: string(middleware.ScopeControl), Zones: []string{"zone-a"}},
	}
	if tune != nil {
		tune(cfg)
	}

	var buf bytes.Buffer
	sessions := middleware.NewSessionStore(0)
	r := gin.New()
	require.NoError(t, configureTrustedProxies(r))
	v1 := r.Group("/api/v1")
	v1.Use(apiMiddleware(cfg, sessions, ownershipFixture{}, audit.NewRecorder(&buf))...)

	v1.POST("/session", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })
	v1.GET("/qubes", func(c *gin.Context) { c.Status(http.StatusOK) })
	v1.POST("/qubes/:id/start", func(c *gin.Context) { c.Status(http.StatusAccepted) })
	v1.POST("/zones", func(c *gin.Context) { c.Status(http.StatusCreated) })
	return r, &buf, sessions
}

// apiRequest issues one request from a fixed peer; mutate adds credentials.
func apiRequest(r *gin.Engine, method, path, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = auditedPeerAddress
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(req)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func withHeader(name, value string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Set(name, value) }
}

func bearer(token string) func(*http.Request) {
	return withHeader("Authorization", "Bearer "+token)
}

// decodeAuditLines parses every JSON line the recorder wrote.
func decodeAuditLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &line), "audit output must be JSON lines: %q", raw)
		lines = append(lines, line)
	}
	return lines
}

// onlyAuditLine asserts the request left exactly one line and returns it.
func onlyAuditLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := decodeAuditLines(t, buf)
	require.Len(t, lines, 1, "want exactly one audit line, got: %s", buf.String())
	return lines[0]
}

// assertAuditFields checks the fields every audited request must carry, plus
// the request ID round trip, then the case-specific expectations.
func assertAuditFields(t *testing.T, line map[string]any, w *httptest.ResponseRecorder, want map[string]any) {
	t.Helper()
	requestID, _ := line["request_id"].(string)
	assert.NotEmpty(t, requestID, "audit line must carry a request ID")
	assert.Equal(t, w.Header().Get(middleware.RequestIDHeader), requestID,
		"the caller must get back the request ID the audit line carries")
	assert.Equal(t, auditedPeerIP, line["source"])
	assert.Equal(t, float64(w.Code), line["status"])
	for key, value := range want {
		assert.Equal(t, value, line[key], "audit field %q", key)
	}
}

// assertNoCredentialMaterial fails if any credential value, or the name of a
// header that carries one, appears anywhere in the audit output.
func assertNoCredentialMaterial(t *testing.T, buf *bytes.Buffer, secrets ...string) {
	t.Helper()
	out := buf.String()
	for _, s := range append(secrets, adminTokenValue, auditorTokenValue, zoneTokenValue) {
		if s != "" {
			assert.NotContains(t, out, s, "credential material reached the audit trail")
		}
	}
	for _, s := range []string{"Bearer", "Authorization", middleware.SessionCookieName} {
		assert.NotContains(t, out, s, "credential header reached the audit trail")
	}
}

func anonymousDenial(route, object string) map[string]any {
	return map[string]any{
		"outcome":       audit.OutcomeDenied,
		"authenticated": false,
		"subject":       audit.AnonymousSubject,
		"zone_scope":    "none",
		"method":        http.MethodPost,
		"route":         route,
		"object":        object,
	}
}

// TestAPIAuditRecordsMutationRefusedByAuthentication is the regression for
// Audit sitting behind ScopedAuth: a POST that never authenticated used to be
// refused with 401 and leave no audit line at all.
func TestAPIAuditRecordsMutationRefusedByAuthentication(t *testing.T) {
	longToken := strings.Repeat("L", 4097) // over the bearer length cap
	cases := []struct {
		name   string
		mutate func(*http.Request)
		secret string
	}{
		{name: "no credential"},
		{name: "unknown bearer", mutate: bearer("guessed-token-SECRET-0d3a"), secret: "guessed-token-SECRET-0d3a"},
		{name: "empty bearer", mutate: withHeader("Authorization", "Bearer ")},
		{name: "wrong scheme", mutate: withHeader("Authorization", "Basic "+adminTokenValue), secret: adminTokenValue},
		{name: "over-long bearer", mutate: bearer(longToken), secret: longToken},
		{
			name:   "unknown session cookie",
			mutate: withHeader("Cookie", middleware.SessionCookieName+"=stale-session-SECRET-5e6f"),
			secret: "stale-session-SECRET-5e6f",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, buf, _ := auditedAPI(t, nil)

			w := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "", tc.mutate)

			require.Equal(t, http.StatusUnauthorized, w.Code)
			assertAuditFields(t, onlyAuditLine(t, buf), w, anonymousDenial(startRoute, "q-a"))
			assertNoCredentialMaterial(t, buf, tc.secret)
		})
	}
}

// TestAPIAuditRecordsFailedLoginWithoutTheToken covers the one mutating route
// ScopedAuth lets through unauthenticated: the guessed token is in the body,
// and the body must never be recorded.
func TestAPIAuditRecordsFailedLoginWithoutTheToken(t *testing.T) {
	r, buf, _ := auditedAPI(t, nil)
	const guess = "login-guess-SECRET-a1b2"

	w := apiRequest(r, http.MethodPost, "/api/v1/session", `{"token":"`+guess+`"}`, nil)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	assertAuditFields(t, onlyAuditLine(t, buf), w, anonymousDenial("/api/v1/session", ""))
	assertNoCredentialMaterial(t, buf, guess)
}

// TestAPIAuditRecordsReadOnlyScopeDenial is the second half of the regression:
// a read-only token doing a POST is refused by RequireControl with 403, and
// the line must name the token that tried.
func TestAPIAuditRecordsReadOnlyScopeDenial(t *testing.T) {
	t.Run("bearer", func(t *testing.T) {
		r, buf, _ := auditedAPI(t, nil)

		w := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "", bearer(auditorTokenValue))

		require.Equal(t, http.StatusForbidden, w.Code)
		assertAuditFields(t, onlyAuditLine(t, buf), w, map[string]any{
			"outcome":       audit.OutcomeDenied,
			"authenticated": true,
			"subject":       "auditor",
			"zone_scope":    "fleet",
			"route":         startRoute,
			"object":        "q-a",
		})
		assertNoCredentialMaterial(t, buf)
	})

	t.Run("session cookie", func(t *testing.T) {
		r, buf, sessions := auditedAPI(t, nil)
		sess, err := sessions.Create("auditor", middleware.ScopeReadOnly, nil)
		require.NoError(t, err)

		w := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "",
			withHeader("Cookie", middleware.SessionCookieName+"="+sess.ID))

		require.Equal(t, http.StatusForbidden, w.Code)
		assertAuditFields(t, onlyAuditLine(t, buf), w, map[string]any{
			"outcome":       audit.OutcomeDenied,
			"authenticated": true,
			"subject":       "auditor",
		})
		assertNoCredentialMaterial(t, buf, sess.ID)
	})
}

// TestAPIAuditRecordsZoneDenial keeps the zone refusals audited with the
// subject and scope that caused them. A foreign or missing object is answered
// 404 so the caller learns nothing, and is still recorded as denied.
func TestAPIAuditRecordsZoneDenial(t *testing.T) {
	cases := []struct {
		name, path, route, object string
		status                    int
	}{
		{name: "foreign qube", path: "/api/v1/qubes/q-b/start", route: startRoute, object: "q-b", status: http.StatusNotFound},
		{name: "missing qube", path: "/api/v1/qubes/q-zz/start", route: startRoute, object: "q-zz", status: http.StatusNotFound},
		{name: "fleet operation", path: "/api/v1/zones", route: "/api/v1/zones", status: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, buf, _ := auditedAPI(t, nil)

			w := apiRequest(r, http.MethodPost, tc.path, "", bearer(zoneTokenValue))

			require.Equal(t, tc.status, w.Code)
			assertAuditFields(t, onlyAuditLine(t, buf), w, map[string]any{
				"outcome":       audit.OutcomeDenied,
				"authenticated": true,
				"subject":       "zone-a-control",
				"zone_scope":    "zone-a",
				"route":         tc.route,
				"object":        tc.object,
			})
			assertNoCredentialMaterial(t, buf)
		})
	}
}

// TestAPIAuditRecordsThrottledMutation records a mutation refused by the rate
// limiter. It stays client_error, not denied: throttling is not an
// authorization decision, and the status field already says 429.
func TestAPIAuditRecordsThrottledMutation(t *testing.T) {
	r, buf, _ := auditedAPI(t, func(cfg *config.Config) {
		cfg.Server.RateLimitPerSec = 0.001
		cfg.Server.RateLimitBurst = 1
	})

	first := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "", bearer(adminTokenValue))
	require.Equal(t, http.StatusAccepted, first.Code, "the first request spends the only token")
	second := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "", bearer(adminTokenValue))
	require.Equal(t, http.StatusTooManyRequests, second.Code)

	lines := decodeAuditLines(t, buf)
	require.Len(t, lines, 2, "each attempt leaves one line: %s", buf.String())
	assertAuditFields(t, lines[1], second, map[string]any{
		"outcome":       audit.OutcomeClientError,
		"authenticated": true,
		"subject":       "api_token",
	})
	assert.NotEqual(t, lines[0]["request_id"], lines[1]["request_id"])
	assertNoCredentialMaterial(t, buf)
}

// TestAPIAuditRecordsSuccessOnce guards against the reorder double-recording:
// a request that passes every check leaves exactly one success line, with the
// console's request ID rather than one the client sent.
func TestAPIAuditRecordsSuccessOnce(t *testing.T) {
	r, buf, _ := auditedAPI(t, nil)

	w := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "", func(req *http.Request) {
		bearer(zoneTokenValue)(req)
		req.Header.Set(middleware.RequestIDHeader, "client-chosen-id")
	})

	require.Equal(t, http.StatusAccepted, w.Code)
	assertAuditFields(t, onlyAuditLine(t, buf), w, map[string]any{
		"outcome":       audit.OutcomeSuccess,
		"authenticated": true,
		"subject":       "zone-a-control",
		"zone_scope":    "zone-a",
		"route":         startRoute,
		"object":        "q-a",
	})
	assert.NotEqual(t, "client-chosen-id", w.Header().Get(middleware.RequestIDHeader))
	assertNoCredentialMaterial(t, buf, "client-chosen-id")
}

// TestAPIAuditSkipsReadsEvenWhenDenied pins the documented boundary: reads,
// allowed or refused, are not audit events (the access log still has them).
func TestAPIAuditSkipsReadsEvenWhenDenied(t *testing.T) {
	r, buf, _ := auditedAPI(t, nil)

	denied := apiRequest(r, http.MethodGet, "/api/v1/qubes", "", nil)
	allowed := apiRequest(r, http.MethodGet, "/api/v1/qubes", "", bearer(auditorTokenValue))

	require.Equal(t, http.StatusUnauthorized, denied.Code)
	require.Equal(t, http.StatusOK, allowed.Code)
	assert.Empty(t, buf.String(), "reads must not be audited")
}
