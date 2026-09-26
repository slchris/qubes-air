package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionStoreLifecycle(t *testing.T) {
	s := NewSessionStore(time.Hour)
	sess, err := s.Create("api_token", ScopeControl, nil)
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
	sess, err := s.Create("t", ScopeReadOnly, nil)
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
	sess, err := store.Create("api_token", ScopeControl, nil)
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

// TestSessionStoreCarriesZoneScope — a session keeps the token's object-level
// restriction and copies it, so a later mutation of the caller's slice cannot
// widen a live session.
func TestSessionStoreCarriesZoneScope(t *testing.T) {
	s := NewSessionStore(time.Hour)
	zones := []string{"z1"}
	sess, err := s.Create("scoped", ScopeControl, zones)
	require.NoError(t, err)
	require.Equal(t, []string{"z1"}, sess.Zones)

	zones[0] = "z2"
	got, ok := s.Get(sess.ID)
	require.True(t, ok)
	assert.Equal(t, []string{"z1"}, got.Zones, "the session must not share the caller's slice")
}

// TestSessionStoreDefaultTTLIsThirtyMinutes pins the lifetime a console uses
// before an operator saves one: a store built with no TTL issues 30-minute
// sessions, not the 12 hours earlier releases handed out.
func TestSessionStoreDefaultTTLIsThirtyMinutes(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Minute} {
		s := NewSessionStore(ttl)
		sess, err := s.Create("t", ScopeControl, nil)
		require.NoError(t, err)
		assert.Equal(t, 30*time.Minute, sess.Expires.Sub(sess.Created), "ttl %v", ttl)
	}
	assert.Equal(t, 30*time.Minute, DefaultSessionTTL)
}

// TestSessionStoreSetTTLShortensButNeverExtends — lowering the setting pulls an
// issued session's deadline in; raising it again must not hand that session
// back the time it lost, while new sessions get the new lifetime.
func TestSessionStoreSetTTLShortensButNeverExtends(t *testing.T) {
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	s := NewSessionStore(30 * time.Minute)
	s.now = func() time.Time { return base }
	existing, err := s.Create("t", ScopeControl, nil)
	require.NoError(t, err)

	s.SetTTL(5 * time.Minute)
	shortened, ok := s.Get(existing.ID)
	require.True(t, ok)
	assert.Equal(t, base.Add(5*time.Minute), shortened.Expires)

	s.SetTTL(time.Hour)
	unchanged, ok := s.Get(existing.ID)
	require.True(t, ok)
	assert.Equal(t, shortened.Expires, unchanged.Expires, "raising the setting must not extend an issued session")

	fresh, err := s.Create("t", ScopeControl, nil)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, fresh.Expires.Sub(fresh.Created))
}

// TestSessionStoreSetTTLExpiresSessionsPastTheNewDeadline — a session older than
// the new lifetime stops authenticating at once rather than at its old deadline.
func TestSessionStoreSetTTLExpiresSessionsPastTheNewDeadline(t *testing.T) {
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	s := NewSessionStore(time.Hour)
	s.now = func() time.Time { return base }
	old, err := s.Create("t", ScopeControl, nil)
	require.NoError(t, err)

	s.now = func() time.Time { return base.Add(10 * time.Minute) }
	s.SetTTL(5 * time.Minute)
	_, ok := s.Get(old.ID)
	assert.False(t, ok, "a session older than the new lifetime must not authenticate")
}

// TestSessionStoreSetTTLIgnoresNonPositive — a zero or negative lifetime would
// expire every session on the spot or mint already-dead ones; it is ignored.
func TestSessionStoreSetTTLIgnoresNonPositive(t *testing.T) {
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	s := NewSessionStore(time.Hour)
	s.now = func() time.Time { return base }
	existing, err := s.Create("t", ScopeControl, nil)
	require.NoError(t, err)

	for _, ttl := range []time.Duration{0, -time.Minute} {
		s.SetTTL(ttl)
	}
	got, ok := s.Get(existing.ID)
	require.True(t, ok)
	assert.Equal(t, base.Add(time.Hour), got.Expires)
	fresh, err := s.Create("t", ScopeControl, nil)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, fresh.Expires.Sub(fresh.Created))
}

// TestSessionStoreSetTTLConcurrentWithCreate runs SetTTL against Create and Get.
// Create used to read the lifetime before taking the lock; under -race this
// test is what catches that coming back.
func TestSessionStoreSetTTLConcurrentWithCreate(t *testing.T) {
	s := NewSessionStore(30 * time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			ttl := 5 * time.Minute
			if i%2 == 0 {
				ttl = time.Hour
			}
			s.SetTTL(ttl)
		}(i)
		go func(i int) {
			defer wg.Done()
			sess, err := s.Create("t", ScopeControl, nil)
			if err != nil {
				t.Errorf("create concurrent session %d: %v", i, err)
				return
			}
			if _, ok := s.Get(sess.ID); !ok {
				t.Errorf("session %d did not authenticate right after it was created", i)
			}
		}(i)
	}
	wg.Wait()
}

// sessionMarkerRouter answers whether the request reached the handler marked as
// a browser session.
func sessionMarkerRouter(apiToken string, store *SessionStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ScopedAuth(apiToken, nil, store))
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"session": SessionAuthenticated(c)})
	})
	return r
}

// TestSessionAuthenticatedOnlyForTheCookieBranch — the marker says "a person's
// browser session", so it is set by the cookie branch of ScopedAuth and by
// nothing else: not a Bearer token, not a Bearer that rescued an unknown or
// expired cookie, and not a console with authentication disabled.
func TestSessionAuthenticatedOnlyForTheCookieBranch(t *testing.T) {
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(time.Hour)
	store.now = func() time.Time { return base }
	expired, err := store.Create("api_token", ScopeControl, nil)
	require.NoError(t, err)
	store.now = func() time.Time { return base.Add(90 * time.Minute) }
	live, err := store.Create("api_token", ScopeControl, nil)
	require.NoError(t, err)

	cases := []struct {
		name    string
		apiTok  string
		cookie  string
		bearer  string
		status  int
		session bool
	}{
		{name: "live cookie", apiTok: "secret", cookie: live.ID, status: http.StatusOK, session: true},
		{name: "bearer only", apiTok: "secret", bearer: "secret", status: http.StatusOK},
		{name: "unknown cookie rescued by bearer", apiTok: "secret", cookie: "nope", bearer: "secret", status: http.StatusOK},
		{name: "expired cookie rescued by bearer", apiTok: "secret", cookie: expired.ID, bearer: "secret", status: http.StatusOK},
		{name: "expired cookie alone", apiTok: "secret", cookie: expired.ID, status: http.StatusUnauthorized},
		{name: "auth disabled with a cookie", cookie: live.ID, status: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tc.cookie})
			}
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			w := httptest.NewRecorder()
			sessionMarkerRouter(tc.apiTok, store).ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code)
			if tc.status != http.StatusOK {
				return
			}
			if tc.session {
				assert.JSONEq(t, `{"session":true}`, w.Body.String())
			} else {
				assert.JSONEq(t, `{"session":false}`, w.Body.String())
			}
		})
	}
}

// TestSessionAuthenticatedIgnoresForeignValues — gin's context keys are plain
// strings, so any package can write under the marker's key. Only the value
// ScopedAuth stores (an unexported type) counts; everything else, including the
// obvious forgery c.Set("middleware.auth.session", true), reads as false.
func TestSessionAuthenticatedIgnoresForeignValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		return c
	}
	assert.False(t, SessionAuthenticated(newContext()), "no marker at all")

	for name, forged := range map[string]any{
		"bool true":        true,
		"string true":      "true",
		"bool false":       false,
		"pointer":          &sessionMarker{},
		"empty struct":     struct{}{},
		"nil":              nil,
		"lookalike struct": struct{ session bool }{session: true},
	} {
		c := newContext()
		c.Set("middleware.auth.session", forged)
		assert.False(t, SessionAuthenticated(c), "a foreign %s under the key must not read as a session", name)
	}

	marked := newContext()
	markSessionAuthenticated(marked)
	assert.True(t, SessionAuthenticated(marked), "the marker ScopedAuth sets must read as a session")
	marked.Set("middleware.auth.session", true)
	assert.False(t, SessionAuthenticated(marked), "overwriting the marker must fail closed")
}
