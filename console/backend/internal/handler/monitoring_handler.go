package handler

import (
	"net/http"
	"runtime"

	"github.com/gin-gonic/gin"
)

// MonitoringHandler handles monitoring-related HTTP requests.
type MonitoringHandler struct{}

// NewMonitoringHandler creates a new MonitoringHandler.
func NewMonitoringHandler() *MonitoringHandler {
	return &MonitoringHandler{}
}

// RegisterRoutes registers monitoring routes.
func (h *MonitoringHandler) RegisterRoutes(rg *gin.RouterGroup) {
	monitoring := rg.Group("/monitoring")
	monitoring.GET("", h.GetOverview)
	monitoring.GET("/metrics", h.GetMetrics)
	monitoring.GET("/alerts", h.GetAlerts)
	monitoring.POST("/alerts/:id/acknowledge", h.AcknowledgeAlert)
}

// SystemMetrics represents metrics for the console process.
//
// IMPORTANT (honesty): MemoryUsage reflects the CONSOLE's own Go runtime, not
// the managed qubes/zones. CPU/disk/network are not collected yet. The Source
// field makes this explicit so a caller never mistakes these for fleet-wide
// infrastructure metrics.
type SystemMetrics struct {
	CPUUsage    float64 `json:"cpuUsage"`
	MemoryUsage float64 `json:"memoryUsage"`
	DiskUsage   float64 `json:"diskUsage"`
	NetworkIn   int64   `json:"networkIn"`
	NetworkOut  int64   `json:"networkOut"`
	// Source identifies where these numbers come from. Currently
	// "console-process" — i.e. the backend's own runtime, not the fleet.
	Source string `json:"source"`
}

// consoleProcessMetrics builds SystemMetrics from the console's own runtime.
// Only memory is real; the rest require external collection (per-zone agents or
// syscalls) and are left at zero with an explicit Source.
func consoleProcessMetrics() SystemMetrics {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	memUsage := float64(m.Alloc) / float64(m.Sys) * 100.0
	return SystemMetrics{
		CPUUsage:    0, // TODO: needs a CPU sampler; not the fleet's CPU
		MemoryUsage: memUsage,
		DiskUsage:   0, // TODO: needs syscall/statfs
		NetworkIn:   0,
		NetworkOut:  0,
		Source:      "console-process",
	}
}

// monitoringNote flags that overview metrics describe the console process, not
// the managed fleet.
const monitoringNote = "PLACEHOLDER: metrics describe the console process (source=console-process), " +
	"not the managed qubes/zones. Fleet metrics require per-zone collection."

// Alert represents a monitoring alert.
type Alert struct {
	ID           string `json:"id"`
	Severity     string `json:"severity"`
	Message      string `json:"message"`
	Source       string `json:"source"`
	Timestamp    string `json:"timestamp"`
	Acknowledged bool   `json:"acknowledged"`
}

// GetOverview returns monitoring overview.
func (h *MonitoringHandler) GetOverview(c *gin.Context) {
	metrics := consoleProcessMetrics()
	alerts := []Alert{}

	c.JSON(http.StatusOK, gin.H{
		"metrics":     metrics,
		"alerts":      alerts,
		"placeholder": true,
		"note":        monitoringNote,
	})
}

// GetMetrics returns detailed metrics.
func (h *MonitoringHandler) GetMetrics(c *gin.Context) {
	metrics := consoleProcessMetrics()

	c.JSON(http.StatusOK, gin.H{
		"metrics":     metrics,
		"placeholder": true,
		"note":        monitoringNote,
	})
}

// GetAlerts returns all alerts.
func (h *MonitoringHandler) GetAlerts(c *gin.Context) {
	alerts := []Alert{}

	c.JSON(http.StatusOK, gin.H{
		"alerts": alerts,
		"total":  0,
	})
}

// AcknowledgeAlert acknowledges an alert.
func (h *MonitoringHandler) AcknowledgeAlert(c *gin.Context) {
	id := c.Param("id")
	c.JSON(http.StatusOK, gin.H{
		"id":           id,
		"acknowledged": true,
	})
}
