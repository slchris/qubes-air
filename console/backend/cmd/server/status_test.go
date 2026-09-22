package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestStatusReportsTheStampedVersion — /status is the second route that serves
// the build version, and the last place the removed `appVersion` constant was
// read. It has to answer from the same stamp /health does (the fixture is
// buildForTest, shared with health_test.go): two routes reporting two builds
// would be the second source of truth this change exists to remove.
func TestStatusReportsTheStampedVersion(t *testing.T) {
	build := buildForTest()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/status", nil)
	statusHandler(nil, build)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("/status: code = %d, want 200", w.Code)
	}

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode /status body %q: %v", w.Body.String(), err)
	}
	if got := raw["version"]; got != build.Version {
		t.Errorf("/status version = %v, want %q", got, build.Version)
	}
	// The route's other field is unchanged; asserting it here keeps the shape
	// pinned, so a future edit cannot quietly drop it while adding version data.
	if got := raw["name"]; got != appName {
		t.Errorf("/status name = %v, want %q", got, appName)
	}
}
