package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/handler"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/slchris/qubes-air/console/internal/systemmetrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emptyFleet lists no qubes, so the collector needs no zone, infra or
// provider source.
type emptyFleet struct{}

func (emptyFleet) List(context.Context, repository.QubeListOptions) ([]*models.Qube, error) {
	return nil, nil
}

var monitoringReadPaths = []string{
	"/api/v1/monitoring",
	"/api/v1/monitoring/metrics",
	"/api/v1/monitoring/qubes",
	"/api/v1/monitoring/alerts",
}

// TestMonitoringRoutesAreFleetOnlyReads runs the monitoring routes behind the
// production /api/v1 chain. They aggregate every zone, so: no credential is
// refused (401), a zone-restricted credential is refused (403) rather than
// shown other zones' qubes, and any fleet-wide credential, read-only included,
// may read. Reads are not audit events.
func TestMonitoringRoutesAreFleetOnlyReads(t *testing.T) {
	r, buf, _ := auditedAPIWith(t, nil, handler.NewMonitoringHandler(systemmetrics.NewHostSampler(),
		service.NewRuntimeMetricsCollector(emptyFleet{}, nil, nil, nil)).RegisterRoutes)

	for _, path := range monitoringReadPaths {
		t.Run(path, func(t *testing.T) {
			assert.Equal(t, http.StatusUnauthorized, apiRequest(r, http.MethodGet, path, "", nil).Code)
			assert.Equal(t, http.StatusForbidden, apiRequest(r, http.MethodGet, path, "", bearer(zoneTokenValue)).Code,
				"a zone-scoped credential must not see fleet-wide monitoring")
			assert.Equal(t, http.StatusOK, apiRequest(r, http.MethodGet, path, "", bearer(auditorTokenValue)).Code,
				"a fleet-wide read-only credential may read")
			assert.Equal(t, http.StatusOK, apiRequest(r, http.MethodGet, path, "", bearer(adminTokenValue)).Code)
		})
	}
	assert.Empty(t, buf.String(), "monitoring reads are not audit events")
}

// TestMonitoringIsWiredToTheHostSamplerAndProviders boots the real dependency
// graph: /monitoring/metrics must come from the platform's host sampler and
// /monitoring/qubes from the provider collector, not from a placeholder.
func TestMonitoringIsWiredToTheHostSamplerAndProviders(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Mode = gin.TestMode
	cfg.Database.DSN = filepath.Join(t.TempDir(), "console.db")
	deps, err := initDependencies(cfg)
	require.NoError(t, err)
	t.Cleanup(deps.Close)
	router := setupRouter(cfg, deps)

	get := func(path string) (int, map[string]any) {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
		return w.Code, body
	}

	code, body := get("/api/v1/monitoring/metrics")
	require.Equal(t, http.StatusOK, code)
	metrics, ok := body["metrics"].(map[string]any)
	require.True(t, ok)
	want := "unsupported-platform"
	if runtime.GOOS == "linux" {
		want = "console-host-linux"
	}
	assert.Equal(t, want, metrics["source"])

	code, body = get("/api/v1/monitoring/qubes")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []any{}, body["items"], "an empty fleet is an empty list, not an error or null")
}
