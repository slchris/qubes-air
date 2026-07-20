package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
)

func streamRig(t *testing.T) (*gin.Engine, *repository.JobRepository, *orchestrator.JobLogStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	f, err := os.CreateTemp(t.TempDir(), "jobs-*.db")
	require.NoError(t, err)
	_ = f.Close()
	cfg := database.DefaultConfig()
	cfg.DSN = f.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	jobs := repository.NewJobRepository(db)
	logs, err := orchestrator.NewJobLogStore(t.TempDir())
	require.NoError(t, err)

	r := gin.New()
	NewJobHandler(jobs, logs).RegisterRoutes(r.Group("/api/v1"))
	return r, jobs, logs
}

func insertJob(t *testing.T, jobs *repository.JobRepository, id string, state orchestrator.JobState) {
	t.Helper()
	require.NoError(t, jobs.Insert(context.Background(), &orchestrator.Job{
		ID: id, QubeName: "q", Action: orchestrator.ActionProvision, State: state,
	}))
}

func finishJob(t *testing.T, jobs *repository.JobRepository, id string, state orchestrator.JobState) {
	t.Helper()
	j, err := jobs.GetByID(context.Background(), id)
	require.NoError(t, err)
	j.State = state
	require.NoError(t, jobs.Update(context.Background(), j))
}

// The whole point: bytes written to the log DURING the stream must arrive
// without the client asking, and the terminal event must mark the job done.
func TestLogStreamPushesWhileWritingThenEnds(t *testing.T) {
	r, jobs, logs := streamRig(t)
	insertJob(t, jobs, "job-1", orchestrator.JobRunning)

	lf, err := logs.Create("job-1")
	require.NoError(t, err)
	_, _ = lf.WriteString("line one\n")

	// httptest.ResponseRecorder does not stream, so drive the handler on a real
	// server and read the socket incrementally.
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/jobs/job-1/log/stream", nil)
	resp, err := http.DefaultClient.Do(hreq)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	events := make(chan map[string]any, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var m map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m) == nil {
				events <- m
			}
		}
		close(events)
	}()

	// First event carries the backlog written before we connected.
	first := recvEvent(t, events)
	require.Contains(t, first["data"], "line one")

	// Write more AFTER the stream is attached; it must be pushed on the next tick.
	_, _ = lf.WriteString("line two\n")
	got := waitForData(t, events, "line two")
	require.NotNil(t, got)

	// End the job; the stream must send a terminal event with running=false.
	finishJob(t, jobs, "job-1", orchestrator.JobSucceeded)
	_ = lf.Close()

	term := waitForRunningFalse(t, events)
	require.Equal(t, "succeeded", term["state"])
}

// A stream over a job that is already finished sends the whole log and one
// terminal event, then closes — it does not hang waiting for a running job.
func TestLogStreamOfFinishedJobClosesPromptly(t *testing.T) {
	r, jobs, logs := streamRig(t)
	insertJob(t, jobs, "job-2", orchestrator.JobRunning)
	lf, err := logs.Create("job-2")
	require.NoError(t, err)
	_, _ = lf.WriteString("all done\n")
	_ = lf.Close()
	finishJob(t, jobs, "job-2", orchestrator.JobSucceeded)

	srv := httptest.NewServer(r)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/jobs/job-2/log/stream", nil)
	resp, err := http.DefaultClient.Do(hreq)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var sawData, sawTerminal bool
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m))
		if s, _ := m["data"].(string); strings.Contains(s, "all done") {
			sawData = true
		}
		if running, _ := m["running"].(bool); !running {
			sawTerminal = true
		}
	}
	require.True(t, sawData, "the finished job's log was not delivered")
	require.True(t, sawTerminal, "no terminal running=false event")
}

func TestLogStreamUnknownJobIs404(t *testing.T) {
	r, _, _ := streamRig(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/nope/log/stream", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}

func recvEvent(t *testing.T, ch <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case m, ok := <-ch:
		require.True(t, ok, "stream closed before an event arrived")
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an SSE event")
		return nil
	}
}

func waitForData(t *testing.T, ch <-chan map[string]any, want string) map[string]any {
	t.Helper()
	deadline := time.After(6 * time.Second)
	for {
		select {
		case m, ok := <-ch:
			require.True(t, ok, "stream closed before %q", want)
			if s, _ := m["data"].(string); strings.Contains(s, want) {
				return m
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", want)
			return nil
		}
	}
}

func waitForRunningFalse(t *testing.T, ch <-chan map[string]any) map[string]any {
	t.Helper()
	deadline := time.After(6 * time.Second)
	for {
		select {
		case m, ok := <-ch:
			require.True(t, ok, "stream closed before a terminal event")
			if running, ok := m["running"].(bool); ok && !running {
				return m
			}
		case <-deadline:
			t.Fatal("timed out waiting for running=false")
			return nil
		}
	}
}
