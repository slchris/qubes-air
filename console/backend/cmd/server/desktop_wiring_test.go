package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDesktopAccessIsWiredWithADesktopStream — the console built by
// initDependencies serves the desktop routes, and its qube service carries the
// per-qube desktop streamer: a grant request for an unknown qube gets as far
// as the qube lookup (404) instead of stopping at "no desktop transport"
// (503), which is where the archived line always stopped.
func TestDesktopAccessIsWiredWithADesktopStream(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Mode = gin.TestMode
	cfg.Database.DSN = filepath.Join(t.TempDir(), "console.db")
	cfg.Auth.APIToken = adminTokenValue
	cfg.Auth.Tokens = []config.ScopedToken{
		{Name: "zone-a-control", Token: zoneTokenValue, Scope: string(middleware.ScopeControl), Zones: []string{"zone-a"}},
	}
	deps, err := initDependencies(cfg)
	require.NoError(t, err)
	t.Cleanup(deps.Close)
	router := setupRouter(cfg, deps)

	call := func(method, path, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:50000"
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		mutate(req)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	grant := call(http.MethodPost, "/api/v1/qubes/no-such-qube/desktop-access", `{"operation":"frame"}`, bearer(adminTokenValue))
	assert.Equal(t, http.StatusNotFound, grant.Code, grant.Body.String())

	// The approval queue is for a person at the Console: a Bearer token is
	// refused, a fleet-wide session is served, a zone-scoped one is refused.
	assert.Equal(t, http.StatusForbidden, call(http.MethodGet, "/api/v1/desktop-access", "", bearer(adminTokenValue)).Code)
	admin, err := deps.sessions.Create("api_token", middleware.ScopeControl, nil)
	require.NoError(t, err)
	queue := call(http.MethodGet, "/api/v1/desktop-access", "", withSessionCookie(admin.ID))
	require.Equal(t, http.StatusOK, queue.Code, queue.Body.String())
	assert.JSONEq(t, `{"requests":[]}`, queue.Body.String())
	assert.Equal(t, http.StatusForbidden, call(http.MethodGet, "/api/v1/desktop-access", "", bearer(zoneTokenValue)).Code)
}
