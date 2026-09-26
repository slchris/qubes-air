package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/handler"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionScopeAPI mounts the real session handler behind the production
// /api/v1 chain, next to a stub fleet-only route, so GET /session is exercised
// through the same authentication, rate limit and zone rules as in setupRouter.
func sessionScopeAPI(t *testing.T, tune func(*config.Config)) (*gin.Engine, *bytes.Buffer, *middleware.SessionStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.DefaultConfig()
	cfg.Auth.APIToken = adminTokenValue
	cfg.Auth.Tokens = []config.ScopedToken{
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
	handler.NewSessionHandler(cfg.Auth.APIToken, scopedTokens(cfg), sessions, false).RegisterRoutes(v1)
	v1.GET("/settings", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r, &buf, sessions
}

func withSessionCookie(id string) func(*http.Request) {
	return func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: id})
	}
}

func decodeSessionScope(t *testing.T, body []byte) (subject string, zones []string) {
	t.Helper()
	var got struct {
		Subject string   `json:"subject"`
		Zones   []string `json:"zones"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	return got.Subject, got.Zones
}

// TestSessionScopeThroughTheAPIChain — a zone-scoped session learns its zones
// from GET /session, the fleet-only routes the UI then hides are still refused
// by the server, and reading the session leaves no audit line and no
// credential material in the response.
func TestSessionScopeThroughTheAPIChain(t *testing.T) {
	r, buf, sessions := sessionScopeAPI(t, nil)
	sess, err := sessions.Create("zone-a-control", middleware.ScopeControl, []string{"zone-a"})
	require.NoError(t, err)

	for name, credential := range map[string]func(*http.Request){
		"cookie": withSessionCookie(sess.ID),
		"bearer": bearer(zoneTokenValue),
	} {
		t.Run(name, func(t *testing.T) {
			w := apiRequest(r, http.MethodGet, "/api/v1/session", "", credential)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			subject, zones := decodeSessionScope(t, w.Body.Bytes())
			assert.Equal(t, "zone-a-control", subject)
			assert.Equal(t, []string{"zone-a"}, zones)
			assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			for _, secret := range []string{sess.ID, zoneTokenValue, adminTokenValue} {
				assert.NotContains(t, w.Body.String(), secret)
			}

			fleet := apiRequest(r, http.MethodGet, "/api/v1/settings", "", credential)
			assert.Equal(t, http.StatusForbidden, fleet.Code, "hiding a view in the UI is cosmetic; the server must refuse it")
		})
	}
	assert.Empty(t, buf.String(), "reads are not audited, GET /session included")
}

// TestSessionScopeRefusesAnonymousCallers — without a credential the chain
// answers 401 before the handler runs.
func TestSessionScopeRefusesAnonymousCallers(t *testing.T) {
	r, _, _ := sessionScopeAPI(t, nil)
	for name, credential := range map[string]func(*http.Request){
		"none":           nil,
		"forged cookie":  withSessionCookie("forged"),
		"unknown bearer": bearer("not-a-configured-token"),
	} {
		t.Run(name, func(t *testing.T) {
			w := apiRequest(r, http.MethodGet, "/api/v1/session", "", credential)
			assert.Equal(t, http.StatusUnauthorized, w.Code)
		})
	}
}

// TestSessionScopeWithAuthenticationDisabled — with no credential configured
// the chain passes everything through and the answer carries no restriction.
func TestSessionScopeWithAuthenticationDisabled(t *testing.T) {
	r, _, _ := sessionScopeAPI(t, func(cfg *config.Config) {
		cfg.Auth.APIToken = ""
		cfg.Auth.Tokens = nil
	})
	w := apiRequest(r, http.MethodGet, "/api/v1/session", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"subject":"","scope":"","zones":[]}`, w.Body.String())
}
