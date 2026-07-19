// Package handler provides HTTP handlers for zone management.
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
)

// ZoneHandler handles zone-related HTTP requests.
type ZoneHandler struct {
	zoneSvc service.ZoneService
	// capacity reads live cluster capacity. Nil when no scheduler is
	// configured, in which case the nodes endpoint reports 501 and the UI falls
	// back to a free-text node field.
	capacity service.CapacityReader
}

// ZoneHandlerOption customises a ZoneHandler.
type ZoneHandlerOption func(*ZoneHandler)

// WithCapacityReader enables the cluster capacity endpoint.
func WithCapacityReader(r service.CapacityReader) ZoneHandlerOption {
	return func(h *ZoneHandler) { h.capacity = r }
}

// NewZoneHandler creates a new ZoneHandler.
func NewZoneHandler(zoneSvc service.ZoneService, opts ...ZoneHandlerOption) *ZoneHandler {
	h := &ZoneHandler{zoneSvc: zoneSvc}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// RegisterRoutes registers zone routes on the router group.
func (h *ZoneHandler) RegisterRoutes(rg *gin.RouterGroup) {
	zones := rg.Group("/zones")
	{
		zones.GET("", h.List)
		zones.GET("/:id", h.GetByID)
		zones.POST("", h.Create)
		zones.PUT("/:id", h.Update)
		zones.DELETE("/:id", h.Delete)
		zones.POST("/:id/connect", h.Connect)
		zones.POST("/:id/disconnect", h.Disconnect)
		zones.GET("/:id/capacity", h.Capacity)
	}
}

// List handles GET /zones.
func (h *ZoneHandler) List(c *gin.Context) {
	opts := parseZoneListOptions(c)

	zones, err := h.zoneSvc.List(c.Request.Context(), opts)
	if err != nil {
		respondError(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"zones": zones,
		"total": len(zones),
	})
}

// parseZoneListOptions extracts list options from query parameters.
func parseZoneListOptions(c *gin.Context) repository.ZoneListOptions {
	opts := repository.DefaultZoneListOptions()

	if status := c.Query("status"); status != "" {
		opts.Status = status
	}
	if zoneType := c.Query("type"); zoneType != "" {
		opts.Type = zoneType
	}

	return opts
}

// GetByID handles GET /zones/:id.
func (h *ZoneHandler) GetByID(c *gin.Context) {
	id := c.Param("id")

	zone, err := h.zoneSvc.GetByID(c.Request.Context(), id)
	if err != nil {
		handleZoneError(c, err)
		return
	}

	c.JSON(http.StatusOK, zone)
}

// Create handles POST /zones.
func (h *ZoneHandler) Create(c *gin.Context) {
	var req models.ZoneCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, err)
		return
	}

	zone, err := h.zoneSvc.Create(c.Request.Context(), &req)
	if err != nil {
		handleZoneError(c, err)
		return
	}

	c.JSON(http.StatusCreated, zone)
}

// Update handles PUT /zones/:id.
func (h *ZoneHandler) Update(c *gin.Context) {
	id := c.Param("id")

	var req models.ZoneUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, err)
		return
	}

	zone, err := h.zoneSvc.Update(c.Request.Context(), id, &req)
	if err != nil {
		handleZoneError(c, err)
		return
	}

	c.JSON(http.StatusOK, zone)
}

// Delete handles DELETE /zones/:id.
func (h *ZoneHandler) Delete(c *gin.Context) {
	id := c.Param("id")

	if err := h.zoneSvc.Delete(c.Request.Context(), id); err != nil {
		handleZoneError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "zone deleted"})
}

// Capacity reports a zone's headroom in whichever form its provider uses.
//
// The response carries a "kind" the UI branches on: a node pool exposes
// per-node free memory and a node picker; an elastic provider exposes usage
// against quota, and node selection is not offered at all because the cloud
// chooses the machine.
func (h *ZoneHandler) Capacity(c *gin.Context) {
	if h.capacity == nil {
		respondError(c, http.StatusNotImplemented,
			errors.New("capacity reporting is unavailable: no scheduler configured"))
		return
	}
	capacity, err := h.capacity.Capacity(c.Request.Context(), c.Param("id"))
	if err != nil {
		// A zone without credentials or an unreachable cluster is an expected
		// state, not a server fault — the UI degrades rather than erroring.
		respondError(c, http.StatusServiceUnavailable, err)
		return
	}
	c.JSON(http.StatusOK, capacity)
}

// Connect handles POST /zones/:id/connect.
func (h *ZoneHandler) Connect(c *gin.Context) {
	id := c.Param("id")

	zone, err := h.zoneSvc.Connect(c.Request.Context(), id)
	if err != nil {
		handleZoneError(c, err)
		return
	}

	c.JSON(http.StatusOK, zone)
}

// Disconnect handles POST /zones/:id/disconnect.
func (h *ZoneHandler) Disconnect(c *gin.Context) {
	id := c.Param("id")

	zone, err := h.zoneSvc.Disconnect(c.Request.Context(), id)
	if err != nil {
		handleZoneError(c, err)
		return
	}

	c.JSON(http.StatusOK, zone)
}

// handleZoneError maps service errors to HTTP responses.
func handleZoneError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrZoneNotFound):
		respondError(c, http.StatusNotFound, err)
	case errors.Is(err, service.ErrZoneInUse):
		respondError(c, http.StatusConflict, err)
	case errors.Is(err, service.ErrInvalidZoneType):
		respondError(c, http.StatusBadRequest, err)
	default:
		respondError(c, http.StatusInternalServerError, err)
	}
}
