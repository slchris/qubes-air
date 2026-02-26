package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/service"
)

// CredentialHandler handles credential-related HTTP requests.
type CredentialHandler struct {
	svc *service.CredentialService
}

// NewCredentialHandler creates a new CredentialHandler.
func NewCredentialHandler(svc *service.CredentialService) *CredentialHandler {
	return &CredentialHandler{svc: svc}
}

// RegisterRoutes registers credential routes.
func (h *CredentialHandler) RegisterRoutes(rg *gin.RouterGroup) {
	creds := rg.Group("/credentials")
	creds.GET("", h.List)
	creds.GET("/:id", h.Get)
	creds.POST("", h.Create)
	creds.PUT("/:id", h.Update)
	creds.DELETE("/:id", h.Delete)
}

// List returns all credentials.
func (h *CredentialHandler) List(c *gin.Context) {
	credentials, err := h.svc.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if credentials == nil {
		credentials = []models.Credential{}
	}

	c.JSON(http.StatusOK, gin.H{
		"credentials": credentials,
		"total":       len(credentials),
	})
}

// Get returns a specific credential.
func (h *CredentialHandler) Get(c *gin.Context) {
	id := c.Param("id")

	credential, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if credential == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Credential not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"credential": credential})
}

// Create creates a new credential.
func (h *CredentialHandler) Create(c *gin.Context) {
	var req models.CredentialCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	credential, err := h.svc.Create(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"credential": credential})
}

// Update updates a credential.
func (h *CredentialHandler) Update(c *gin.Context) {
	id := c.Param("id")

	var req models.CredentialUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	credential, err := h.svc.Update(c.Request.Context(), id, req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if credential == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Credential not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"credential": credential})
}

// Delete deletes a credential.
func (h *CredentialHandler) Delete(c *gin.Context) {
	id := c.Param("id")

	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"deleted": true})
}
