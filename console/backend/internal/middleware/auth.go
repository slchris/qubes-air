// Package middleware provides HTTP middleware for the Qubes Air console.
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Auth returns a Gin middleware that requires a Bearer token matching apiToken
// on every request. If apiToken is empty, authentication is disabled and all
// requests pass through (a warning should be logged by the caller at startup).
//
// The token is compared in constant time to avoid leaking its length or
// contents through timing side channels.
func Auth(apiToken string) gin.HandlerFunc {
	// Precompute once; the closure captures the fixed token.
	authDisabled := apiToken == ""
	expected := []byte(apiToken)

	return func(c *gin.Context) {
		if authDisabled {
			c.Next()
			return
		}

		token, ok := bearerToken(c.Request.Header.Get("Authorization"))
		if !ok || !constantTimeEqual([]byte(token), expected) {
			c.Header("WWW-Authenticate", "Bearer")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Unauthorized",
				"code":  http.StatusUnauthorized,
			})
			return
		}

		c.Next()
	}
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header value. It returns ok=false when the header is missing or malformed.
func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// constantTimeEqual reports whether a and b are equal without leaking their
// contents or length difference through timing.
func constantTimeEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
