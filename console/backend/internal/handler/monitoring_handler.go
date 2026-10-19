package handler

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/slchris/qubes-air/console/internal/systemmetrics"
)

// hostMetricsSampler samples the host the Console runs on.
type hostMetricsSampler interface {
	Collect() systemmetrics.Metrics
}

// qubeMetricsCollector reads live per-qube metrics from the providers.
type qubeMetricsCollector interface {
	Collect(context.Context) ([]service.QubeRuntimeMetrics, error)
}

type monitoringZoneLister interface {
	List(context.Context, repository.ZoneListOptions) ([]*models.Zone, error)
}

type monitoringQubeLister interface {
	List(context.Context, repository.QubeListOptions) ([]*models.Qube, error)
}

type monitoringAlertAcknowledgements interface {
	SyncActive(context.Context, []string) (map[string]repository.AlertAcknowledgement, error)
	Acknowledge(context.Context, string, string, time.Time) error
}

type monitoringAlertEventLister interface {
	List(context.Context, int, int) ([]repository.MonitoringAlertEvent, int, error)
}

const monitoringPageSize = 200

// MonitoringHandler handles monitoring-related HTTP requests.
//
// Every /monitoring route aggregates across zones, so a zone-restricted
// credential is refused before it gets here (middleware.RequireZones).
type MonitoringHandler struct {
	host             hostMetricsSampler
	qubes            qubeMetricsCollector
	zones            monitoringZoneLister
	qubeLister       monitoringQubeLister
	acknowledgements monitoringAlertAcknowledgements
	alertEvents      monitoringAlertEventLister
	logf             func(format string, args ...any)
}

// NewMonitoringHandler creates a MonitoringHandler. host must be non-nil;
// qubes may be nil, in which case GET /monitoring/qubes answers 501.
func NewMonitoringHandler(host hostMetricsSampler, qubes qubeMetricsCollector) *MonitoringHandler {
	return &MonitoringHandler{host: host, qubes: qubes, logf: log.Printf}
}

// WithAlertSources enables alerts derived from persisted zone and qube health.
// Without them the alert endpoints keep saying alerting is not implemented.
func (h *MonitoringHandler) WithAlertSources(zones monitoringZoneLister, qubes monitoringQubeLister) *MonitoringHandler {
	h.zones = zones
	h.qubeLister = qubes
	return h
}

// WithAlertAcknowledgements enables persistent acknowledgement state.
func (h *MonitoringHandler) WithAlertAcknowledgements(store monitoringAlertAcknowledgements) *MonitoringHandler {
	h.acknowledgements = store
	return h
}

// WithAlertEvents enables the durable alert transition history endpoint.
func (h *MonitoringHandler) WithAlertEvents(store monitoringAlertEventLister) *MonitoringHandler {
	h.alertEvents = store
	return h
}

// RegisterRoutes registers monitoring routes.
func (h *MonitoringHandler) RegisterRoutes(rg *gin.RouterGroup) {
	monitoring := rg.Group("/monitoring")
	monitoring.GET("", h.GetOverview)
	monitoring.GET("/metrics", h.GetMetrics)
	monitoring.GET("/qubes", h.GetQubeMetrics)
	monitoring.GET("/alerts", h.GetAlerts)
	monitoring.GET("/alerts/history", h.GetAlertHistory)
	monitoring.POST("/alerts/:id/acknowledge", h.AcknowledgeAlert)
}

// hostMetricsNote states the scope of the host metrics next to the numbers.
const hostMetricsNote = "Host-wide Console metrics; these values do not describe managed qubes. " +
	"Network rates sum non-loopback interfaces and can count bridged traffic more than once."

// alertsNotImplemented is reported when no alert source is configured: an empty
// list must not read as "all healthy".
const alertsNotImplemented = "not_implemented"

// qubeMetricsUnavailable is the only failure text a caller sees; the cause is
// logged, because it can name internal endpoints or database paths.
const qubeMetricsUnavailable = "provider runtime metrics are unavailable"

// Alert represents a monitoring alert.
type Alert struct {
	ID             string     `json:"id"`
	ResourceID     string     `json:"resourceId,omitempty"`
	Severity       string     `json:"severity"`
	Message        string     `json:"message"`
	Source         string     `json:"source"`
	Timestamp      *time.Time `json:"timestamp,omitempty"`
	Acknowledged   bool       `json:"acknowledged"`
	AcknowledgedBy string     `json:"acknowledged_by,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

// GetOverview returns the Console host metrics and the alert list.
func (h *MonitoringHandler) GetOverview(c *gin.Context) {
	body := gin.H{
		"metrics": h.host.Collect(),
		"note":    hostMetricsNote,
	}
	alerts, implemented, err := h.alerts(c.Request.Context())
	if err != nil {
		h.logf("monitoring: GET /monitoring: %v", err)
		c.JSON(http.StatusServiceUnavailable, ErrorResponse{
			Error:   http.StatusText(http.StatusServiceUnavailable),
			Message: "monitoring health data is unavailable",
			Code:    http.StatusServiceUnavailable,
		})
		return
	}
	body["alerts"] = alerts
	if !implemented {
		body["alerts_status"] = alertsNotImplemented
	}
	c.JSON(http.StatusOK, body)
}

// GetMetrics returns the Console host metrics.
func (h *MonitoringHandler) GetMetrics(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"metrics": h.host.Collect(),
		"note":    hostMetricsNote,
	})
}

// GetQubeMetrics returns one entry per qube: live values read from its
// provider, or a reason code saying why there are none. The collector bounds
// the whole read (service.DefaultRuntimeMetricsSweepDeadline) inside the
// server's write timeout.
func (h *MonitoringHandler) GetQubeMetrics(c *gin.Context) {
	if h.qubes == nil {
		c.JSON(http.StatusNotImplemented, ErrorResponse{
			Error:   http.StatusText(http.StatusNotImplemented),
			Message: "provider runtime metrics are not configured",
			Code:    http.StatusNotImplemented,
		})
		return
	}
	items, err := h.qubes.Collect(c.Request.Context())
	if err != nil {
		h.logf("monitoring: GET /monitoring/qubes: %v", err)
		c.JSON(http.StatusServiceUnavailable, ErrorResponse{
			Error:   http.StatusText(http.StatusServiceUnavailable),
			Message: qubeMetricsUnavailable,
			Code:    http.StatusServiceUnavailable,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// GetAlerts returns all active alerts derived from persisted health state.
func (h *MonitoringHandler) GetAlerts(c *gin.Context) {
	alerts, implemented, err := h.alerts(c.Request.Context())
	if err != nil {
		h.logf("monitoring: GET /monitoring/alerts: %v", err)
		c.JSON(http.StatusServiceUnavailable, ErrorResponse{
			Error:   http.StatusText(http.StatusServiceUnavailable),
			Message: "monitoring health data is unavailable",
			Code:    http.StatusServiceUnavailable,
		})
		return
	}
	body := gin.H{"alerts": alerts, "total": len(alerts)}
	if !implemented {
		body["alerts_status"] = alertsNotImplemented
	}
	c.JSON(http.StatusOK, body)
}

// GetAlertHistory returns persisted alert transitions, newest first.
func (h *MonitoringHandler) GetAlertHistory(c *gin.Context) {
	if h.alertEvents == nil {
		c.JSON(http.StatusServiceUnavailable, ErrorResponse{
			Error:   http.StatusText(http.StatusServiceUnavailable),
			Message: "alert history storage is unavailable",
			Code:    http.StatusServiceUnavailable,
		})
		return
	}
	limit, err := parseHistoryQuery(c.Query("limit"), 20, 1, 100)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   http.StatusText(http.StatusBadRequest),
			Message: "limit must be between 1 and 100",
			Code:    http.StatusBadRequest,
		})
		return
	}
	offset, err := parseHistoryQuery(c.Query("offset"), 0, 0, 10000)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   http.StatusText(http.StatusBadRequest),
			Message: "offset must be between 0 and 10000",
			Code:    http.StatusBadRequest,
		})
		return
	}
	events, total, err := h.alertEvents.List(c.Request.Context(), limit, offset)
	if err != nil {
		h.logf("monitoring: GET /monitoring/alerts/history: %v", err)
		c.JSON(http.StatusServiceUnavailable, ErrorResponse{
			Error:   http.StatusText(http.StatusServiceUnavailable),
			Message: "alert history is unavailable",
			Code:    http.StatusServiceUnavailable,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events, "total": total, "limit": limit, "offset": offset})
}

func parseHistoryQuery(raw string, defaultValue, minimum, maximum int) (int, error) {
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("value out of range")
	}
	return value, nil
}

// AcknowledgeAlert records the operator against an alert that is active now.
// It refuses an ID that is not in the current snapshot: an acknowledgement
// for an alert that has already resolved would be cleared on the next read.
func (h *MonitoringHandler) AcknowledgeAlert(c *gin.Context) {
	if h.acknowledgements == nil {
		c.JSON(http.StatusNotImplemented, ErrorResponse{
			Error:   http.StatusText(http.StatusNotImplemented),
			Message: "alert acknowledgement is not implemented",
			Code:    http.StatusNotImplemented,
		})
		return
	}
	alertID := c.Param("id")
	if len(alertID) > 512 || !strings.Contains(alertID, ":") {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   http.StatusText(http.StatusBadRequest),
			Message: "invalid alert id",
			Code:    http.StatusBadRequest,
		})
		return
	}
	active, _, err := h.alerts(c.Request.Context())
	if err != nil {
		h.logf("monitoring: POST /monitoring/alerts/%s/acknowledge: %v", alertID, err)
		c.JSON(http.StatusServiceUnavailable, ErrorResponse{
			Error:   http.StatusText(http.StatusServiceUnavailable),
			Message: "active alerts are unavailable",
			Code:    http.StatusServiceUnavailable,
		})
		return
	}
	found := false
	for _, alert := range active {
		if alert.ID == alertID {
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error:   http.StatusText(http.StatusNotFound),
			Message: "active alert not found",
			Code:    http.StatusNotFound,
		})
		return
	}
	subject, ok := middleware.SubjectFromContext(c)
	if !ok {
		subject = "anonymous"
	}
	acknowledgedAt := time.Now().UTC()
	if err := h.acknowledgements.Acknowledge(c.Request.Context(), alertID, subject, acknowledgedAt); err != nil {
		h.logf("monitoring: acknowledge alert %s: %v", alertID, err)
		c.JSON(http.StatusServiceUnavailable, ErrorResponse{
			Error:   http.StatusText(http.StatusServiceUnavailable),
			Message: "could not persist alert acknowledgement",
			Code:    http.StatusServiceUnavailable,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"alert_id": alertID, "acknowledged": true,
		"acknowledged_by": subject, "acknowledged_at": acknowledgedAt,
	})
}

// alerts builds the active alert list from the persisted zone and qube health
// observations. implemented is false when no health source is configured.
func (h *MonitoringHandler) alerts(ctx context.Context) ([]Alert, bool, error) {
	if h.zones == nil || h.qubeLister == nil {
		return []Alert{}, false, nil
	}
	zones, err := listMonitoringZones(ctx, h.zones)
	if err != nil {
		return nil, true, fmt.Errorf("load zone health for monitoring: %w", err)
	}
	qubes, err := listMonitoringQubes(ctx, h.qubeLister)
	if err != nil {
		return nil, true, fmt.Errorf("load agent health for monitoring: %w", err)
	}
	alerts := make([]Alert, 0)
	for _, zone := range zones {
		if zone.Status != models.ZoneStatusDisconnected {
			continue
		}
		observedAt := zone.UpdatedAt
		alerts = append(alerts, Alert{
			ID: "zone-disconnected:" + zone.ID, ResourceID: zone.ID,
			Severity: "warning", Message: fmt.Sprintf("Zone %q is disconnected", zone.Name),
			Source: "zone-status", Timestamp: &observedAt,
		})
	}
	for _, qube := range qubes {
		if qube.AgentHealth != models.AgentHealthUnreachable || !agentAlertApplies(qube.Status) {
			continue
		}
		alerts = append(alerts, Alert{
			ID: "agent-unreachable:" + qube.ID, ResourceID: qube.ID,
			Severity: "critical", Message: fmt.Sprintf("Agent for qube %q is unreachable", qube.Name),
			Source: "agent-health", Timestamp: qube.AgentLastProbedAt,
		})
	}
	if h.acknowledgements != nil {
		activeIDs := make([]string, len(alerts))
		for index := range alerts {
			activeIDs[index] = alerts[index].ID
		}
		stored, err := h.acknowledgements.SyncActive(ctx, activeIDs)
		if err != nil {
			return nil, true, fmt.Errorf("load active alert acknowledgements: %w", err)
		}
		for index := range alerts {
			if record, ok := stored[alerts[index].ID]; ok {
				alerts[index].Acknowledged = true
				alerts[index].AcknowledgedBy = record.AcknowledgedBy
				alerts[index].AcknowledgedAt = &record.AcknowledgedAt
			}
		}
	}
	return alerts, true, nil
}

func agentAlertApplies(status models.QubeStatus) bool {
	switch status {
	case models.QubeStatusStopped, models.QubeStatusSuspended, models.QubeStatusReleased, models.QubeStatusPurged:
		return false
	default:
		return true
	}
}

func listMonitoringZones(ctx context.Context, lister monitoringZoneLister) ([]*models.Zone, error) {
	var all []*models.Zone
	for offset := 0; ; offset += monitoringPageSize {
		page, err := lister.List(ctx, repository.ZoneListOptions{Limit: monitoringPageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < monitoringPageSize {
			return all, nil
		}
	}
}

func listMonitoringQubes(ctx context.Context, lister monitoringQubeLister) ([]*models.Qube, error) {
	var all []*models.Qube
	for offset := 0; ; offset += monitoringPageSize {
		page, err := lister.List(ctx, repository.QubeListOptions{Limit: monitoringPageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < monitoringPageSize {
			return all, nil
		}
	}
}
