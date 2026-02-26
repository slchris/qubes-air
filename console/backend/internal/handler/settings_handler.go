package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/service"
)

// SettingsHandler handles settings-related HTTP requests.
type SettingsHandler struct {
	svc *service.SettingsService
}

// NewSettingsHandler creates a new SettingsHandler.
func NewSettingsHandler(svc *service.SettingsService) *SettingsHandler {
	return &SettingsHandler{svc: svc}
}

// RegisterRoutes registers settings routes.
func (h *SettingsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	settings := rg.Group("/settings")
	settings.GET("", h.Get)
	settings.PUT("", h.Update)
}

// Get returns current settings.
func (h *SettingsHandler) Get(c *gin.Context) {
	settings, err := h.svc.Get(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"settings": settings})
}

// Update updates settings.
func (h *SettingsHandler) Update(c *gin.Context) {
	var settings models.Settings
	if err := c.ShouldBindJSON(&settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.Update(c.Request.Context(), &settings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"settings": settings,
		"message":  "Settings saved successfully",
	})
}
