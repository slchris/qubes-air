package handler

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/service"
)

// settingsService is the part of service.SettingsService the handler uses. It
// is an interface so a test can hold a save open and prove that two saves never
// overlap between "stored" and "applied" (see updateMu).
type settingsService interface {
	Get(ctx context.Context) (*models.Settings, error)
	Update(ctx context.Context, settings *models.Settings) error
}

// SettingsHandler handles settings-related HTTP requests.
type SettingsHandler struct {
	svc settingsService
	// sessions receives the saved session timeout, so it governs browser
	// sessions from the moment it is saved rather than from the next restart.
	sessions *middleware.SessionStore
	// updateMu makes "store the settings, then apply the timeout" one step.
	// Without it two concurrent saves could store A then B but apply B then A,
	// leaving sessions on a lifetime nobody saved last.
	updateMu sync.Mutex
}

// NewSettingsHandler creates a new SettingsHandler. sessions is the store that
// backs cookie authentication; a saved session timeout is applied to it.
func NewSettingsHandler(svc *service.SettingsService, sessions *middleware.SessionStore) *SettingsHandler {
	return &SettingsHandler{svc: svc, sessions: sessions}
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

// Update validates and stores settings, then applies the session timeout to
// the live session store: new sessions get the new lifetime, and sessions
// already issued are shortened to it but never extended. A timeout outside
// the accepted range, or turning on a setting the console does not implement,
// is refused with 400 and changes nothing.
func (h *SettingsHandler) Update(c *gin.Context) {
	var settings models.Settings
	if err := c.ShouldBindJSON(&settings); err != nil {
		respondError(c, http.StatusBadRequest, err)
		return
	}

	h.updateMu.Lock()
	defer h.updateMu.Unlock()
	if err := h.svc.Update(c.Request.Context(), &settings); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, service.ErrInvalidSessionTimeout) || errors.Is(err, service.ErrUnsupportedSetting) {
			status = http.StatusBadRequest
		}
		respondError(c, status, err)
		return
	}
	if h.sessions != nil {
		h.sessions.SetTTL(time.Duration(settings.Security.SessionTimeout) * time.Minute)
	}

	c.JSON(http.StatusOK, gin.H{
		"settings": settings,
		"message":  "Settings saved successfully",
	})
}
