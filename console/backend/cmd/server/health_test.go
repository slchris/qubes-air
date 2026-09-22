package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
)

// newHealthDB opens a real database in a temp directory: /health's whole point is
// that it probes the database rather than a stub, so the test uses the same
// constructor the server does.
func newHealthDB(t *testing.T) *database.DB {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "health.db")
	db, err := database.New(cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// getHealthBody issues the request and decodes the body once, so each test reads
// both the status code and the fields that explain it.
func getHealthBody(t *testing.T, h gin.HandlerFunc) (int, healthBody) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/health", nil)

	h(c)

	var body healthBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /health body %q: %v", w.Body.String(), err)
	}
	return w.Code, body
}

// TestHealthIsRedWhenDispatcherIsStale is the failure this endpoint used to be
// blind to: the dispatcher is not running, so no queued job will ever execute,
// while every HTTP request keeps succeeding. A heartbeat that was never stamped
// is exactly that state — it is also what a worker that exited leaves behind.
//
// The runner is a real orchestrator.Runner, not a stub, so the staleness rule
// under test is the one the server runs. It is never started, which makes the
// result deterministic: no clock to race, no sleep.
func TestHealthIsRedWhenDispatcherIsStale(t *testing.T) {
	runner := orchestrator.NewRunner(orchestrator.RunnerConfig{
		Executor: orchestrator.NewFakeExecutor(),
		Store:    orchestrator.NewMemoryJobStore(),
	})

	code, body := getHealthBody(t, healthHandler(newHealthDB(t), runner))

	if code != http.StatusServiceUnavailable {
		t.Fatalf("stale dispatcher: status = %d, want 503", code)
	}
	if body.Status != statusUnhealthy {
		t.Errorf("stale dispatcher: body status = %q, want %q", body.Status, statusUnhealthy)
	}
	if body.Worker.Dispatcher != dispatcherStale {
		t.Errorf("worker.dispatcher = %q, want %q", body.Worker.Dispatcher, dispatcherStale)
	}
	// The database is fine and must say so: a probe that blames everything
	// teaches its reader nothing.
	if body.Database != dbConnected {
		t.Errorf("database = %q, want %q", body.Database, dbConnected)
	}
}

// TestHealthIsGreenWhenDispatcherIsAlive — the guard against a probe that is red
// for a console that is working. Start stamps the heartbeat synchronously, so
// the fresh case needs no waiting either.
func TestHealthIsGreenWhenDispatcherIsAlive(t *testing.T) {
	runner := orchestrator.NewRunner(orchestrator.RunnerConfig{
		Executor: orchestrator.NewFakeExecutor(),
		Store:    orchestrator.NewMemoryJobStore(),
	})
	runner.Start()
	t.Cleanup(func() { runner.Shutdown(time.Second) })

	code, body := getHealthBody(t, healthHandler(newHealthDB(t), runner))

	if code != http.StatusOK {
		t.Fatalf("live dispatcher: status = %d, want 200", code)
	}
	if body.Status != statusHealthy {
		t.Errorf("body status = %q, want %q", body.Status, statusHealthy)
	}
	if body.Worker.Dispatcher != dispatcherAlive {
		t.Errorf("worker.dispatcher = %q, want %q", body.Worker.Dispatcher, dispatcherAlive)
	}
	if body.Database != dbConnected {
		t.Errorf("database = %q, want %q", body.Database, dbConnected)
	}
}

// TestHealthIsRedWhenDatabaseIsNotWritable — the database half has its own tests
// at the probe (internal/database), and this pins the handler's mapping: a
// database that cannot be written is a 503 with database=disconnected, whatever
// the worker says. Without it the worker check could mask a failing probe.
func TestHealthIsRedWhenDatabaseIsNotWritable(t *testing.T) {
	db := newHealthDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	runner := orchestrator.NewRunner(orchestrator.RunnerConfig{
		Executor: orchestrator.NewFakeExecutor(),
		Store:    orchestrator.NewMemoryJobStore(),
	})
	runner.Start()
	t.Cleanup(func() { runner.Shutdown(time.Second) })

	code, body := getHealthBody(t, healthHandler(db, runner))

	if code != http.StatusServiceUnavailable {
		t.Fatalf("closed database: status = %d, want 503", code)
	}
	if body.Status != statusUnhealthy || body.Database != dbDisconnected {
		t.Errorf("closed database: status %q database %q, want %q/%q",
			body.Status, body.Database, statusUnhealthy, dbDisconnected)
	}
	// The worker is healthy and must still say so.
	if body.Worker.Dispatcher != dispatcherAlive {
		t.Errorf("worker.dispatcher = %q, want %q", body.Worker.Dispatcher, dispatcherAlive)
	}
}

// TestHealthIsGreenWithOrchestrationDisabled — a nil runner is the configured
// state when orchestration is off (docker-compose sets it off by default).
// Reporting that as unhealthy would fail the liveness probe of a console that is
// working as configured.
func TestHealthIsGreenWithOrchestrationDisabled(t *testing.T) {
	code, body := getHealthBody(t, healthHandler(newHealthDB(t), nil))

	if code != http.StatusOK {
		t.Fatalf("disabled orchestration: status = %d, want 200", code)
	}
	if body.Worker.Dispatcher != dispatcherDisabled {
		t.Errorf("worker.dispatcher = %q, want %q", body.Worker.Dispatcher, dispatcherDisabled)
	}
}
