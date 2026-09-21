package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/middleware"
)

// RevocationSource signs public status using the existing fleet CA.
type RevocationSource interface {
	RevocationDocument(context.Context) ([]byte, error)
}

// RegisterRevocations serves only signed public hashes. There is no bearer
// secret on this route: authenticity is verified against the agent's pinned CA.
// Requests have no body, a five-second deadline and a per-client rate limit.
func RegisterRevocations(r *gin.Engine, source RevocationSource) {
	r.GET("/pki/revocations", middleware.RateLimit(middleware.NewRateLimiter(5, 10)), func(c *gin.Context) {
		started := time.Now()
		defer func() {
			slog.Info("pki status read", "route", "/pki/revocations", "method", c.Request.Method,
				"subject", "public", "source", c.ClientIP(), "status", c.Writer.Status(), "latency_ms", time.Since(started).Milliseconds())
		}()
		c.Header("Cache-Control", "no-store")
		if c.Request.ContentLength != 0 {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		if source == nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		document, err := source.RevocationDocument(ctx)
		if err != nil || len(document) > 1024*1024 {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.Data(http.StatusOK, "application/json", document)
	})
}
