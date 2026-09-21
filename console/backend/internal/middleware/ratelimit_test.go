package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRateLimitBurstThenRejectAndRefill(t *testing.T) {
	gin.SetMode(gin.TestMode)

	lim := NewRateLimiter(1, 2)
	base := time.Now()
	lim.now = func() time.Time { return base }

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(SubjectContextKey, "alice")
		c.Next()
	})
	r.Use(RateLimit(lim))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func() int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.ServeHTTP(w, req)
		return w.Code
	}

	if got := do(); got != http.StatusOK {
		t.Fatalf("request 1: got %d, want 200", got)
	}
	if got := do(); got != http.StatusOK {
		t.Fatalf("request 2: got %d, want 200", got)
	}
	if got := do(); got != http.StatusTooManyRequests {
		t.Fatalf("request 3: got %d, want 429", got)
	}

	// One second at 1 req/s refills exactly one token.
	base = base.Add(time.Second)
	if got := do(); got != http.StatusOK {
		t.Fatalf("after refill: got %d, want 200", got)
	}
}

func TestRateLimitSeparatesSubjects(t *testing.T) {
	gin.SetMode(gin.TestMode)

	lim := NewRateLimiter(1, 1)
	lim.now = func() time.Time { return time.Now() }

	subject := "alice"
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(SubjectContextKey, subject)
		c.Next()
	})
	r.Use(RateLimit(lim))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func() int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.ServeHTTP(w, req)
		return w.Code
	}

	if got := do(); got != http.StatusOK {
		t.Fatalf("alice first: got %d, want 200", got)
	}
	if got := do(); got != http.StatusTooManyRequests {
		t.Fatalf("alice second: got %d, want 429", got)
	}

	// A different subject has its own bucket and must not be blocked.
	subject = "bob"
	if got := do(); got != http.StatusOK {
		t.Fatalf("bob first: got %d, want 200", got)
	}
}
