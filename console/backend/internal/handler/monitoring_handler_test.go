package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/slchris/qubes-air/console/internal/systemmetrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hostSamplerStub struct{ metrics systemmetrics.Metrics }

func (s hostSamplerStub) Collect() systemmetrics.Metrics { return s.metrics }

type qubeMetricsStub struct {
	items []service.QubeRuntimeMetrics
	err   error
}

func (s qubeMetricsStub) Collect(context.Context) ([]service.QubeRuntimeMetrics, error) {
	return s.items, s.err
}

func measuredHost() hostSamplerStub {
	cpu, mem := 12.5, 40.0
	return hostSamplerStub{metrics: systemmetrics.Metrics{
		CPUUsage: &cpu, MemoryUsage: &mem, Source: "console-host-linux",
		CapturedAt: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), Reason: "some counters are unavailable",
	}}
}

// serveMonitoring mounts the handler and answers one request.
func serveMonitoring(t *testing.T, h *MonitoringHandler, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h.RegisterRoutes(router.Group("/api"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body), recorder.Body.String())
	return recorder, body
}

func TestMonitoringOverviewReportsHostMetricsWithTheirScope(t *testing.T) {
	recorder, body := serveMonitoring(t, NewMonitoringHandler(measuredHost(), nil), http.MethodGet, "/api/monitoring")

	require.Equal(t, http.StatusOK, recorder.Code)
	metrics, ok := body["metrics"].(map[string]any)
	require.True(t, ok, "metrics object: %v", body)
	assert.InDelta(t, 12.5, metrics["cpuUsage"], 0.001)
	assert.Nil(t, metrics["diskUsage"], "an unmeasured value is null, not 0")
	assert.Equal(t, "console-host-linux", metrics["source"])
	assert.Equal(t, "2026-09-26T12:00:00Z", metrics["capturedAt"])
	assert.Contains(t, body["note"], "do not describe managed qubes")
	assert.NotContains(t, body, "placeholder", "host metrics are measured, not placeholders")
	assert.Equal(t, []any{}, body["alerts"])
	assert.Equal(t, "not_implemented", body["alerts_status"], "an empty alert list must not read as a healthy fleet")
}

func TestMonitoringMetricsReportsAnUnsupportedHostAsMissingValues(t *testing.T) {
	host := hostSamplerStub{metrics: systemmetrics.Metrics{Source: "unsupported-platform", Reason: "Linux only"}}
	recorder, body := serveMonitoring(t, NewMonitoringHandler(host, nil), http.MethodGet, "/api/monitoring/metrics")

	require.Equal(t, http.StatusOK, recorder.Code)
	metrics, ok := body["metrics"].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"cpuUsage", "memoryUsage", "diskUsage", "networkIn", "networkOut"} {
		value, present := metrics[key]
		assert.True(t, present, key)
		assert.Nil(t, value, "%s must be null when it was not measured", key)
	}
	assert.Equal(t, "Linux only", metrics["reason"])
	assert.Contains(t, body["note"], "Host-wide")
}

func TestMonitoringAlertsSayAlertingIsNotImplemented(t *testing.T) {
	recorder, body := serveMonitoring(t, NewMonitoringHandler(measuredHost(), nil), http.MethodGet, "/api/monitoring/alerts")

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, []any{}, body["alerts"])
	assert.InDelta(t, 0, body["total"], 0)
	assert.Equal(t, "not_implemented", body["alerts_status"])
}

func TestQubeMetricsEndpointReturnsCollectorState(t *testing.T) {
	cpu := 0.25
	h := NewMonitoringHandler(measuredHost(), qubeMetricsStub{items: []service.QubeRuntimeMetrics{
		{QubeID: "q1", QubeName: "work", ZoneID: "z1", State: "running",
			Metrics: &provider.RuntimeMetrics{Source: "proxmox-qemu-status-current", CPUFraction: &cpu}},
		{QubeID: "q2", QubeName: "idle", ZoneID: "z1", State: "stopped", Reason: service.RuntimeMetricsNotRunning},
	}})

	recorder, body := serveMonitoring(t, h, http.MethodGet, "/api/monitoring/qubes")

	require.Equal(t, http.StatusOK, recorder.Code)
	items, ok := body["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)
	first, ok := items[0].(map[string]any)
	require.True(t, ok)
	measured, ok := first["metrics"].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, 0.25, measured["cpu_fraction"], 0.0001)
	assert.NotContains(t, measured, "memory_used_bytes", "a value the provider omitted is absent, not 0")
	second, ok := items[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "qube_not_running", second["reason"])
	assert.NotContains(t, second, "metrics")
}

func TestQubeMetricsEndpointReportsAnUnconfiguredCollector(t *testing.T) {
	recorder, body := serveMonitoring(t, NewMonitoringHandler(measuredHost(), nil), http.MethodGet, "/api/monitoring/qubes")

	require.Equal(t, http.StatusNotImplemented, recorder.Code)
	assert.Equal(t, "provider runtime metrics are not configured", body["message"])
}

// The collector's error can carry a database path or a provider address. It is
// logged for the operator and replaced by a fixed message for the caller.
func TestQubeMetricsFailureIsLoggedAndNotReturned(t *testing.T) {
	const internal = "list qubes for runtime metrics: open /var/lib/qubes-air/console.db: disk I/O error"
	h := NewMonitoringHandler(measuredHost(), qubeMetricsStub{err: errors.New(internal)})
	var logged []string
	h.logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	recorder, body := serveMonitoring(t, h, http.MethodGet, "/api/monitoring/qubes")

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, "provider runtime metrics are unavailable", body["message"])
	assert.InDelta(t, http.StatusServiceUnavailable, body["code"], 0)
	assert.NotContains(t, recorder.Body.String(), "/var/lib/qubes-air")
	assert.NotContains(t, recorder.Body.String(), "disk I/O error")
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], internal)
}

// Until alerts are persisted there is nothing to acknowledge; the stub must
// say so instead of confirming an acknowledgement that was never recorded.
func TestAcknowledgeAlertIsNotImplemented(t *testing.T) {
	for _, id := range []string{"a1", "zone-disconnected%3Az1"} {
		recorder, body := serveMonitoring(t, NewMonitoringHandler(measuredHost(), nil),
			http.MethodPost, "/api/monitoring/alerts/"+id+"/acknowledge")

		require.Equal(t, http.StatusNotImplemented, recorder.Code, id)
		assert.Equal(t, "alert acknowledgement is not implemented", body["message"])
		assert.NotContains(t, body, "acknowledged")
	}
}
