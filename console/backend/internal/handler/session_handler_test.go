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
