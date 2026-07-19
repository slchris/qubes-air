package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/config"
)

// newWebUIRouter builds a bare engine with only the web UI registered, so these
// tests exercise the fallback without needing a database or the API handlers.
func newWebUIRouter(t *testing.T, webRoot string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerWebUI(r, &config.Config{Server: config.ServerConfig{WebRoot: webRoot}})
	return r
}

// writeDist lays out a minimal built frontend: index.html plus one asset.
func writeDist(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><title>console</title>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("export default 1"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	return root
}

func get(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// The point of the exclusion in the fallback: an unknown /api path must stay a
// JSON 404. If it returned index.html with 200, a client expecting JSON would
// fail to parse HTML somewhere far from the URL that was actually wrong.
func TestWebUIDoesNotSwallowAPINotFound(t *testing.T) {
	r := newWebUIRouter(t, writeDist(t))

	for _, path := range []string{"/api/v1/nope", "/api/", "/health"} {
		w := get(t, r, path)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "<!doctype html") {
			t.Errorf("%s: served the SPA index instead of a JSON 404", path)
		}
	}
}

// A client-side route must survive a reload rather than 404.
func TestWebUIServesIndexForClientRoutes(t *testing.T) {
	r := newWebUIRouter(t, writeDist(t))

	for _, path := range []string{"/", "/qubes", "/zones/abc"} {
		w := get(t, r, path)
		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "<title>console</title>") {
			t.Errorf("%s: did not serve index.html", path)
		}
	}
}

func TestWebUIServesAssets(t *testing.T) {
	r := newWebUIRouter(t, writeDist(t))

	w := get(t, r, "/assets/app.js")
	if w.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "export default 1" {
		t.Errorf("asset body = %q", got)
	}
}

// Non-GET verbs are not client-side routes; answering them with the SPA index
// would report success for a write that no handler ever ran.
func TestWebUIFallbackIsGETOnly(t *testing.T) {
	r := newWebUIRouter(t, writeDist(t))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/qubes", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("POST fallback status = %d, want 404", w.Code)
	}
}

// The console must still start and serve its API when the UI is absent or
// unconfigured — a missing directory of static files is not an outage.
func TestWebUIDisabledWhenUnset(t *testing.T) {
	r := newWebUIRouter(t, "")
	if w := get(t, r, "/"); w.Code != http.StatusNotFound {
		t.Errorf("unset web root: status = %d, want 404 (no routes registered)", w.Code)
	}
}

func TestWebUIDisabledWhenIndexMissing(t *testing.T) {
	r := newWebUIRouter(t, t.TempDir())
	if w := get(t, r, "/"); w.Code != http.StatusNotFound {
		t.Errorf("empty web root: status = %d, want 404 (no routes registered)", w.Code)
	}
}
