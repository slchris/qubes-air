package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// BillingHandler handles billing-related HTTP requests.
// Note: Billing is typically integrated with external billing systems.
// This is a placeholder implementation for demo purposes.
type BillingHandler struct{}

// NewBillingHandler creates a new BillingHandler.
func NewBillingHandler() *BillingHandler {
	return &BillingHandler{}
}

// RegisterRoutes registers billing routes.
func (h *BillingHandler) RegisterRoutes(rg *gin.RouterGroup) {
	billing := rg.Group("/billing")
	billing.GET("", h.GetSummary)
	billing.GET("/usage", h.GetUsage)
	billing.GET("/invoices", h.ListInvoices)
}

// BillingSummary represents billing summary data.
type BillingSummary struct {
	CurrentMonth   float64 `json:"currentMonth"`
	LastMonth      float64 `json:"lastMonth"`
	ProjectedMonth float64 `json:"projectedMonth"`
	Currency       string  `json:"currency"`
}

// UsageItem represents a usage breakdown item.
type UsageItem struct {
	Service string  `json:"service"`
	Usage   float64 `json:"usage"`
	Unit    string  `json:"unit"`
	Cost    float64 `json:"cost"`
}

// placeholderNote is returned alongside billing data to make it explicit that
// the figures are NOT real. This is deliberate honesty: rather than silently
// returning 0.00 (which reads like "you owe nothing"), callers get a machine-
// and human-readable signal that a real cost source is not wired up yet.
//
// A truthful implementation would estimate cost from each qube's spec (vCPU/
// memory/disk from tfvars) multiplied by its running duration, or read actual
// invoices from the cloud provider's billing API. See TODO below.
const placeholderNote = "PLACEHOLDER: billing is not yet integrated with a cost source. " +
	"Values are not real. Wire up per-qube spec-based estimation or a provider billing API."

// GetSummary returns billing summary.
//
// TODO(billing): replace placeholders with a real estimate. The console already
// knows each qube's spec (vCPU/memory/disk) and its running/suspended status;
// a spec-rate table plus running duration would give an honest estimate without
// depending on an external billing system. For invoiced totals, integrate the
// cloud provider billing API per zone.
func (h *BillingHandler) GetSummary(c *gin.Context) {
	summary := BillingSummary{
		CurrentMonth:   0.00,
		LastMonth:      0.00,
		ProjectedMonth: 0.00,
		Currency:       "USD",
	}

	usage := []UsageItem{}

	c.JSON(http.StatusOK, gin.H{
		"summary":     summary,
		"usage":       usage,
		"placeholder": true,
		"note":        placeholderNote,
	})
}

// GetUsage returns detailed usage breakdown.
func (h *BillingHandler) GetUsage(c *gin.Context) {
	usage := []UsageItem{}

	c.JSON(http.StatusOK, gin.H{
		"usage":       usage,
		"placeholder": true,
		"note":        placeholderNote,
	})
}

// ListInvoices returns list of invoices.
func (h *BillingHandler) ListInvoices(c *gin.Context) {
	invoices := []gin.H{}

	c.JSON(http.StatusOK, gin.H{
		"invoices": invoices,
		"total":    0,
	})
}
