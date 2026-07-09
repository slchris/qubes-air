package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func newTestRouter(token string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Auth(token))
	r.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func doGet(r *gin.Engine, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAuth_DisabledWhenNoToken(t *testing.T) {
	r := newTestRouter("")

	// No header: should pass through when auth is disabled.
	w := doGet(r, "")
	assert.Equal(t, http.StatusOK, w.Code)

	// A stray header should also be ignored when disabled.
	w = doGet(r, "Bearer anything")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuth_ValidToken(t *testing.T) {
	r := newTestRouter("s3cr3t-token")

	w := doGet(r, "Bearer s3cr3t-token")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuth_CaseInsensitiveScheme(t *testing.T) {
	r := newTestRouter("s3cr3t-token")

	w := doGet(r, "bearer s3cr3t-token")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuth_RejectsInvalidOrMissing(t *testing.T) {
	r := newTestRouter("s3cr3t-token")

	tests := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"wrong token", "Bearer wrong-token"},
		{"no scheme", "s3cr3t-token"},
		{"wrong scheme", "Basic s3cr3t-token"},
		{"empty bearer", "Bearer "},
		{"prefix only", "Bearer"},
		{"token is a prefix of expected", "Bearer s3cr3t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doGet(r, tt.header)
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, "Bearer", w.Header().Get("WWW-Authenticate"))
		})
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
	}
	for _, tt := range tests {
		got, ok := bearerToken(tt.header)
		assert.Equal(t, tt.wantOk, ok, "header=%q", tt.header)
		assert.Equal(t, tt.want, got, "header=%q", tt.header)
	}
}
