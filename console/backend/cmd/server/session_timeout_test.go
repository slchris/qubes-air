package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedBuffer is a log sink safe to read while background goroutines started
// by initDependencies may still be writing to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureStartupLog(t *testing.T) *lockedBuffer {
	t.Helper()
	out := &lockedBuffer{}
	prev := log.Writer()
	log.SetOutput(out)
	t.Cleanup(func() { log.SetOutput(prev) })
	return out
}

// seedSecuritySettings writes the security settings row into a fresh database
// at dsn verbatim, the way a release that accepted any integer left it, and
// closes the database again.
func seedSecuritySettings(t *testing.T, dsn, security string) {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = dsn
	db, err := database.New(cfg)
	require.NoError(t, err)
	_, err = db.DB().Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('security', ?, datetime('now'))`, security)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func openSeededDB(t *testing.T, security string) *database.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "console.db")
	if security != "" {
		seedSecuritySettings(t, dsn, security)
	}
	cfg := database.DefaultConfig()
	cfg.DSN = dsn
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sessionLifetime(t *testing.T, store *middleware.SessionStore) time.Duration {
	t.Helper()
	sess, err := store.Create("operator", middleware.ScopeControl, nil)
	require.NoError(t, err)
	return sess.Expires.Sub(sess.Created)
}

// TestSessionDefaultsAgree — the service's fallback minutes and the session
// store's default must be the same lifetime, or GET /settings would report a
// value sessions do not use.
func TestSessionDefaultsAgree(t *testing.T) {
	assert.Equal(t, middleware.DefaultSessionTTL, time.Duration(service.DefaultSessionTimeoutMinutes)*time.Minute)
}

// TestConfiguredSessionStoreUsesTheSavedTimeout — a valid stored timeout is the
// lifetime of every session issued after startup, with no warning.
func TestConfiguredSessionStoreUsesTheSavedTimeout(t *testing.T) {
	out := captureStartupLog(t)
	for security, want := range map[string]time.Duration{
		"":                        middleware.DefaultSessionTTL,
		`{"sessionTimeout":5}`:    5 * time.Minute,
		`{"sessionTimeout":45}`:   45 * time.Minute,
		`{"sessionTimeout":1440}`: 24 * time.Hour,
	} {
		_, store := newSettingsAndSessions(context.Background(), openSeededDB(t, security))
		assert.Equal(t, want, sessionLifetime(t, store), "stored %q", security)
	}
	assert.NotContains(t, out.String(), "WARNING")
}

// TestConfiguredSessionStoreFallsBackOnABadStoredTimeout is the upgrade case:
// releases before the 5-1440 range was enforced saved any integer. Such a value
// must not stop the console from starting; sessions use the default and the
// operator is told why.
func TestConfiguredSessionStoreFallsBackOnABadStoredTimeout(t *testing.T) {
	for _, security := range []string{
		`{"sessionTimeout":0}`,
		`{"sessionTimeout":1}`,
		`{"sessionTimeout":-10}`,
		`{"sessionTimeout":1441}`,
		`{"sessionTimeout":100000}`,
		`{"sessionTimeout":"thirty"}`,
	} {
		t.Run(security, func(t *testing.T) {
			out := captureStartupLog(t)
			_, store := newSettingsAndSessions(context.Background(), openSeededDB(t, security))
			require.NotNil(t, store)
			assert.Equal(t, middleware.DefaultSessionTTL, sessionLifetime(t, store))
			logged := out.String()
			assert.Contains(t, logged, "WARNING: settings:")
			assert.Contains(t, logged, "browser sessions last the default 30m0s")
		})
	}
}

// startConsole runs the real startup path over a database whose security
// settings row was seeded by an older release, and returns the router.
func startConsole(t *testing.T, security string) *gin.Engine {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Server.Mode = gin.TestMode
	cfg.Auth.APIToken = adminTokenValue
	cfg.Database.DSN = filepath.Join(t.TempDir(), "console.db")
	seedSecuritySettings(t, cfg.Database.DSN, security)

	deps, err := initDependencies(cfg)
	require.NoError(t, err, "a stored session timeout must never stop the console from starting")
	t.Cleanup(deps.Close)
	return setupRouter(cfg, deps)
}

// loginLifetime exchanges the admin token for a session and returns how long
// the server said it lasts.
func loginLifetime(t *testing.T, router *gin.Engine) time.Duration {
	t.Helper()
	before := time.Now()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(`{"token":"`+adminTokenValue+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var session struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &session))
	return session.ExpiresAt.Sub(before).Round(time.Minute)
}

func adminRequest(t *testing.T, router *gin.Engine, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/settings", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminTokenValue)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// TestStartupSurvivesAnOutOfRangeStoredTimeout is the upgrade test: a database
// an older release left with sessionTimeout 2 still boots, a login gets the
// default lifetime, and the settings page shows that default.
func TestStartupSurvivesAnOutOfRangeStoredTimeout(t *testing.T) {
	out := captureStartupLog(t)
	router := startConsole(t, `{"sessionTimeout":2,"twoFactorEnabled":false}`)

	assert.Contains(t, out.String(), "stored session timeout: invalid session timeout: 2 minutes is outside 5-1440")
	assert.Equal(t, middleware.DefaultSessionTTL, loginLifetime(t, router))
	w := adminRequest(t, router, http.MethodGet, "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"sessionTimeout":30`)
}

// TestStartupAppliesTheStoredTimeoutAndLaterSaves — the store initDependencies
// builds is the one login and the settings handler share: a valid stored value
// governs logins after a restart, and a save takes effect without one.
func TestStartupAppliesTheStoredTimeoutAndLaterSaves(t *testing.T) {
	router := startConsole(t, `{"sessionTimeout":45,"twoFactorEnabled":false}`)
	assert.Equal(t, 45*time.Minute, loginLifetime(t, router))

	w := adminRequest(t, router, http.MethodPut,
		`{"general":{"timezone":"UTC","language":"en","theme":"system"},"notifications":{},"security":{"sessionTimeout":10}}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, 10*time.Minute, loginLifetime(t, router))
}
