package middleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	zoneTokenValue  = "zone-scoped-token"
	fleetTokenValue = "fleet-token"
	credentialsPath = "/api/v1/credentials"
)

// fakeResolver answers ownership from two maps: qube→zone and job→qube.
type fakeResolver struct {
	qubeZones map[string]string
	jobQubes  map[string]string
	err       error
}

func (f fakeResolver) ZoneOfQube(_ context.Context, qubeID string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	zone, ok := f.qubeZones[qubeID]
	return zone, ok, nil
}

func (f fakeResolver) ZoneOfJob(ctx context.Context, jobID string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	qube, ok := f.jobQubes[jobID]
	if !ok {
		return "", false, nil
	}
	return f.ZoneOfQube(ctx, qube)
}

func testResolver() fakeResolver {
	return fakeResolver{
		qubeZones: map[string]string{"q1": "z1", "q2": "z2"},
		jobQubes:  map[string]string{"j1": "q1", "j2": "q2"},
	}
}

// zoneRouter builds the middleware chain under test plus representative routes
// for every class the middleware classifies.
func zoneRouter(resolver ObjectResolver, sessions *SessionStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(ScopedAuth("", []Token{
		{Name: "zones", Value: zoneTokenValue, Scope: ScopeControl, Zones: []string{"z1"}},
		{Name: "fleet", Value: fleetTokenValue, Scope: ScopeControl},
	}, sessions))
	v1.Use(RequireZones(resolver))

	echo := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	v1.POST("/session", echo)
	v1.GET("/session", echo)
	v1.GET("/zones", echo)
	v1.POST("/zones", echo)
	v1.GET("/zones/:id", echo)
	v1.PUT("/zones/:id", echo)
	v1.GET("/zones/:id/capacity", echo)
	v1.GET("/qubes", echo)
	v1.POST("/qubes", func(c *gin.Context) {
		var body struct {
			ZoneID string `json:"zone_id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad body"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"zone_id": body.ZoneID})
	})
	v1.GET("/qubes/:id", echo)
	v1.POST("/qubes/:id/start", echo)
	v1.POST("/qubes/:id/purge", echo)
	v1.GET("/jobs", echo)
	v1.GET("/jobs/:id", echo)
	v1.GET("/jobs/:id/log", echo)
	v1.GET("/credentials", echo)
	v1.POST("/credentials", echo)
	v1.GET("/status", echo)
	// Deliberately unclassified: a zone-restricted token must not reach a route
	// this middleware does not know.
	v1.GET("/mystery", echo)
	return r
}

func doZoneReq(r *gin.Engine, token, method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestZoneAuthFleetTokenReachesFleetRoutes(t *testing.T) {
	r := zoneRouter(testResolver(), nil)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, credentialsPath, ""},
		{http.MethodPost, credentialsPath, "{}"},
		{http.MethodGet, "/api/v1/status", ""},
		{http.MethodGet, "/api/v1/jobs", ""},
		{http.MethodPost, "/api/v1/zones", "{}"},
		{http.MethodGet, "/api/v1/mystery", ""},
	} {
		w := doZoneReq(r, fleetTokenValue, tc.method, tc.path, tc.body)
		assert.Equal(t, http.StatusOK, w.Code, "%s %s", tc.method, tc.path)
	}
}

func TestZoneAuthScopedTokenObjectAccess(t *testing.T) {
	r := zoneRouter(testResolver(), nil)

	allowed := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/zones"},
		{http.MethodGet, "/api/v1/zones/z1"},
		{http.MethodPut, "/api/v1/zones/z1"},
		{http.MethodGet, "/api/v1/zones/z1/capacity"},
		{http.MethodGet, "/api/v1/qubes"},
		{http.MethodGet, "/api/v1/qubes/q1"},
		{http.MethodPost, "/api/v1/qubes/q1/start"},
		{http.MethodPost, "/api/v1/qubes/q1/purge"},
		{http.MethodGet, "/api/v1/jobs/j1"},
		{http.MethodGet, "/api/v1/jobs/j1/log"},
	}
	for _, tc := range allowed {
		w := doZoneReq(r, zoneTokenValue, tc.method, tc.path, "")
		assert.Equal(t, http.StatusOK, w.Code, "%s %s must be allowed", tc.method, tc.path)
	}

	// Foreign and missing objects are indistinguishable: 404 both ways.
	denied := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/zones/z2"},
		{http.MethodPut, "/api/v1/zones/z2"},
		{http.MethodGet, "/api/v1/qubes/q2"},
		{http.MethodPost, "/api/v1/qubes/q2/start"},
		{http.MethodGet, "/api/v1/qubes/missing"},
		{http.MethodGet, "/api/v1/jobs/j2"},
		{http.MethodGet, "/api/v1/jobs/missing"},
	}
	for _, tc := range denied {
		w := doZoneReq(r, zoneTokenValue, tc.method, tc.path, "")
		assert.Equal(t, http.StatusNotFound, w.Code, "%s %s must look missing", tc.method, tc.path)
	}
}

func TestZoneAuthScopedTokenFleetRoutesRefused(t *testing.T) {
	r := zoneRouter(testResolver(), nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, credentialsPath},
		{http.MethodPost, credentialsPath},
		{http.MethodGet, "/api/v1/status"},
		{http.MethodGet, "/api/v1/jobs"},
		{http.MethodPost, "/api/v1/zones"},
		{http.MethodGet, "/api/v1/mystery"},
	} {
		w := doZoneReq(r, zoneTokenValue, tc.method, tc.path, "")
		assert.Equal(t, http.StatusForbidden, w.Code, "%s %s must be refused", tc.method, tc.path)
	}
}

func TestZoneAuthCreateQubeChecksBodyZone(t *testing.T) {
	r := zoneRouter(testResolver(), nil)

	w := doZoneReq(r, zoneTokenValue, http.MethodPost, "/api/v1/qubes", `{"name":"n","zone_id":"z1"}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"zone_id":"z1"`, "the handler must still see the body")

	w = doZoneReq(r, zoneTokenValue, http.MethodPost, "/api/v1/qubes", `{"name":"n","zone_id":"z2"}`)
	assert.Equal(t, http.StatusForbidden, w.Code)

	w = doZoneReq(r, zoneTokenValue, http.MethodPost, "/api/v1/qubes", `{"name":"n"}`)
	assert.Equal(t, http.StatusForbidden, w.Code, "a zone-less qube is not addressable by a scoped token")

	w = doZoneReq(r, zoneTokenValue, http.MethodPost, "/api/v1/qubes", `not json`)
	assert.Equal(t, http.StatusForbidden, w.Code, "an unparseable body must fail closed")
}

func TestZoneAuthResolverFailureFailsClosed(t *testing.T) {
	r := zoneRouter(fakeResolver{err: errors.New("database down")}, nil)
	for _, path := range []string{"/api/v1/qubes/q1", "/api/v1/jobs/j1"} {
		w := doZoneReq(r, zoneTokenValue, http.MethodGet, path, "")
		assert.Equal(t, http.StatusInternalServerError, w.Code, path)
	}
}

func TestZoneAuthScopedSessionCookieCarriesZones(t *testing.T) {
	store := NewSessionStore(time.Hour)
	sess, err := store.Create("zones", ScopeControl, []string{"z1"})
	require.NoError(t, err)

	r := zoneRouter(testResolver(), store)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/qubes/q2", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sess.ID})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code, "a session must not exceed the zones of its token")

	req = httptest.NewRequest(http.MethodGet, "/api/v1/qubes/q1", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sess.ID})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestZoneAuthDisabledAuthPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(ScopedAuth("", nil, nil)) // no credentials: authentication disabled
	v1.Use(RequireZones(testResolver()))
	v1.GET("/credentials", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	v1.GET("/qubes/:id", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	for _, path := range []string{credentialsPath, "/api/v1/qubes/q2"} {
		w := doZoneReq(r, "", http.MethodGet, path, "")
		assert.Equal(t, http.StatusOK, w.Code, path)
	}
}

func TestZoneAuthNilResolverRefusesRestrictedToken(t *testing.T) {
	r := zoneRouter(nil, nil)
	w := doZoneReq(r, zoneTokenValue, http.MethodGet, "/api/v1/qubes/q1", "")
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	// A fleet token does not need the resolver.
	w = doZoneReq(r, fleetTokenValue, http.MethodGet, "/api/v1/qubes/q1", "")
	assert.Equal(t, http.StatusOK, w.Code)
}
