package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// defaultMaxBodyBytes matches config.DefaultMaxBodyBytes; duplicated here so the
// middleware package does not import config (config already imports middleware).
const defaultMaxBodyBytes int64 = 1 << 20

// BodyLimit caps the number of bytes read from a request body.
//
// It wraps the body in http.MaxBytesReader, so reads past the limit fail with
// *http.MaxBytesError. Handlers that bind the body surface that error, and
// handler.respondError maps it to 413. Applied per request, this stops a single
// client from growing console memory without bound.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBodyBytes
	}
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}
