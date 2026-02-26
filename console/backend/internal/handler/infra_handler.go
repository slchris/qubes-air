package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/service"
)

// InfraHandler handles infrastructure-related HTTP requests.
type InfraHandler struct {
	svc *service.InfraService
}

// NewInfraHandler creates a new InfraHandler.
func NewInfraHandler(svc *service.InfraService) *InfraHandler {
	return &InfraHandler{svc: svc}
}

// RegisterRoutes registers infrastructure routes.
func (h *InfraHandler) RegisterRoutes(rg *gin.RouterGroup) {
	infra := rg.Group("/infrastructure")
	infra.GET("", h.List)
	infra.GET("/:id", h.Get)
	infra.POST("", h.Create)
	infra.PUT("/:id", h.Update)
	infra.DELETE("/:id", h.Delete)
	infra.POST("/:id/connect", h.Connect)
	infra.POST("/:id/disconnect", h.Disconnect)
}

// List returns all infrastructure providers.
func (h *InfraHandler) List(c *gin.Context) {
	providers, err := h.svc.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if providers == nil {
		providers = []models.InfraProvider{}
	}

	c.JSON(http.StatusOK, gin.H{
		"providers": providers,
		"total":     len(providers),
	})
}

// Get returns a specific infrastructure provider.
func (h *InfraHandler) Get(c *gin.Context) {
	id := c.Param("id")

	provider, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if provider == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Infrastructure provider not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"provider": provider})
}

// Create creates a new infrastructure provider.
func (h *InfraHandler) Create(c *gin.Context) {
	var req models.InfraCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	provider, err := h.svc.Create(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"provider": provider})
}

// Update updates an infrastructure provider.
func (h *InfraHandler) Update(c *gin.Context) {
	id := c.Param("id")

	var req models.InfraUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	provider, err := h.svc.Update(c.Request.Context(), id, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if provider == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Infrastructure provider not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"provider": provider})
}

// Delete deletes an infrastructure provider.
func (h *InfraHandler) Delete(c *gin.Context) {
	id := c.Param("id")

	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// Connect connects to an infrastructure provider.
func (h *InfraHandler) Connect(c *gin.Context) {
	id := c.Param("id")

	provider, err := h.svc.Connect(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"provider": provider})
}

// Disconnect disconnects from an infrastructure provider.
func (h *InfraHandler) Disconnect(c *gin.Context) {
	id := c.Param("id")

	provider, err := h.svc.Disconnect(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"provider": provider})
}
