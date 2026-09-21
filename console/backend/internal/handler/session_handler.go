package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/middleware"
)

// errInvalidToken is returned when the supplied token matches no credential.
var errInvalidToken = errors.New("invalid API token")

// SessionHandler issues and clears browser sessions.
//
// A browser exchanges the configured API token for a short-lived session ONCE,
// receives an HttpOnly + SameSite=Strict cookie, and never puts the token in
// localStorage (where any script on the page could read it). Programmatic
// clients (MCP, CLI) keep using the Bearer token directly.
type SessionHandler struct {
	apiToken string
	scoped   []middleware.Token
	sessions *middleware.SessionStore
	// secure forces the Secure cookie attribute even when the request is plain
	// HTTP (e.g. behind a TLS-terminating proxy). A request over TLS also sets it.
	secure bool
}

// NewSessionHandler builds the handler.
func NewSessionHandler(apiToken string, scoped []middleware.Token, sessions *middleware.SessionStore, secure bool) *SessionHandler {
	return &SessionHandler{apiToken: apiToken, scoped: scoped, sessions: sessions, secure: secure}
}

// RegisterRoutes mounts the session endpoints on the authenticated API group.
func (h *SessionHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/session", h.Login)
	rg.DELETE("/session", h.Logout)
}

type sessionLoginRequest struct {
	Token string `json:"token" binding:"required"`
}

// Login exchanges a valid API token for a session cookie.
func (h *SessionHandler) Login(c *gin.Context) {
	var req sessionLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, err)
		return
	}
	subject, scope, ok := middleware.MatchToken(h.apiToken, h.scoped, req.Token)
	if !ok {
		respondError(c, http.StatusUnauthorized, errInvalidToken)
		return
	}
	sess, err := h.sessions.Create(subject, scope)
	if err != nil {
		respondError(c, http.StatusInternalServerError, err)
		return
	}
	h.setCookie(c, sess.ID, int(time.Until(sess.Expires).Seconds()))
	c.JSON(http.StatusOK, gin.H{
		"subject":    sess.Subject,
		"scope":      string(sess.Scope),
		"expires_at": sess.Expires,
	})
}

// Logout clears the caller's session.
func (h *SessionHandler) Logout(c *gin.Context) {
	if cookie, err := c.Cookie(middleware.SessionCookieName); err == nil {
		h.sessions.Delete(cookie)
	}
	h.setCookie(c, "", -1)
	c.Status(http.StatusNoContent)
}

func (h *SessionHandler) setCookie(c *gin.Context, id string, maxAge int) {
	c.SetSameSite(http.SameSiteStrictMode)
	secure := h.secure || c.Request.TLS != nil
	c.SetCookie(middleware.SessionCookieName, id, maxAge, "/api/v1", "", secure, true)
}
