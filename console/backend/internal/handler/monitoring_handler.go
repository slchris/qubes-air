package handler

import (
	"context"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
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

// MonitoringHandler handles monitoring-related HTTP requests.
//
// Every /monitoring route aggregates across zones, so a zone-restricted
// credential is refused before it gets here (middleware.RequireZones).
type MonitoringHandler struct {
	host  hostMetricsSampler
	qubes qubeMetricsCollector
	logf  func(format string, args ...any)
}

// NewMonitoringHandler creates a MonitoringHandler. host must be non-nil;
// qubes may be nil, in which case GET /monitoring/qubes answers 501.
func NewMonitoringHandler(host hostMetricsSampler, qubes qubeMetricsCollector) *MonitoringHandler {
	return &MonitoringHandler{host: host, qubes: qubes, logf: log.Printf}
}

// RegisterRoutes registers monitoring routes.
func (h *MonitoringHandler) RegisterRoutes(rg *gin.RouterGroup) {
	monitoring := rg.Group("/monitoring")
	monitoring.GET("", h.GetOverview)
	monitoring.GET("/metrics", h.GetMetrics)
	monitoring.GET("/qubes", h.GetQubeMetrics)
	monitoring.GET("/alerts", h.GetAlerts)
	monitoring.POST("/alerts/:id/acknowledge", h.AcknowledgeAlert)
}

// hostMetricsNote states the scope of the host metrics next to the numbers.
const hostMetricsNote = "Host-wide Console metrics; these values do not describe managed qubes. " +
	"Network rates sum non-loopback interfaces and can count bridged traffic more than once."

// alertsNotImplemented is reported with every alert list until alerting is
// wired to real health data: an empty list must not read as "all healthy".
const alertsNotImplemented = "not_implemented"

// qubeMetricsUnavailable is the only failure text a caller sees; the cause is
// logged, because it can name internal endpoints or database paths.
const qubeMetricsUnavailable = "provider runtime metrics are unavailable"

// Alert represents a monitoring alert.
type Alert struct {
	ID           string `json:"id"`
	Severity     string `json:"severity"`
	Message      string `json:"message"`
	Source       string `json:"source"`
	Timestamp    string `json:"timestamp"`
	Acknowledged bool   `json:"acknowledged"`
}

// GetOverview returns the Console host metrics and the alert list.
func (h *MonitoringHandler) GetOverview(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"metrics":       h.host.Collect(),
		"note":          hostMetricsNote,
		"alerts":        []Alert{},
		"alerts_status": alertsNotImplemented,
	})
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

// GetAlerts returns all alerts. Alerting is not implemented yet, which the
// response says explicitly.
func (h *MonitoringHandler) GetAlerts(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"alerts":        []Alert{},
		"total":         0,
		"alerts_status": alertsNotImplemented,
	})
}

// AcknowledgeAlert is a stub until alert persistence lands (F2). There are no
// alerts to acknowledge and nowhere to record who did, so it answers 501
// rather than reporting an acknowledgement that did not happen.
func (h *MonitoringHandler) AcknowledgeAlert(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, ErrorResponse{
		Error:   http.StatusText(http.StatusNotImplemented),
		Message: "alert acknowledgement is not implemented",
		Code:    http.StatusNotImplemented,
	})
}
