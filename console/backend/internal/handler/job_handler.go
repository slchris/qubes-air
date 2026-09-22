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
	// logs serves what the operation printed. Nil when orchestration is disabled or
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

// Log returns the operation output of a job, from ?offset= onwards.
//
// Offset-based polling rather than a streamed connection: a provision runs for
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
	// record rather than from "the log stopped growing": a slow provider call
	// prints nothing for minutes at a time, and treating that as completion is
	// how a UI decides an operation finished while it is still going.
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
// provider call spins the CPU reopening a file. It is the SAME source the
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
//
// The cap only means something if the connection is still writable when it
// arrives. http.Server.WriteTimeout (15s, see cmd/server/main.go) applies to the
// whole response, so it used to fail every write from second 15 onwards and the
// terminal event this design depends on was never sent — the client saw a
// stalled connection, not a clean end. LogStream therefore takes its own
// per-event write window.
const streamMaxDuration = 5 * time.Minute

// streamWriteWindow bounds ONE event's write.
//
// Longer than the server's global WriteTimeout, which is sized for ordinary
// responses, so a stream is not truncated by it; short enough that a client
// which stops accepting bytes is treated as gone instead of parking the handler
// inside a write. It is refreshed per event, so it bounds a single write and
// never the stream's total length — that stays streamMaxDuration.
const streamWriteWindow = 30 * time.Second

// LogStream pushes a job's operation output as Server-Sent Events.
//
// It exists alongside Log, not instead of it. Streaming gives an operator the
// output line-by-line as the operation prints it; the offset poller is the fallback
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
	if _, ok := c.Writer.(http.Flusher); !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return
	}
	// The stream owns its write deadline (streamWriteWindow). Left to the
	// server's WriteTimeout, every write would fail 15 seconds in and the events
	// — including the terminal one — would be dropped silently.
	rc := http.NewResponseController(c.Writer)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// Defeats proxy buffering (nginx in particular), which would otherwise hold
	// events back until a buffer fills and undo the point of streaming.
	c.Header("X-Accel-Buffering", "no")

	if h.logs == nil {
		_ = writeSSE(c, rc, gin.H{
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
	// mid-run does not wait a poll interval to see the backlog. A failed write
	// here means the client is already gone: it resumes from the offset it last
	// saw, so there is nobody left to tell.
	if offset, err = h.pushChunk(c, rc, id, offset); err != nil {
		return
	}

	for {
		// Re-read the job each iteration: "running" is what tells the client to
		// keep the connection, and it is the job record — not a quiet log — that
		// knows the apply finished.
		cur, err := h.jobs.GetByID(ctx, id)
		if err == nil && cur.State != orchestrator.JobRunning && cur.State != orchestrator.JobQueued {
			// Drain any final bytes written between the last tick and the job
			// ending, then send a terminal event and stop. The terminal event is
			// the client's signal to reconnect, so a failed write is reported by
			// the reconnecting client rather than here.
			offset, _ = h.pushChunk(c, rc, id, offset)
			_ = writeSSE(c, rc, gin.H{"offset": offset, "data": "", "running": false, "state": cur.State})
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
			next, err := h.pushChunk(c, rc, id, offset)
			if err != nil {
				// A write that fails is the honest end of the stream: the client
				// is gone or stopped reading, and the offset it last received is
				// what it reconnects with. Spinning here until the 5-minute cap
				// is what used to happen, and it looked like a healthy stream
				// from the server's side.
				return
			}
			offset = next
		}
	}
}

// pushChunk reads new log bytes from offset and, if any, emits one SSE event.
//
// It returns the offset to continue from — unchanged when there was nothing new
// — together with the write error, if any. The offset stays meaningful on error:
// it is the last one the client can be assumed to have received, which is what
// it reconnects with.
func (h *JobHandler) pushChunk(c *gin.Context, rc *http.ResponseController, id string, offset int64) (int64, error) {
	data, next, err := h.logs.ReadFrom(id, offset, maxLogChunk)
	if err != nil {
		// A read failure is the console's problem, not the client's: report it as
		// an event so the operator sees why the stream went quiet.
		return offset, writeSSE(c, rc, gin.H{"offset": offset, "error": err.Error()})
	}
	if len(data) == 0 {
		return next, nil
	}
	return next, writeSSE(c, rc, gin.H{"offset": next, "data": string(data), "running": true})
}

// writeSSE marshals one event, writes it under this stream's own write deadline
// and flushes, returning the error instead of swallowing it.
//
// The error is the only reliable way to notice a client that stopped reading:
// the request context does not necessarily fire while the connection is still
// open, and a swallowed write error is exactly how the stream used to spin to
// the 5-minute cap sending nothing.
func writeSSE(c *gin.Context, rc *http.ResponseController, payload gin.H) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Refresh the deadline per event: it bounds one write, not the stream. A
	// ResponseWriter that cannot carry a deadline (some test doubles) is not a
	// reason to drop the event — the write below reports its own failure.
	if err := rc.SetWriteDeadline(time.Now().Add(streamWriteWindow)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", b); err != nil {
		return err
	}
	return rc.Flush()
}
