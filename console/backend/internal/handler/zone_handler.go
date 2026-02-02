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
}

// NewZoneHandler creates a new ZoneHandler.
func NewZoneHandler(zoneSvc service.ZoneService) *ZoneHandler {
	return &ZoneHandler{zoneSvc: zoneSvc}
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
