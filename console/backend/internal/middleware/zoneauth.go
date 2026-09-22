package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ObjectResolver maps an object ID to the zone that owns it. found=false means
// the object does not exist; an error means the answer is unknown and the
// request must fail closed.
type ObjectResolver interface {
	ZoneOfQube(ctx context.Context, qubeID string) (zoneID string, found bool, err error)
	ZoneOfJob(ctx context.Context, jobID string) (zoneID string, found bool, err error)
}

// fleetPrefixes are endpoints that manage fleet-wide resources or aggregate
// across zones. A zone-restricted credential is refused outright: filtering
// them would either leak other zones' data or turn a fleet view into a
// half-truth.
var fleetPrefixes = []string{
	"/api/v1/credentials",
	"/api/v1/infrastructure",
	"/api/v1/settings",
	"/api/v1/monitoring",
	"/api/v1/billing",
	"/api/v1/status",
}

// maxZoneProbeBytes bounds how much of a create body is read to find zone_id.
// It is generous for a spec payload and small enough that a hostile body cannot
// make this middleware a memory amplifier; bodies over the cap are refused
// rather than guessed at.
const maxZoneProbeBytes = 2 << 20

// RequireZones enforces the object-level zone allowlist carried by a token or
// session. Fleet-wide credentials (no zone list) and an authentication-disabled
// console pass through unchanged.
//
// Decisions:
//   - an object in another zone, or one that does not exist, returns 404: the
//     response must not tell a restricted caller whether the ID is real;
//   - a fleet-only endpoint returns 403;
//   - an unresolvable object (repository failure, oversized create body)
//     returns 500/403 and never falls through.
func RequireZones(resolver ObjectResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		zones, restricted := ZoneScopeFromContext(c)
		if !restricted {
			c.Next()
			return
		}
		route := c.FullPath()
		if resolver == nil {
			// A restricted credential with no way to resolve ownership can only
			// be served by refusing; this is a wiring bug, not a client error.
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": "Internal Server Error",
				"code":  http.StatusInternalServerError,
			})
			return
		}

		switch {
		case route == "/api/v1/session":
			// Logging in and out is self-service; the session never exceeds the
			// zones of the token it was created from.
			c.Next()
		case isFleetRoute(route):
			forbiddenZone(c)
		case strings.HasPrefix(route, "/api/v1/zones"):
			allowZoneRoute(c, zones, route)
		case strings.HasPrefix(route, "/api/v1/qubes"):
			allowQubeRoute(c, resolver, zones, route)
		case route == "/api/v1/jobs":
			// The unqualified listing aggregates every zone's jobs.
			forbiddenZone(c)
		case strings.HasPrefix(route, "/api/v1/jobs/"):
			allowObjectRoute(c, func() (string, bool, error) {
				return resolver.ZoneOfJob(c.Request.Context(), c.Param("id"))
			}, zones)
		default:
			// Unclassified route: a new endpoint must be added to this switch
			// deliberately, so an unknown one is refused rather than exposed.
			forbiddenZone(c)
		}
	}
}

func isFleetRoute(route string) bool {
	for _, prefix := range fleetPrefixes {
		if route == prefix || strings.HasPrefix(route, prefix+"/") {
			return true
		}
	}
	return false
}

// allowZoneRoute handles /zones and /zones/:id... . Listing is filtered by the
// handler; a zone object may only be addressed when it is in the allowlist.
func allowZoneRoute(c *gin.Context, zones []string, route string) {
	if route == "/api/v1/zones" {
		if c.Request.Method == http.MethodPost {
			// Creating a zone adds fleet infrastructure; the console has no
			// concept of a zone-scoped administrator for a zone that does not
			// exist yet.
			forbiddenZone(c)
			return
		}
		c.Next()
		return
	}
	if !zoneAllowed(zones, c.Param("id")) {
		notFoundObject(c)
		return
	}
	c.Next()
}

// allowQubeRoute handles /qubes and /qubes/:id... . Creating a qube names its
// zone in the body; every other object route resolves the qube's zone.
func allowQubeRoute(c *gin.Context, resolver ObjectResolver, zones []string, route string) {
	if route == "/api/v1/qubes" {
		if c.Request.Method == http.MethodPost {
			zoneID, ok := requestZoneID(c)
			if !ok || !zoneAllowed(zones, zoneID) {
				forbiddenZone(c)
				return
			}
		}
		c.Next()
		return
	}
	allowObjectRoute(c, func() (string, bool, error) {
		return resolver.ZoneOfQube(c.Request.Context(), c.Param("id"))
	}, zones)
}

// allowObjectRoute resolves an object's zone and lets the request through only
// when it is in the allowlist. A missing object and a foreign object are
// answered identically.
func allowObjectRoute(c *gin.Context, resolve func() (string, bool, error), zones []string) {
	zoneID, found, err := resolve()
	if err != nil {
		// Fail closed: if ownership cannot be established, the object is not
		// addressable by a restricted caller.
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"error": "Internal Server Error",
			"code":  http.StatusInternalServerError,
		})
		return
	}
	if !found || !zoneAllowed(zones, zoneID) {
		notFoundObject(c)
		return
	}
	c.Next()
}

func zoneAllowed(zones []string, zoneID string) bool {
	if zoneID == "" {
		return false
	}
	for _, allowed := range zones {
		if allowed == zoneID {
			return true
		}
	}
	return false
}

// requestZoneID reads zone_id from a JSON request body and restores the body
// for the handler. ok=false means the body could not be read or parsed; the
// caller must refuse rather than assume a zone.
func requestZoneID(c *gin.Context) (string, bool) {
	if c.Request.Body == nil {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxZoneProbeBytes+1))
	if err != nil || len(body) > maxZoneProbeBytes {
		return "", false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	var probe struct {
		ZoneID string `json:"zone_id"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", false
	}
	return probe.ZoneID, true
}

func forbiddenZone(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error": "Forbidden",
		"code":  http.StatusForbidden,
	})
}

func notFoundObject(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
		"error": "Not Found",
		"code":  http.StatusNotFound,
	})
}
