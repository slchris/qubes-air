package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/audit"
)

// Audit records every MUTATING API request as a structured entry.
//
// Reads are not recorded: the trail is for actions that change state, and
// logging every list/poll would bury them. The request body is never recorded —
// mutating bodies can carry secrets (a credential value, the token in the login
// exchange), and an audit log must not become the leak.
//
// Subject comes from the resolved credential (Bearer token or session); Source
// is the client address. An unauthenticated mutation (a failed login attempt, or
// one that never got past auth) records an empty subject with outcome "denied".
func Audit(rec *audit.Recorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()

		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return
		}

		subject, _ := SubjectFromContext(c)
		status := c.Writer.Status()
		rec.Record(audit.Entry{
			Subject:   subject,
			Source:    c.ClientIP(),
			Method:    c.Request.Method,
			Route:     c.FullPath(),
			Object:    audit.Object(c.Param),
			Status:    status,
			Outcome:   audit.Outcome(status),
			LatencyMS: time.Since(started).Milliseconds(),
		})
	}
}
