// Package handler provides HTTP handlers for qube management.
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
)

// QubeHandler handles qube-related HTTP requests.
type QubeHandler struct {
	qubeSvc service.QubeService
	// certs exposes issued agent certificates (metadata only).
	certs *repository.AgentCertRepository
}

// NewQubeHandler creates a new QubeHandler.
// QubeHandlerOption customises a QubeHandler.
type QubeHandlerOption func(*QubeHandler)

// WithCertRepository enables the per-qube certificate listing endpoint.
func WithCertRepository(r *repository.AgentCertRepository) QubeHandlerOption {
	return func(h *QubeHandler) { h.certs = r }
}

func NewQubeHandler(qubeSvc service.QubeService, opts ...QubeHandlerOption) *QubeHandler {
	h := &QubeHandler{qubeSvc: qubeSvc}
	for _, opt := range opts {
		opt(h)
	}
	return h
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
		qubes.GET("/:id/reachable", h.CheckReachable)
		qubes.GET("/:id/certs", h.ListCerts)
	}
}

// List handles GET /qubes.
//
// Each qube carries both status (the compute instance) and agent_health (whether
// the agent inside it answers). They are reported separately and are meant to be
// read together: "running" plus "unreachable" is the case an operator previously
// had to discover by SSHing to a hypervisor node and running systemctl by hand.
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
//
// The response reports agent_health alongside status, plus when the agent was
// last probed, when it was last seen answering, and why the last probe failed.
// agent_health is always present — a missing field would read as "no opinion"
// when the honest answer for an unprobed qube is "unknown".
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

	op, err := h.qubeSvc.Create(c.Request.Context(), &req)
	if err != nil {
		handleQubeError(c, err)
		return
	}

	// 201 with the row, but the infrastructure is not up yet: provisioning runs
	// on a background worker and takes minutes. Poll the job for the outcome.
	respondOperation(c, http.StatusCreated, op)
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

	// Released, not destroyed: the compute VM goes away and the data disk stays.
	// Discarding the disk is a separate, explicitly confirmed action.
	c.JSON(http.StatusAccepted, gin.H{
		"message": "qube released: compute is being destroyed, the data disk is retained",
	})
}

// Start handles POST /qubes/:id/start.
func (h *QubeHandler) Start(c *gin.Context) {
	id := c.Param("id")

	op, err := h.qubeSvc.Start(c.Request.Context(), id)
	if err != nil {
		handleQubeError(c, err)
		return
	}

	// 202: the terraform apply has been queued, not performed. The qube is in a
	// transient status and settles when the job completes.
	respondOperation(c, http.StatusAccepted, op)
}

// Stop handles POST /qubes/:id/stop.
func (h *QubeHandler) Stop(c *gin.Context) {
	id := c.Param("id")

	op, err := h.qubeSvc.Stop(c.Request.Context(), id)
	if err != nil {
		handleQubeError(c, err)
		return
	}

	// 202: the terraform apply has been queued, not performed. The qube is in a
	// transient status and settles when the job completes.
	respondOperation(c, http.StatusAccepted, op)
}

// CheckReachable handles GET /qubes/:id/reachable. It probes the remote qube
// over the gRPC transport (cross-machine qrexec health check) and returns the
// result. 502 Bad Gateway when the qube can't be reached.
func (h *QubeHandler) CheckReachable(c *gin.Context) {
	id := c.Param("id")

	resp, err := h.qubeSvc.CheckReachable(c.Request.Context(), id)
	if err != nil {
		handleQubeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"reachable": true, "response": resp})
}

// handleQubeError maps service errors to HTTP responses.
// ListCerts returns the certificates issued to a qube's agent.
//
// Deliberately metadata only — fingerprint, validity, revocation state. The
// private key is returned exactly once, when the certificate is issued, and is
// never retrievable afterwards: an endpoint that hands out agent keys on demand
// would make every reader of this API able to impersonate any agent.
func (h *QubeHandler) ListCerts(c *gin.Context) {
	if h.certs == nil {
		respondError(c, http.StatusNotImplemented, errors.New("certificate issuance is not configured"))
		return
	}
	certs, err := h.certs.ListByQube(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondError(c, http.StatusInternalServerError, err)
		return
	}
	if certs == nil {
		certs = []*repository.AgentCert{}
	}
	c.JSON(http.StatusOK, gin.H{"certs": certs, "count": len(certs)})
}

// respondOperation writes an async operation result. The Location header points
// at the job so a client can poll without having to know how to build the URL.
func respondOperation(c *gin.Context, status int, op *service.Operation) {
	if op == nil {
		c.JSON(status, gin.H{})
		return
	}
	body := gin.H{"qube": op.Qube}
	if op.JobID != "" {
		body["job_id"] = op.JobID
		c.Header("Location", "/api/v1/jobs/"+op.JobID)
	}
	c.JSON(status, body)
}

func handleQubeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, repository.ErrTransitionConflict):
		// An operation is already in flight for this qube.
		respondError(c, http.StatusConflict, err)
	case errors.Is(err, orchestrator.ErrQueueFull):
		respondError(c, http.StatusTooManyRequests, err)
	case errors.Is(err, service.ErrQubeNotFound):
		respondError(c, http.StatusNotFound, err)
	case errors.Is(err, service.ErrZoneNotFound):
		respondError(c, http.StatusBadRequest, err)
	case errors.Is(err, service.ErrQubeNotStopped):
		respondError(c, http.StatusConflict, err)
	case errors.Is(err, service.ErrZoneDisconnected):
		respondError(c, http.StatusPreconditionFailed, err)
	case errors.Is(err, service.ErrInvalidQubeName):
		respondError(c, http.StatusBadRequest, err)
	case errors.Is(err, service.ErrInvalidQubeType):
		respondError(c, http.StatusBadRequest, err)
	case errors.Is(err, service.ErrUnreachable):
		respondError(c, http.StatusBadGateway, err)
	default:
		respondError(c, http.StatusInternalServerError, err)
	}
}
