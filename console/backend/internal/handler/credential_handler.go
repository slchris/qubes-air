package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/service"
)

// notFoundMessage is the one error text for an ID the caller cannot address,
// whether it is missing or names a console row.
const notFoundMessage = "Credential not found"

// CredentialHandler handles credential-related HTTP requests.
//
// It serves the operator's credentials only. The console's own secrets share
// the table and are invisible here: they are left out of the list, and their
// IDs answer exactly like IDs that do not exist (see service.CredentialService).
// The routes are fleet-only, so a zone-restricted token is refused before it
// reaches this handler (middleware.RequireZones).
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

// List returns the operator's credentials.
func (h *CredentialHandler) List(c *gin.Context) {
	credentials, err := h.svc.List(c.Request.Context())
	if err != nil {
		respondCredentialError(c, err)
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
	credential, err := h.svc.GetByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondCredentialError(c, err)
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
		respondCredentialError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"credential": credential})
}

// Update updates a credential.
func (h *CredentialHandler) Update(c *gin.Context) {
	var req models.CredentialUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	credential, err := h.svc.Update(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		respondCredentialError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"credential": credential})
}

// Delete deletes a credential.
func (h *CredentialHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		respondCredentialError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// respondCredentialError maps a service error to the response.
//
// A console row and a missing ID get the same status and body. Only the audit
// outcome differs: touching a console row is recorded as denied, so the
// operator can see the attempt, while a caller cannot tell the two apart.
// Storage failures are logged here and answered generically; their text can
// name tables, paths or key versions.
func respondCredentialError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrConsoleCredential):
		middleware.MarkDenied(c)
		c.JSON(http.StatusNotFound, gin.H{"error": notFoundMessage})
	case errors.Is(err, service.ErrCredentialNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": notFoundMessage})
	case errors.Is(err, service.ErrReservedCredential):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	default:
		log.Printf("credentials: %s %s: %v", c.Request.Method, c.FullPath(), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": http.StatusText(http.StatusInternalServerError)})
	}
}
