package middleware

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxBodyBytes is the largest request body, in bytes, accepted by write
// endpoints that bind a JSON payload. No legitimate zone, qube, credential,
// infrastructure or settings configuration approaches 1 MiB, while the cap is
// small enough to read defensively and reject oversized requests early.
const MaxBodyBytes int64 = 1 << 20 // 1 MiB

// MaxBodySize aborts requests whose body exceeds limit, before they reach the
// handler, with 413 RequestEntityTooLarge. Bodies at or under the limit are
// read once and restored unchanged, so the handler sees exactly the bytes the
// client sent. The limit is enforced on what the body actually yields — reads
// are bounded by io.LimitReader at limit+1 bytes — so it also holds for
// chunked bodies and never trusts a Content-Length header.
func MaxBodySize(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil {
			c.Next()
			return
		}

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": http.StatusText(http.StatusBadRequest),
				"code":  http.StatusBadRequest,
			})
			return
		}
		if int64(len(body)) > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": http.StatusText(http.StatusRequestEntityTooLarge),
				"code":  http.StatusRequestEntityTooLarge,
			})
			return
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Next()
	}
}