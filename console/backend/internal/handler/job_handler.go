package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

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
		jobs.GET("/:id/log/stream", h.LogStream)
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

// streamPollInterval is how often the stream checks for new log output.
//
// Short enough that lines appear as they are written, not so short that an idle
// terraform step spins the CPU reopening a file. It is the SAME source the
// offset endpoint reads — the stream is a push wrapper over the exact bytes a
// poller would fetch, so a client that loses the stream and falls back sees no
// gap and no duplication.
const streamPollInterval = 750 * time.Millisecond

// streamMaxDuration bounds one streamed connection.
//
// A provision runs 15-25 minutes, but the console is reached over a qrexec TCP
// forward where a connection held open that long is a connection to lose (see
// Log). Capping it means the stream ENDS cleanly rather than dying: the client
// gets a terminal event carrying the last offset and reconnects — to the stream
// if it can, to the offset poller if it cannot. The cap is the design working,
// not a timeout to be tuned up.
const streamMaxDuration = 5 * time.Minute

// LogStream pushes a job's terraform output as Server-Sent Events.
//
// It exists alongside Log, not instead of it. Streaming gives an operator the
// output line-by-line as terraform prints it; the offset poller is the fallback
// the client degrades to when this connection drops, which over a qrexec
// forward it eventually will. Both read the same JobLogStore, so switching
// between them is seamless.
//
// Each event is the SAME JSON shape the offset endpoint returns
// ({offset,data,running,state}), so the client parses one format regardless of
// how it arrived. The offset in every event is what makes a fallback resume
// exactly where the stream stopped.
func (h *JobHandler) LogStream(c *gin.Context) {
	id := c.Param("id")

	job, err := h.jobs.GetByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrJobNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// A ResponseWriter that cannot flush would buffer the whole stream and
	// deliver it at the end — the opposite of streaming. Refuse rather than
	// silently behave like a slow non-stream; the client falls back to polling.
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// Defeats proxy buffering (nginx in particular), which would otherwise hold
	// events back until a buffer fills and undo the point of streaming.
	c.Header("X-Accel-Buffering", "no")

	if h.logs == nil {
		writeSSE(c, flusher, gin.H{
			"offset": 0, "data": "", "running": false,
			"state": job.State, "note": "job logs are not enabled on this console",
		})
		return
	}

	offset, _ := strconv.ParseInt(c.Query("offset"), 10, 64)
	if offset < 0 {
		offset = 0
	}

	ctx := c.Request.Context()
	deadline := time.NewTimer(streamMaxDuration)
	defer deadline.Stop()
	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()

	// Send whatever already exists immediately, so a client attaching to a job
	// mid-run does not wait a poll interval to see the backlog.
	offset = h.pushChunk(c, flusher, id, offset)

	for {
		// Re-read the job each iteration: "running" is what tells the client to
		// keep the connection, and it is the job record — not a quiet log — that
		// knows the apply finished.
		cur, err := h.jobs.GetByID(ctx, id)
		if err == nil && cur.State != orchestrator.JobRunning && cur.State != orchestrator.JobQueued {
			// Drain any final bytes written between the last tick and the job
			// ending, then send a terminal event and stop.
			offset = h.pushChunk(c, flusher, id, offset)
			writeSSE(c, flusher, gin.H{"offset": offset, "data": "", "running": false, "state": cur.State})
			return
		}

		select {
		case <-ctx.Done():
			// The client went away (or the qrexec forward dropped). Nothing to
			// send; it will reconnect with the offset it last saw.
			return
		case <-deadline.C:
			// End cleanly at the cap so the client reconnects rather than being
			// cut mid-event.
			return
		case <-ticker.C:
			offset = h.pushChunk(c, flusher, id, offset)
		}
	}
}

// pushChunk reads new log bytes from offset and, if any, emits one SSE event.
// Returns the offset to continue from — unchanged when there was nothing new.
func (h *JobHandler) pushChunk(c *gin.Context, flusher http.Flusher, id string, offset int64) int64 {
	data, next, err := h.logs.ReadFrom(id, offset, maxLogChunk)
	if err != nil {
		writeSSE(c, flusher, gin.H{"offset": offset, "error": err.Error()})
		return offset
	}
	if len(data) == 0 {
		return next
	}
	writeSSE(c, flusher, gin.H{"offset": next, "data": string(data), "running": true})
	return next
}

// writeSSE marshals one event and flushes it. A write error means the client is
// gone; the surrounding loop notices through the request context, so this need
// not return anything.
func writeSSE(c *gin.Context, flusher http.Flusher, payload gin.H) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
	flusher.Flush()
}
