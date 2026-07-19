package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// JobHandler serves the orchestration job history.
//
// Jobs serve two audiences. A client that just issued a 202 polls one job to
// learn the outcome of its own request; an operator reads the list to see every
// infrastructure change this console made, including the ones that failed.
type JobHandler struct {
	jobs *repository.JobRepository
	// logs serves what terraform printed. Nil when orchestration is disabled or
	// the log directory could not be created; the endpoint then reports that
	// plainly instead of looking like a job with no output.
	logs *orchestrator.JobLogStore
}

// NewJobHandler creates a JobHandler.
func NewJobHandler(jobs *repository.JobRepository, logs *orchestrator.JobLogStore) *JobHandler {
	return &JobHandler{jobs: jobs, logs: logs}
}

// RegisterRoutes registers job routes.
func (h *JobHandler) RegisterRoutes(rg *gin.RouterGroup) {
	jobs := rg.Group("/jobs")
	{
		jobs.GET("", h.List)
		jobs.GET("/:id", h.GetByID)
		jobs.GET("/:id/log", h.Log)
	}
}

// defaultJobLimit bounds an unqualified audit listing.
const defaultJobLimit = 100

// maxJobLimit caps what a caller may request in one page.
const maxJobLimit = 500

// GetByID returns a single job — the poll target for a 202 response.
func (h *JobHandler) GetByID(c *gin.Context) {
	job, err := h.jobs.GetByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, repository.ErrJobNotFound) {
			respondError(c, http.StatusNotFound, err)
			return
		}
		respondError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, job)
}

// List returns recent jobs, newest first. Filter to one qube with ?qube_id=.
func (h *JobHandler) List(c *gin.Context) {
	limit := defaultJobLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			respondError(c, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return
		}
		limit = min(n, maxJobLimit)
	}

	var (
		jobs []*orchestrator.Job
		err  error
	)
	if qubeID := c.Query("qube_id"); qubeID != "" {
		jobs, err = h.jobs.ListByQube(c.Request.Context(), qubeID, limit)
	} else {
		jobs, err = h.jobs.List(c.Request.Context(), limit)
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, err)
		return
	}
	if jobs == nil {
		jobs = []*orchestrator.Job{}
	}

	c.JSON(http.StatusOK, gin.H{"jobs": jobs, "count": len(jobs)})
}

// maxLogChunk bounds one log response. A failed apply can print a great deal;
// returning it all in one body would stall the browser rendering it and, on a
// poll loop, resend the same megabytes on every tick.
const maxLogChunk = 256 * 1024

// Log returns the terraform output of a job, from ?offset= onwards.
//
// Offset-based polling rather than a streamed connection: an apply runs for
// twenty minutes, and a held-open connection through the qrexec TCP forward
// this console is reached over is a connection to lose. The client asks for
// what it has not seen, which reads the same whether the job is still running,
// finished an hour ago, or ran before the last console restart.
func (h *JobHandler) Log(c *gin.Context) {
	id := c.Param("id")

	// Confirm the job exists before reporting on its log, so a mistyped id is a
	// 404 rather than an empty log that looks like a job which printed nothing.
	job, err := h.jobs.GetByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrJobNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if h.logs == nil {
		c.JSON(http.StatusOK, gin.H{
			"offset": 0, "data": "", "running": job.State == orchestrator.JobRunning,
			"note": "job logs are not enabled on this console",
		})
		return
	}

	offset, _ := strconv.ParseInt(c.Query("offset"), 10, 64)
	if offset < 0 {
		offset = 0
	}

	data, next, err := h.logs.ReadFrom(id, offset, maxLogChunk)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// running tells the client whether to poll again. Derived from the job
	// record rather than from "the log stopped growing": a slow terraform step
	// prints nothing for minutes at a time, and treating that as completion is
	// how a UI decides an apply finished while it is still going.
	c.JSON(http.StatusOK, gin.H{
		"offset":  next,
		"data":    string(data),
		"running": job.State == orchestrator.JobRunning || job.State == orchestrator.JobQueued,
		"state":   job.State,
	})
}
