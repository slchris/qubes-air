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

// ScopeContextKey is the gin context key holding the authenticated requests'
// granted scope (a string, see ScopeFromContext).
const ScopeContextKey = "middleware.auth.scope"

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

// ScopedAuth returns a Gin middleware that requires a valid Bearer token on
// every request. apiToken and the entries of scoped form the credential set;
// the legacy single api_token is treated as control scope. When NO credential
// is configured, authentication is DISABLED and all requests pass through (the
// caller should log a startup warning).
//
// On success the granted scope is stored in the request context under
// ScopeContextKey so downstream middleware (RequireControl) can read it.
// Matching runs in constant time across the WHOLE credential list: every
// candidate is hashed once and compared against all prehashed digests, never
// returning early, so neither the number of credentials nor any single
// credential's length leaks through the comparison.
func ScopedAuth(apiToken string, scoped []Token) gin.HandlerFunc {
	creds := newCredentials(apiToken, scoped)
	authDisabled := len(creds) == 0

	return func(c *gin.Context) {
		if authDisabled {
			c.Next()
			return
		}

		token, ok := bearerToken(c.Request.Header.Get("Authorization"))
		if !ok {
			unauthorized(c)
			return
		}
		scope, ok := matchScope(token, creds)
		if !ok {
			unauthorized(c)
			return
		}
		c.Set(ScopeContextKey, string(scope))
		c.Next()
	}
}

// matchScope returns the scope granted by candidate, scanning every credential
// in constant time. Comparing SHA-256 digests of a fixed length removes the
// raw length difference from the comparison, and iterating the whole list
// without an early return means which position matched (or that none did) does
// not leak either.
func matchScope(candidate string, creds []credential) (Scope, bool) {
	digest := sha256.Sum256([]byte(candidate))
	var (
		found Scope
		ok    bool
	)
	for _, cd := range creds {
		if subtle.ConstantTimeCompare(digest[:], cd.digest[:]) == 1 {
			found = cd.scope
			ok = true
		}
	}
	return found, ok
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
