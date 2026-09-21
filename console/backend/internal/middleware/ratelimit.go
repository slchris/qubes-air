package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// defaultRate and defaultBurst are used when a non-positive value is configured.
const (
	defaultRate  = 20.0
	defaultBurst = 40
)

type visitor struct {
	tokens   float64
	lastSeen time.Time
}

// RateLimiter is a per-client token bucket.
//
// The key is the authenticated subject when there is one, otherwise the client
// address. Keying by subject means one operator's runaway tab cannot consume
// another's budget, and an unauthenticated caller cannot escape the limit by
// changing its token.
type RateLimiter struct {
	mu        sync.Mutex
	visitors  map[string]*visitor
	rate      float64
	burst     float64
	now       func() time.Time
	lastSweep time.Time
}

// NewRateLimiter builds a limiter. Non-positive values fall back to the
// defaults, so a misconfigured zero cannot disable the limit entirely.
func NewRateLimiter(ratePerSec float64, burst int) *RateLimiter {
	if ratePerSec <= 0 {
		ratePerSec = defaultRate
	}
	if burst <= 0 {
		burst = defaultBurst
	}
	return &RateLimiter{
		visitors: make(map[string]*visitor),
		rate:     ratePerSec,
		burst:    float64(burst),
		now:      time.Now,
	}
}

func (l *RateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	v, ok := l.visitors[key]
	if !ok {
		v = &visitor{tokens: l.burst}
		l.visitors[key] = v
	}
	elapsed := now.Sub(v.lastSeen).Seconds()
	v.lastSeen = now
	v.tokens += elapsed * l.rate
	if v.tokens > l.burst {
		v.tokens = l.burst
	}
	if v.tokens < 1 {
		need := (1 - v.tokens) / l.rate
		return false, time.Duration(need * float64(time.Second))
	}
	v.tokens--
	if now.Sub(l.lastSweep) > time.Minute {
		l.sweep(now)
		l.lastSweep = now
	}
	return true, 0
}

// sweep drops idle buckets so the map cannot grow without bound. Callers hold l.mu.
func (l *RateLimiter) sweep(now time.Time) {
	for key, v := range l.visitors {
		if now.Sub(v.lastSeen) > 10*time.Minute {
			delete(l.visitors, key)
		}
	}
}

// RateLimit throttles requests by subject (preferred) or client address and
// answers 429 with Retry-After when the bucket is empty.
func RateLimit(l *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.ClientIP()
		if subject, ok := SubjectFromContext(c); ok && subject != "" {
			key = "subject:" + subject
		} else {
			key = "addr:" + key
		}
		if ok, retry := l.allow(key); !ok {
			c.Header("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": http.StatusText(http.StatusTooManyRequests),
				"code":  http.StatusTooManyRequests,
			})
			return
		}
		c.Next()
	}
}
