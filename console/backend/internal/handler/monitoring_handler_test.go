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
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/slchris/qubes-air/console/internal/repository"
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

// Without stored acknowledgements there is nothing to acknowledge; the stub
// must say so instead of confirming an acknowledgement that was never recorded.
func TestAcknowledgeAlertIsNotImplemented(t *testing.T) {
	for _, id := range []string{"a1", "zone-disconnected%3Az1"} {
		recorder, body := serveMonitoring(t, NewMonitoringHandler(measuredHost(), nil),
			http.MethodPost, "/api/monitoring/alerts/"+id+"/acknowledge")

		require.Equal(t, http.StatusNotImplemented, recorder.Code, id)
		assert.Equal(t, "alert acknowledgement is not implemented", body["message"])
		assert.NotContains(t, body, "acknowledged")
	}
}

// --- alerts derived from persisted health ---

type monitoringZonesStub struct {
	zones []*models.Zone
	err   error
}

func (s monitoringZonesStub) List(_ context.Context, opts repository.ZoneListOptions) ([]*models.Zone, error) {
	if s.err != nil || opts.Offset >= len(s.zones) {
		return nil, s.err
	}
	end := min(opts.Offset+opts.Limit, len(s.zones))
	return s.zones[opts.Offset:end], nil
}

type monitoringQubesStub struct {
	qubes []*models.Qube
	err   error
}

func (s monitoringQubesStub) List(_ context.Context, opts repository.QubeListOptions) ([]*models.Qube, error) {
	if s.err != nil || opts.Offset >= len(s.qubes) {
		return nil, s.err
	}
	end := min(opts.Offset+opts.Limit, len(s.qubes))
	return s.qubes[opts.Offset:end], nil
}

type monitoringAlertEventsStub struct {
	events []repository.MonitoringAlertEvent
	total  int
	err    error
}

func (s monitoringAlertEventsStub) List(_ context.Context, limit, offset int) ([]repository.MonitoringAlertEvent, int, error) {
	if s.err != nil {
		return nil, 0, s.err
	}
	start := min(offset, len(s.events))
	end := min(start+limit, len(s.events))
	return s.events[start:end], s.total, nil
}

type alertAcknowledgementsStub struct {
	records map[string]repository.AlertAcknowledgement
	err     error
}

func (s *alertAcknowledgementsStub) SyncActive(_ context.Context, ids []string) (map[string]repository.AlertAcknowledgement, error) {
	if s.err != nil {
		return nil, s.err
	}
	active := make(map[string]repository.AlertAcknowledgement)
	for _, id := range ids {
		if record, ok := s.records[id]; ok {
			active[id] = record
		}
	}
	for id := range s.records {
		if _, ok := active[id]; !ok {
			delete(s.records, id)
		}
	}
	return active, nil
}

func (s *alertAcknowledgementsStub) Acknowledge(_ context.Context, id, subject string, at time.Time) error {
	if s.err != nil {
		return s.err
	}
	s.records[id] = repository.AlertAcknowledgement{AlertID: id, AcknowledgedBy: subject, AcknowledgedAt: at}
	return nil
}

func newAlertHandler(zones monitoringZoneLister, qubes monitoringQubeLister) *MonitoringHandler {
	return NewMonitoringHandler(measuredHost(), nil).WithAlertSources(zones, qubes)
}

func TestMonitoringAlertsUsePersistedHealthAndIgnoreNonAlerts(t *testing.T) {
	observedAt := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	probedAt := observedAt.Add(-time.Minute)
	h := newAlertHandler(
		monitoringZonesStub{zones: []*models.Zone{
			{ID: "z-down", Name: "lab-down", Status: models.ZoneStatusDisconnected, UpdatedAt: observedAt},
			{ID: "z-up", Name: "lab-up", Status: models.ZoneStatusConnected, UpdatedAt: observedAt},
		}},
		monitoringQubesStub{qubes: []*models.Qube{
			{ID: "q-bad", Name: "broken", AgentHealth: models.AgentHealthUnreachable, AgentLastProbedAt: &probedAt},
			{ID: "q-stopped", Name: "stopped", Status: models.QubeStatusStopped, AgentHealth: models.AgentHealthUnreachable},
			{ID: "q-good", Name: "good", AgentHealth: models.AgentHealthHealthy},
			{ID: "q-unknown", Name: "new", AgentHealth: models.AgentHealthUnknown},
		}},
	)

	alerts, implemented, err := h.alerts(context.Background())
	require.NoError(t, err)
	require.True(t, implemented)
	require.Len(t, alerts, 2)
	require.Equal(t, "zone-disconnected:z-down", alerts[0].ID)
	require.Equal(t, "zone-status", alerts[0].Source)
	require.Equal(t, "warning", alerts[0].Severity)
	require.Equal(t, observedAt, *alerts[0].Timestamp)
	require.Equal(t, "agent-unreachable:q-bad", alerts[1].ID)
	require.Equal(t, "agent-health", alerts[1].Source)
	require.Equal(t, "critical", alerts[1].Severity)
	require.Equal(t, probedAt, *alerts[1].Timestamp)
}

func TestMonitoringFailsClosedWhenHealthSourceFails(t *testing.T) {
	const internal = "database unavailable at /var/lib/qubes-air/console.db"
	h := newAlertHandler(monitoringZonesStub{err: errors.New(internal)}, monitoringQubesStub{})
	var logged []string
	h.logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	for _, path := range []string{"/api/monitoring", "/api/monitoring/alerts"} {
		recorder, body := serveMonitoring(t, h, http.MethodGet, path)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, path)
		assert.Equal(t, "monitoring health data is unavailable", body["message"])
		assert.NotContains(t, recorder.Body.String(), "/var/lib/qubes-air")
	}
	require.Len(t, logged, 2)
	assert.Contains(t, logged[0], internal)
}

func TestMonitoringOverviewIncludesAlertsWithoutImplementationMarker(t *testing.T) {
	h := newAlertHandler(
		monitoringZonesStub{zones: []*models.Zone{{ID: "z1", Name: "lab", Status: models.ZoneStatusDisconnected}}},
		monitoringQubesStub{},
	)
	recorder, body := serveMonitoring(t, h, http.MethodGet, "/api/monitoring")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, body["alerts"], 1)
	assert.NotContains(t, body, "alerts_status", "implemented alerting must not say it is unimplemented")
}

func TestAcknowledgeAlertPersistsOnlyAnActiveAlert(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &alertAcknowledgementsStub{records: make(map[string]repository.AlertAcknowledgement)}
	h := newAlertHandler(
		monitoringZonesStub{zones: []*models.Zone{{ID: "z1", Name: "lab", Status: models.ZoneStatusDisconnected}}},
		monitoringQubesStub{},
	).WithAlertAcknowledgements(store)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.SubjectContextKey, "operator-a")
		c.Next()
	})
	h.RegisterRoutes(router.Group("/api"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/monitoring/alerts/zone-disconnected%3Az1/acknowledge", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"acknowledged":true`)
	require.Equal(t, "operator-a", store.records["zone-disconnected:z1"].AcknowledgedBy)

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/monitoring/alerts/missing/acknowledge", nil))
	require.Equal(t, http.StatusBadRequest, recorder.Code)

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/monitoring/alerts/zone-disconnected%3Az2/acknowledge", nil))
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestAcknowledgeAlertRequiresAnAuthenticatedSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &alertAcknowledgementsStub{records: make(map[string]repository.AlertAcknowledgement)}
	h := newAlertHandler(
		monitoringZonesStub{zones: []*models.Zone{{ID: "z1", Name: "lab", Status: models.ZoneStatusDisconnected}}},
		monitoringQubesStub{},
	).WithAlertAcknowledgements(store)
	router := gin.New()
	h.RegisterRoutes(router.Group("/api"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/monitoring/alerts/zone-disconnected%3Az1/acknowledge", nil))
	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	require.Empty(t, store.records, "an unauthenticated request must not record an acknowledgement")
}

func TestGetAlertsIncludesAcknowledgementAndExpiresResolvedAlert(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &alertAcknowledgementsStub{records: map[string]repository.AlertAcknowledgement{
		"zone-disconnected:z1": {
			AlertID: "zone-disconnected:z1", AcknowledgedBy: "operator-a",
			AcknowledgedAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
		},
		"agent-unreachable:q1": {AlertID: "agent-unreachable:q1", AcknowledgedBy: "operator-b"},
	}}
	h := newAlertHandler(
		monitoringZonesStub{zones: []*models.Zone{{ID: "z1", Name: "lab", Status: models.ZoneStatusDisconnected}}},
		monitoringQubesStub{},
	).WithAlertAcknowledgements(store)
	router := gin.New()
	h.RegisterRoutes(router.Group("/api"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/monitoring/alerts", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"acknowledged_by":"operator-a"`)
	require.NotContains(t, recorder.Body.String(), `"acknowledged_by":"operator-b"`)
	require.NotContains(t, store.records, "agent-unreachable:q1")
}

func TestGetAlertHistoryReturnsBoundedPersistedTransitions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	events := make([]repository.MonitoringAlertEvent, 5)
	events[4] = repository.MonitoringAlertEvent{
		ID: 7, AlertID: "zone-disconnected:z1", ResourceID: "z1", EventType: "resolved",
		Severity: "warning", Message: `Zone "lab" is disconnected`, Source: "zone-status",
		OccurredAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
	}
	store := monitoringAlertEventsStub{events: events, total: 12}
	h := newAlertHandler(monitoringZonesStub{}, monitoringQubesStub{}).WithAlertEvents(store)
	router := gin.New()
	h.RegisterRoutes(router.Group("/api"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/monitoring/alerts/history?limit=1&offset=4", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"event_type":"resolved"`)
	require.Contains(t, recorder.Body.String(), `"total":12`)
	require.Contains(t, recorder.Body.String(), `"offset":4`)
}

func TestGetAlertHistoryRejectsInvalidPaginationAndStorageFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newAlertHandler(monitoringZonesStub{}, monitoringQubesStub{}).WithAlertEvents(monitoringAlertEventsStub{
		err: errors.New("database unavailable"),
	})
	router := gin.New()
	h.RegisterRoutes(router.Group("/api"))
	for _, path := range []string{
		"/api/monitoring/alerts/history?limit=0",
		"/api/monitoring/alerts/history?limit=101",
		"/api/monitoring/alerts/history?offset=-1",
		"/api/monitoring/alerts/history?offset=not-a-number",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusBadRequest, recorder.Code, path)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/monitoring/alerts/history", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestMonitoringAlertsReadAllPages(t *testing.T) {
	zones := make([]*models.Zone, monitoringPageSize+1)
	for i := range zones {
		zones[i] = &models.Zone{ID: "z", Name: "zone", Status: models.ZoneStatusDisconnected}
	}
	all, err := listMonitoringZones(context.Background(), monitoringZonesStub{zones: zones})
	require.NoError(t, err)
	require.Len(t, all, len(zones))
}
