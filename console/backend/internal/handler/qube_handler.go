// Package handler provides HTTP handlers for qube management.
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
)

// QubeHandler handles qube-related HTTP requests.
type QubeHandler struct {
	qubeSvc service.QubeService
}

// NewQubeHandler creates a new QubeHandler.
func NewQubeHandler(qubeSvc service.QubeService) *QubeHandler {
	return &QubeHandler{qubeSvc: qubeSvc}
}

// RegisterRoutes registers qube routes on the router group.
func (h *QubeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	qubes := rg.Group("/qubes")
	{
		qubes.GET("", h.List)
		qubes.GET("/:id", h.GetByID)
		qubes.POST("", h.Create)
		qubes.PUT("/:id", h.Update)
		qubes.DELETE("/:id", h.Delete)
		qubes.POST("/:id/start", h.Start)
		qubes.POST("/:id/stop", h.Stop)
	}
}

// List handles GET /qubes.
func (h *QubeHandler) List(c *gin.Context) {
	opts := parseQubeListOptions(c)

	qubes, err := h.qubeSvc.List(c.Request.Context(), opts)
	if err != nil {
		respondError(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"qubes": qubes,
		"total": len(qubes),
	})
}

// parseQubeListOptions extracts list options from query parameters.
func parseQubeListOptions(c *gin.Context) repository.QubeListOptions {
	opts := repository.DefaultQubeListOptions()

	if zoneID := c.Query("zone_id"); zoneID != "" {
		opts.ZoneID = zoneID
	}
	if status := c.Query("status"); status != "" {
		opts.Status = status
	}
	if qubeType := c.Query("type"); qubeType != "" {
		opts.Type = qubeType
	}

	return opts
}

// GetByID handles GET /qubes/:id.
func (h *QubeHandler) GetByID(c *gin.Context) {
	id := c.Param("id")

	qube, err := h.qubeSvc.GetByID(c.Request.Context(), id)
	if err != nil {
		handleQubeError(c, err)
		return
	}

	c.JSON(http.StatusOK, qube)
}

// Create handles POST /qubes.
func (h *QubeHandler) Create(c *gin.Context) {
	var req models.QubeCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, err)
		return
	}

	qube, err := h.qubeSvc.Create(c.Request.Context(), &req)
	if err != nil {
		handleQubeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, qube)
}

// Update handles PUT /qubes/:id.
func (h *QubeHandler) Update(c *gin.Context) {
	id := c.Param("id")

	var req models.QubeUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, err)
		return
	}

	qube, err := h.qubeSvc.Update(c.Request.Context(), id, &req)
	if err != nil {
		handleQubeError(c, err)
		return
	}

	c.JSON(http.StatusOK, qube)
}

// Delete handles DELETE /qubes/:id.
func (h *QubeHandler) Delete(c *gin.Context) {
	id := c.Param("id")

	if err := h.qubeSvc.Delete(c.Request.Context(), id); err != nil {
		handleQubeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "qube deleted"})
}

// Start handles POST /qubes/:id/start.
func (h *QubeHandler) Start(c *gin.Context) {
	id := c.Param("id")

	qube, err := h.qubeSvc.Start(c.Request.Context(), id)
	if err != nil {
		handleQubeError(c, err)
		return
	}

	c.JSON(http.StatusOK, qube)
}

// Stop handles POST /qubes/:id/stop.
func (h *QubeHandler) Stop(c *gin.Context) {
	id := c.Param("id")

	qube, err := h.qubeSvc.Stop(c.Request.Context(), id)
	if err != nil {
		handleQubeError(c, err)
		return
	}

	c.JSON(http.StatusOK, qube)
}

// handleQubeError maps service errors to HTTP responses.
func handleQubeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrQubeNotFound):
		respondError(c, http.StatusNotFound, err)
	case errors.Is(err, service.ErrZoneNotFound):
		respondError(c, http.StatusBadRequest, err)
	case errors.Is(err, service.ErrQubeNotStopped):
		respondError(c, http.StatusConflict, err)
	case errors.Is(err, service.ErrZoneDisconnected):
		respondError(c, http.StatusPreconditionFailed, err)
	case errors.Is(err, service.ErrInvalidQubeType):
		respondError(c, http.StatusBadRequest, err)
	default:
		respondError(c, http.StatusInternalServerError, err)
	}
}
