package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	readToken     = "read-only-token-0123456789abcdef"
	controlToken  = "control-token-0123456789abcdef"
	adminToken    = "admin-api-token-0123456789abcdef"
	unknownBearer = "Bearer entirely-unknown-token"
)

// newScopedRouter builds the production middleware chain (ScopedAuth then
// RequireControl) over one GET and one POST route.
func newScopedRouter(t *testing.T, apiToken string, scoped []Token) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ScopedAuth(apiToken, scoped))
	r.Use(RequireControl())
	r.GET("/protected", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.POST("/protected", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return r
}

func doReq(r *gin.Engine, method, authHeader string) *httptest.ResponseRecorder {
	return doReqPath(r, method, "/protected", authHeader)
}

func doReqPath(r *gin.Engine, method, path, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func scopedCreds() []Token {
	return []Token{
		{Name: "read", Value: readToken, Scope: ScopeReadOnly},
		{Name: "control", Value: controlToken, Scope: ScopeControl},
	}
}

func TestScopedAuth_ReadOnlyTokenReadsButCannotWrite(t *testing.T) {
	r := newScopedRouter(t, "", scopedCreds())

	w := doReq(r, http.MethodGet, "Bearer "+readToken)
	assert.Equal(t, http.StatusOK, w.Code)

	w = doReq(r, http.MethodPost, "Bearer "+readToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestScopedAuth_ControlTokenReadsAndWrites(t *testing.T) {
	r := newScopedRouter(t, "", scopedCreds())

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := doReq(r, method, "Bearer "+controlToken)
		assert.Equal(t, http.StatusOK, w.Code, "method %s", method)
	}
}

func TestScopedAuth_AdminAPITokenReadsAndWrites(t *testing.T) {
	// The legacy single api_token continues to work and is a control token:
	// misuse is the administrator's own scope, not a reduced one.
	r := newScopedRouter(t, adminToken, nil)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := doReq(r, method, "Bearer "+adminToken)
		assert.Equal(t, http.StatusOK, w.Code, "method %s", method)
	}
}

func TestScopedAuth_UnknownTokenIs401(t *testing.T) {
	r := newScopedRouter(t, "", scopedCreds())

	w := doReq(r, http.MethodGet, unknownBearer)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "Bearer", w.Header().Get("WWW-Authenticate"))
}

func TestScopedAuth_DisabledWhenNoTokenConfigured(t *testing.T) {
	r := newScopedRouter(t, "", nil)

	// No header: pass through.
	w := doReq(r, http.MethodPost, "")
	assert.Equal(t, http.StatusOK, w.Code)

	// A stray header is also ignored when auth is disabled.
	w = doReq(r, http.MethodPost, unknownBearer)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestScopedAuth_MalformedOrOversizedBearerIs401(t *testing.T) {
	r := newScopedRouter(t, "", scopedCreds())

	tests := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"wrong token", "Bearer wrong-token"},
		{"no scheme", readToken},
		{"wrong scheme", "Basic " + readToken},
		{"empty bearer", "Bearer "},
		{"prefix only", "Bearer"},
		{"token is a prefix of expected", "Bearer " + readToken[:10]},
		{"oversized token", "Bearer " + strings.Repeat("a", maxBearerTokenLen+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doReq(r, http.MethodGet, tt.header)
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, "Bearer", w.Header().Get("WWW-Authenticate"))
		})
	}
}

func TestScopedAuth_CaseInsensitiveScheme(t *testing.T) {
	r := newScopedRouter(t, "", scopedCreds())

	w := doReq(r, http.MethodGet, "bearer "+controlToken)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestScopeFromContext — the resolved scope is carried in the context so
// downstream code (RequireControl and future handlers) can read it.
func TestScopeFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ScopedAuth("", scopedCreds()))
	r.GET("/p", func(c *gin.Context) {
		scope, authed := ScopeFromContext(c)
		c.JSON(http.StatusOK, gin.H{"scope": scope, "authed": authed})
	})

	w := doReqPath(r, http.MethodGet, "/p", "Bearer "+readToken)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"scope":"read-only"`)
	assert.Contains(t, w.Body.String(), `"authed":true`)
}

// TestScopeFromContext_Disabled — when auth is disabled nothing is stored.
func TestScopeFromContext_Disabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ScopedAuth("", nil))
	r.GET("/p", func(c *gin.Context) {
		scope, authed := ScopeFromContext(c)
		c.JSON(http.StatusOK, gin.H{"scope": scope, "authed": authed})
	})

	w := doReqPath(r, http.MethodGet, "/p", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"authed":false`)
}

// TestMatchScope — matching scans every credential (no early return) across
// differing token lengths and picks the right scope.
func TestMatchScope(t *testing.T) {
	creds := newCredentials("", scopedCreds())

	_, ok := matchScope("short-admin", creds)
	assert.False(t, ok)

	got, ok := matchScope(readToken, creds)
	assert.True(t, ok)
	assert.Equal(t, ScopeReadOnly, got)

	got, ok = matchScope(controlToken, creds)
	assert.True(t, ok)
	assert.Equal(t, ScopeControl, got)
}

func TestValidScope(t *testing.T) {
	for _, s := range []string{"read-only", "control"} {
		assert.True(t, ValidScope(s), s)
	}
	for _, s := range []string{"", "admin", "READ-ONLY", "read_only"} {
		assert.False(t, ValidScope(s), s)
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
		wantOk bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"Bearer   abc  ", "abc", true},
		{"Bearer ", "", false},
		{"Bearer", "", false},
		{"", "", false},
		{"Basic abc", "", false},
		{"Bearer " + strings.Repeat("x", maxBearerTokenLen), strings.Repeat("x", maxBearerTokenLen), true},
		{"Bearer " + strings.Repeat("x", maxBearerTokenLen+1), "", false},
	}
	for _, tt := range tests {
		got, ok := bearerToken(tt.header)
		assert.Equal(t, tt.wantOk, ok, "header=%q", tt.header)
		assert.Equal(t, tt.want, got, "header=%q", tt.header)
	}
}
