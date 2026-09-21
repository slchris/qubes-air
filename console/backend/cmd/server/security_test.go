package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/stretchr/testify/assert"
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
