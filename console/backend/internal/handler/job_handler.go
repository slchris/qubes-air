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
}

// NewJobHandler creates a JobHandler.
func NewJobHandler(jobs *repository.JobRepository) *JobHandler {
	return &JobHandler{jobs: jobs}
}

// RegisterRoutes registers job routes.
func (h *JobHandler) RegisterRoutes(rg *gin.RouterGroup) {
	jobs := rg.Group("/jobs")
	{
		jobs.GET("", h.List)
		jobs.GET("/:id", h.GetByID)
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
