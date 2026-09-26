package middleware

import (
	"crypto/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/audit"
)

// RequestIDHeader is the response header that carries the audit request ID
// back to the caller of every audited (mutating) request, so a reported
// failure can be matched to its audit line.
const RequestIDHeader = "X-Request-Id"

// deniedContextKey marks a request that a policy middleware refused. Audit
// classifies such a request as denied whatever status the refusal used:
// RequireZones answers a foreign object with 404 so the caller cannot tell it
// exists, but the operator reading the audit trail must still see a denial.
const deniedContextKey = "middleware.authz.denied"

// markDenied records that the current request was refused by an authorization
// decision. The 401, 403 and zone 404 refusals in this package call it before
// aborting; throttling (429) and fail-closed errors (500) do not, because they
// are not authorization decisions and their status already classifies them.
func markDenied(c *gin.Context) {
	c.Set(deniedContextKey, true)
}

// Audit records every MUTATING API request as a structured entry, including the
// ones the rest of the chain refuses.
//
// It must be the OUTERMOST middleware of the API group: it records after the
// chain has run, so a request turned away by authentication (401), rate
// limiting (429), the method scope rule (403) or the zone allowlist (403/404)
// is recorded exactly like one that reached a handler. Identity is read from
// the context only after c.Next(), so whatever ScopedAuth resolved is still
// attributed even though Audit runs first.
//
// Reads are not recorded: the trail is for actions that change state, and
// logging every list/poll would bury them. The request body and headers are
// never recorded — mutating bodies can carry secrets (a credential value, the
// token in the login exchange), the Authorization header and session cookie
// are credentials, and an audit log must not become the leak.
//
// Subject comes from the resolved credential (Bearer token or session); Source
// is the client address. A request with no resolved credential (a failed or
// missing Bearer, an unknown session, a login attempt) is recorded as
// unauthenticated, which the recorder renders as the anonymous subject.
//
// The request ID is generated here and never taken from the client: a caller
// that could choose it could make its request collide with another's line.
func Audit(rec *audit.Recorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isMutating(c.Request.Method) {
			c.Next()
			return
		}

		started := time.Now()
		requestID := rand.Text()
		// Set before the chain runs: a refusal writes the response inside
		// c.Next(), after which headers can no longer change.
		c.Header(RequestIDHeader, requestID)
		c.Next()

		rec.Record(auditEntry(c, requestID, started))
	}
}

// isMutating reports whether method can change state. GET/HEAD/OPTIONS are the
// same permissive set RequireControl opens to read-only scopes.
func isMutating(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// auditEntry builds the entry for a request whose chain has finished.
func auditEntry(c *gin.Context, requestID string, started time.Time) audit.Entry {
	subject, authenticated := SubjectFromContext(c)
	zones, _ := ZoneScopeFromContext(c)
	status := c.Writer.Status()
	outcome := audit.Outcome(status)
	if c.GetBool(deniedContextKey) {
		outcome = audit.OutcomeDenied
	}
	return audit.Entry{
		RequestID:     requestID,
		Authenticated: authenticated,
		Subject:       subject,
		Source:        c.ClientIP(),
		Method:        c.Request.Method,
		Route:         c.FullPath(),
		Object:        audit.Object(c.Param),
		Status:        status,
		Outcome:       outcome,
		LatencyMS:     time.Since(started).Milliseconds(),
		ZoneScope:     zones,
	}
}
