// Package middleware provides HTTP middleware for the Qubes Air console.
package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Scope is the permission scope a bearer token grants on the Console API.
type Scope string

// Valid scopes. The scope is bound to a token at configuration time, and
// RequireControl then classifies each request BY METHOD rather than by route.
const (
	ScopeReadOnly Scope = "read-only"
	ScopeControl  Scope = "control"
)

// ValidScope reports whether s is a scope the configuration may assign to a
// token.
func ValidScope(s string) bool {
	return Scope(s) == ScopeReadOnly || Scope(s) == ScopeControl
}

// Context keys set by ScopedAuth.
const (
	// ScopeContextKey holds the granted scope (a string, see ScopeFromContext).
	ScopeContextKey = "middleware.auth.scope"
	// SubjectContextKey holds the credential's name (never its value), used for
	// operation audit. See SubjectFromContext.
	SubjectContextKey = "middleware.auth.subject"
)

// sessionLoginPath is the one route exempt from the Bearer requirement: it
// authenticates the token from its own request body so a browser can exchange
// the token for a session cookie.
const sessionLoginPath = "/api/v1/session"

// Token is one accepted bearer credential and the scope it grants.
type Token struct {
	// Name labels the token in logs; it carries no authority.
	Name string
	// Value is the expected token bytes.
	Value string
	// Scope is the scope granted to a request using this token.
	Scope Scope
}

// credential is the prehashed form of a Token, so every request matches
// against fixed-size digests rather than the raw token bytes.
type credential struct {
	name   string
	digest [32]byte
	scope  Scope
}

// newCredentials builds the matcher list: the legacy single api_token, when
// configured, is accepted as a control-scope credential (it is the
// administrator token, not a compatibility branch).
func newCredentials(apiToken string, scoped []Token) []credential {
	n := len(scoped)
	if apiToken != "" {
		n++
	}
	creds := make([]credential, 0, n)
	if apiToken != "" {
		creds = append(creds, credential{
			name:   "api_token",
			digest: sha256.Sum256([]byte(apiToken)),
			scope:  ScopeControl,
		})
	}
	for _, t := range scoped {
		creds = append(creds, credential{
			name:   t.Name,
			digest: sha256.Sum256([]byte(t.Value)),
			scope:  t.Scope,
		})
	}
	return creds
}

// MatchToken reports whether candidate is a configured credential, returning its
// subject label (never the value) and scope. The session endpoint authenticates
// the token from its request body through this.
func MatchToken(apiToken string, scoped []Token, candidate string) (subject string, scope Scope, ok bool) {
	return matchCredential(candidate, newCredentials(apiToken, scoped))
}

// ScopedAuth returns a Gin middleware that authenticates every request with EITHER
// a valid session cookie OR a Bearer token. apiToken and the entries of scoped
// form the credential set; the legacy single api_token is treated as control
// scope. When NO credential is configured, authentication is DISABLED and all
// requests pass through (the caller should log a startup warning).
//
// On success the granted scope and the subject label are stored in the request
// context for RequireControl and the audit log. Matching runs in constant time
// across the WHOLE credential list: every candidate is hashed once and compared
// against all prehashed digests, never returning early, so neither the number of
// credentials nor any single credential's length leaks through the comparison.
func ScopedAuth(apiToken string, scoped []Token, sessions *SessionStore) gin.HandlerFunc {
	creds := newCredentials(apiToken, scoped)
	authDisabled := len(creds) == 0

	return func(c *gin.Context) {
		if authDisabled {
			c.Next()
			return
		}

		// The login endpoint authenticates the token in its own body; requiring a
		// Bearer here would make exchanging a token for a session impossible.
		if c.Request.Method == http.MethodPost && c.Request.URL.Path == sessionLoginPath {
			c.Next()
			return
		}

		if sessions != nil {
			if cookie, err := c.Cookie(SessionCookieName); err == nil {
				if sess, ok := sessions.Get(cookie); ok {
					setAuthContext(c, sess.Subject, sess.Scope)
					c.Next()
					return
				}
			}
		}

		token, ok := bearerToken(c.Request.Header.Get("Authorization"))
		if !ok {
			unauthorized(c)
			return
		}
		subject, scope, ok := matchCredential(token, creds)
		if !ok {
			unauthorized(c)
			return
		}
		setAuthContext(c, subject, scope)
		c.Next()
	}
}

func setAuthContext(c *gin.Context, subject string, scope Scope) {
	c.Set(ScopeContextKey, string(scope))
	c.Set(SubjectContextKey, subject)
}

// matchCredential returns the subject label and scope granted by candidate,
// scanning every credential in constant time. Comparing SHA-256 digests of a
// fixed length removes the raw length difference from the comparison, and
// iterating the whole list without an early return means which position matched
// (or that none did) does not leak either.
func matchCredential(candidate string, creds []credential) (string, Scope, bool) {
	digest := sha256.Sum256([]byte(candidate))
	var (
		subject string
		found   Scope
		ok      bool
	)
	for _, cd := range creds {
		if subtle.ConstantTimeCompare(digest[:], cd.digest[:]) == 1 {
			subject = cd.name
			found = cd.scope
			ok = true
		}
	}
	return subject, found, ok
}

// RequireControl enforces the FAIL-CLOSED method rule: the permissive methods
// (GET/HEAD/OPTIONS) are read-only and may be performed by any authenticated
// scope; EVERY other method requires control scope.
//
// The decision is made BY METHOD, not by annotating each route, on purpose: a
// write endpoint added later is restricted the moment it is registered, so a
// new route cannot forget to opt in to protection. ScopedAuth must run before
// this middleware on the same group, so the granted scope is already in the
// context. When authentication is disabled (ScopedAuth set no scope) the
// request passes through, preserving the historic no-auth behavior.
func RequireControl() gin.HandlerFunc {
	return func(c *gin.Context) {
		scope, authed := ScopeFromContext(c)
		if !authed {
			c.Next()
			return
		}
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if scope == string(ScopeControl) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": "Forbidden",
			"code":  http.StatusForbidden,
		})
	}
}

// ScopeFromContext returns the scope ScopedAuth granted to this request.
// authed is false when authentication is disabled, in which case the request
// carries no scope.
func ScopeFromContext(c *gin.Context) (string, bool) {
	v, ok := c.Get(ScopeContextKey)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// SubjectFromContext returns the credential name ScopedAuth authenticated, or
// ("", false) when authentication is disabled. It is for audit only and carries
// no authority.
func SubjectFromContext(c *gin.Context) (string, bool) {
	v, ok := c.Get(SubjectContextKey)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// unauthorized aborts with 401 and the Bearer challenge.
func unauthorized(c *gin.Context) {
	c.Header("WWW-Authenticate", "Bearer")
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"error": "Unauthorized",
		"code":  http.StatusUnauthorized,
	})
}

// maxBearerTokenLen bounds how large a bearer token is worth hashing. gin does
// not cap header sizes, so without the cap an over-long Authorization header
// would be hashed on every request.
const maxBearerTokenLen = 4096

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header value. It returns ok=false when the header is missing, malformed,
// empty, or carries a token beyond maxBearerTokenLen.
func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" || len(token) > maxBearerTokenLen {
		return "", false
	}
	return token, true
}
