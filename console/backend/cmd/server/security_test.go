package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetAllowedOrigin(t *testing.T) {
	allowed := []string{"https://console.local"}
	assert.Equal(t, "https://console.local", getAllowedOrigin("https://console.local", allowed))
	assert.Equal(t, "", getAllowedOrigin("https://evil.example", allowed),
		"a disallowed origin must get no allow-origin, not the first configured one")
	assert.Equal(t, "*", getAllowedOrigin("https://any", []string{"*"}))
	assert.Equal(t, "", getAllowedOrigin("https://any", nil))
}

func TestCORSDisallowedOriginSetsVaryOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.DefaultConfig()
	cfg.CORS.AllowedOrigins = []string{"https://console.local"}

	r := gin.New()
	r.Use(corsMiddleware(cfg))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, "Origin", w.Header().Get("Vary"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

func TestSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(securityHeaders())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
	assert.Contains(t, w.Header().Get("Content-Security-Policy"), "default-src 'self'")
	assert.Empty(t, w.Header().Get("Strict-Transport-Security"),
		"HSTS must not be set on plain HTTP")
}

// TestClientIPIgnoresForwardedFor pins the property that a caller cannot choose
// its own rate-limit bucket or audit source by sending X-Forwarded-For.
//
// gin trusts every proxy by default, which is what configureTrustedProxies
// exists to undo; without it the limiter would key on a value the client picks,
// so an unauthenticated caller could rotate the header and never hit the limit.
func TestClientIPIgnoresForwardedFor(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	require.NoError(t, configureTrustedProxies(r))

	var reported string
	r.GET("/whoami", func(c *gin.Context) {
		reported = c.ClientIP()
		c.Status(http.StatusOK)
	})
	// One token, negligible refill: a second request from the same bucket is
	// throttled, so the status code shows which key the limiter used.
	r.GET("/throttled",
		middleware.RateLimit(middleware.NewRateLimiter(0.001, 1)),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	whoami := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	whoami.RemoteAddr = "10.31.0.50:41234"
	whoami.Header.Set("X-Forwarded-For", "203.0.113.7")
	r.ServeHTTP(httptest.NewRecorder(), whoami)
	assert.Equal(t, "10.31.0.50", reported,
		"ClientIP must be the peer address, not the address the caller claimed")

	first := httptest.NewRequest(http.MethodGet, "/throttled", nil)
	first.RemoteAddr = "10.31.0.50:41234"
	first.Header.Set("X-Forwarded-For", "203.0.113.7")
	firstRecorder := httptest.NewRecorder()
	r.ServeHTTP(firstRecorder, first)
	require.Equal(t, http.StatusOK, firstRecorder.Code, "the first request spends the token")

	second := httptest.NewRequest(http.MethodGet, "/throttled", nil)
	second.RemoteAddr = "10.31.0.50:41234"
	second.Header.Set("X-Forwarded-For", "198.51.100.9")
	secondRecorder := httptest.NewRecorder()
	r.ServeHTTP(secondRecorder, second)
	assert.Equal(t, http.StatusTooManyRequests, secondRecorder.Code,
		"a fresh X-Forwarded-For must not buy a fresh bucket")
}
