package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sessionRouter(h *SessionHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	h.RegisterRoutes(g)
	return r
}

func TestSessionLoginExchangesTokenForCookie(t *testing.T) {
	store := middleware.NewSessionStore(time.Hour)
	r := sessionRouter(NewSessionHandler("secret", nil, store, false))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(`{"token":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == middleware.SessionCookieName {
			cookie = c
		}
	}
	require.NotNil(t, cookie, "login must set the session cookie")
	assert.True(t, cookie.HttpOnly, "the session cookie must not be readable by scripts")
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	_, ok := store.Get(cookie.Value)
	assert.True(t, ok, "the cookie must correspond to a live session")
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"), "the login answer sets a credential cookie; it must not be cached")
}

func TestSessionLoginRejectsBadToken(t *testing.T) {
	store := middleware.NewSessionStore(time.Hour)
	r := sessionRouter(NewSessionHandler("secret", nil, store, false))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(`{"token":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	for _, c := range w.Result().Cookies() {
		assert.NotEqual(t, middleware.SessionCookieName, c.Name, "no session on a bad token")
	}
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"), "a refused login is not cacheable either")
}

// TestSessionLoginIsNotCacheableEvenForAMalformedBody — the header is set before
// the body is read, so no answer of the login route can be cached.
func TestSessionLoginIsNotCacheableEvenForAMalformedBody(t *testing.T) {
	r := sessionRouter(NewSessionHandler("secret", nil, middleware.NewSessionStore(time.Hour), false))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(`{`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestSessionLogoutClearsSession(t *testing.T) {
	store := middleware.NewSessionStore(time.Hour)
	sess, err := store.Create("api_token", middleware.ScopeControl, nil)
	require.NoError(t, err)
	r := sessionRouter(NewSessionHandler("secret", nil, store, false))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/session", nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: sess.ID})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	_, ok := store.Get(sess.ID)
	assert.False(t, ok, "logout must invalidate the session")
}

const (
	sessionTestAdminToken = "admin-token-SECRET-c41d"
	sessionTestZoneToken  = "zone-token-SECRET-0be7"
)

// authedSessionRouter mounts the session routes behind ScopedAuth, the way
// setupRouter does, with one fleet-wide admin token and one zone-scoped token.
// Passing authDisabled configures no credential at all.
func authedSessionRouter(store *middleware.SessionStore, authDisabled bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	apiToken := sessionTestAdminToken
	scoped := []middleware.Token{{
		Name: "zone-operator", Value: sessionTestZoneToken, Scope: middleware.ScopeReadOnly, Zones: []string{"z1", "z2"},
	}}
	if authDisabled {
		apiToken, scoped = "", nil
	}
	r := gin.New()
	rg := r.Group("/api/v1")
	rg.Use(middleware.ScopedAuth(apiToken, scoped, store))
	NewSessionHandler(apiToken, scoped, store, false).RegisterRoutes(rg)
	return r
}

func getSession(r *gin.Engine, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	if mutate != nil {
		mutate(req)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestSessionCurrentReportsTheCookieSessionScope — a browser that reloads holds
// only an HttpOnly cookie; GET /session tells it which zones that session may
// address, and nothing that would let a script reuse the session.
func TestSessionCurrentReportsTheCookieSessionScope(t *testing.T) {
	store := middleware.NewSessionStore(time.Hour)
	sess, err := store.Create("zone-operator", middleware.ScopeReadOnly, []string{"z1", "z2"})
	require.NoError(t, err)

	w := getSession(authedSessionRouter(store, false), func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: sess.ID})
	})

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"subject":"zone-operator","scope":"read-only","zones":["z1","z2"]}`, w.Body.String())
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"), "the answer names the credential; it must not be cached")
	assert.NotContains(t, w.Body.String(), sess.ID, "the session ID must never be echoed")
	assert.Empty(t, w.Result().Cookies(), "reading the session must not reissue or clear the cookie")
}

// TestSessionCurrentReportsTheBearerScope — CLI/MCP callers can ask too, and a
// fleet-wide token answers with an empty (never null) zone list.
func TestSessionCurrentReportsTheBearerScope(t *testing.T) {
	r := authedSessionRouter(middleware.NewSessionStore(time.Hour), false)

	fleet := getSession(r, func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+sessionTestAdminToken) })
	require.Equal(t, http.StatusOK, fleet.Code)
	assert.JSONEq(t, `{"subject":"api_token","scope":"control","zones":[]}`, fleet.Body.String())

	zoned := getSession(r, func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+sessionTestZoneToken) })
	require.Equal(t, http.StatusOK, zoned.Code)
	assert.JSONEq(t, `{"subject":"zone-operator","scope":"read-only","zones":["z1","z2"]}`, zoned.Body.String())

	for _, body := range []string{fleet.Body.String(), zoned.Body.String()} {
		assert.NotContains(t, body, sessionTestAdminToken, "the token value must never be echoed")
		assert.NotContains(t, body, sessionTestZoneToken, "the token value must never be echoed")
	}
}

// TestSessionCurrentRefusesAnUnauthenticatedCaller — GET is not the exempt login
// route: without a live cookie or a valid Bearer the answer is 401 and no scope.
func TestSessionCurrentRefusesAnUnauthenticatedCaller(t *testing.T) {
	store := middleware.NewSessionStore(time.Hour)
	r := authedSessionRouter(store, false)
	cases := map[string]func(*http.Request){
		"no credential": nil,
		"unknown cookie": func(req *http.Request) {
			req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "forged"})
		},
		"unknown bearer": func(req *http.Request) { req.Header.Set("Authorization", "Bearer not-a-token") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := getSession(r, mutate)
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.NotContains(t, w.Body.String(), "zones")
		})
	}
}

// TestSessionCurrentWithAuthenticationDisabled — a console with no credential
// configured has no identity to report and no zone restriction.
func TestSessionCurrentWithAuthenticationDisabled(t *testing.T) {
	w := getSession(authedSessionRouter(middleware.NewSessionStore(time.Hour), true), nil)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"subject":"","scope":"","zones":[]}`, w.Body.String())
}
