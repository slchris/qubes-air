package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionStoreLifecycle(t *testing.T) {
	s := NewSessionStore(time.Hour)
	sess, err := s.Create("api_token", ScopeControl)
	require.NoError(t, err)
	require.NotEmpty(t, sess.ID)

	got, ok := s.Get(sess.ID)
	require.True(t, ok)
	assert.Equal(t, ScopeControl, got.Scope)
	assert.Equal(t, "api_token", got.Subject)

	s.Delete(sess.ID)
	_, ok = s.Get(sess.ID)
	assert.False(t, ok, "a deleted session must not authenticate")
}

func TestSessionStoreExpires(t *testing.T) {
	s := NewSessionStore(time.Minute)
	base := time.Now()
	s.now = func() time.Time { return base }
	sess, err := s.Create("t", ScopeReadOnly)
	require.NoError(t, err)

	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	_, ok := s.Get(sess.ID)
	assert.False(t, ok, "an expired session must not authenticate")
}

// TestScopedAuthAcceptsSessionCookie — a browser holding a valid session cookie
// authenticates without a Bearer header, and the scope/subject reach the context.
func TestScopedAuthAcceptsSessionCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := NewSessionStore(time.Hour)
	sess, err := store.Create("api_token", ScopeControl)
	require.NoError(t, err)

	r := gin.New()
	r.Use(ScopedAuth("secret", nil, store))
	r.GET("/", func(c *gin.Context) {
		scope, authed := ScopeFromContext(c)
		subject, _ := SubjectFromContext(c)
		c.JSON(http.StatusOK, gin.H{"scope": scope, "authed": authed, "subject": subject})
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sess.ID})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"scope":"control"`)
	assert.Contains(t, w.Body.String(), `"subject":"api_token"`)

	// A bogus cookie is rejected like a missing token.
	bad := httptest.NewRequest(http.MethodGet, "/", nil)
	bad.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "nope"})
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, bad)
	assert.Equal(t, http.StatusUnauthorized, w2.Code)
}
