package orchestrator

import (
	"context"
	"testing"
	"time"
)

// TestRunnerHealthWithoutAHeartbeat — a runner that was never started has never
// reported for duty, and /health must say so. This is also the state after the
// worker goroutine has exited: the queue stops being serviced while the process
// carries on answering requests.
func TestRunnerHealthWithoutAHeartbeat(t *testing.T) {
	r := NewRunner(RunnerConfig{Executor: NewFakeExecutor(), Store: newMemJobStore()})

	h := r.HealthAt(time.Now())
	if !h.Enabled {
		t.Fatal("a real runner must report itself enabled, or the check is skipped")
	}
	if h.DispatcherAlive {
		t.Fatal("a dispatcher that never beat must not report alive")
	}
	if !h.LastBeat.IsZero() {
		t.Fatalf("unstarted dispatcher has heartbeat %s", h.LastBeat)
	}
}

// TestRunnerHealthStalenessBound pins the bound from both sides, on an injected
// clock: a heartbeat inside its budget is alive, and the first instant past it
// is not. Nothing here sleeps, so the result cannot depend on scheduling.
func TestRunnerHealthStalenessBound(t *testing.T) {
	base := time.Now().UTC()
	r := NewRunner(RunnerConfig{Executor: NewFakeExecutor(), Store: newMemJobStore()})
	r.beat(base)

	if h := r.HealthAt(base.Add(DispatcherStaleAfter)); !h.DispatcherAlive {
		t.Fatalf("heartbeat exactly at the staleness bound reported dead: %+v", h)
	}
	if h := r.HealthAt(base.Add(DispatcherStaleAfter + time.Millisecond)); h.DispatcherAlive {
		t.Fatalf("heartbeat past the staleness bound reported alive: %+v", h)
	}
}

// TestRunnerHealthAllowsForTheJobItIsRunning — while the worker is inside a job
// it cannot poll, and a legitimate provision takes 15-25 minutes. Declaring that
// unhealthy would restart a console that is doing exactly what it was asked to,
// so the budget inside a job is the job's own configured timeout.
func TestRunnerHealthAllowsForTheJobItIsRunning(t *testing.T) {
	base := time.Now().UTC()
	r := NewRunner(RunnerConfig{
		Executor: NewFakeExecutor(),
		Store:    newMemJobStore(),
		Timeout:  30 * time.Minute,
	})
	r.beat(base)
	r.running.Store(1)

	if h := r.HealthAt(base.Add(30*time.Minute + DispatcherStaleAfter)); !h.DispatcherAlive {
		t.Fatalf("a job inside its timeout reported dead: %+v", h)
	}
	if h := r.HealthAt(base.Add(30*time.Minute + DispatcherStaleAfter + time.Millisecond)); h.DispatcherAlive {
		t.Fatalf("a job past its timeout reported alive: %+v", h)
	}
}

// TestRunnerHealthCountsQueuedJobs — the counts are the informational half of the
// endpoint: how much work is waiting and whether the worker is busy. A queued job
// is not a failure, so the same state must still read as alive.
func TestRunnerHealthCountsQueuedJobs(t *testing.T) {
	base := time.Now().UTC()
	r := NewRunner(RunnerConfig{Executor: NewFakeExecutor(), Store: newMemJobStore(), QueueSize: 4})
	for i := 0; i < 3; i++ {
		if _, err := r.Submit(context.Background(), "q", "qube", ActionResume); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}

	r.beat(base)
	h := r.HealthAt(base)
	if h.Queued != 3 || h.Running != 0 {
		t.Fatalf("queue state = queued %d running %d, want 3 and 0", h.Queued, h.Running)
	}
	if !h.DispatcherAlive {
		t.Fatal("a queue with waiting work is not a health failure")
	}
}

// TestStartedDispatcherIsAlive — the happy path: Start stamps the heartbeat
// before it spawns the worker, so the first probe after a successful start can
// never read "dead" because the goroutine has not been scheduled yet.
func TestStartedDispatcherIsAlive(t *testing.T) {
	r := NewRunner(RunnerConfig{Executor: NewFakeExecutor(), Store: newMemJobStore()})
	r.Start()
	defer r.Shutdown(time.Second)

	h := r.Health()
	if !h.Enabled || !h.DispatcherAlive {
		t.Fatalf("started dispatcher reported %+v", h)
	}
	if h.LastBeat.IsZero() {
		t.Fatal("started dispatcher has no heartbeat")
	}
}

// TestDispatcherBeatsAroundAJob — the counts must come back to rest once a job
// finishes. A running count stuck above zero would extend the health budget
// forever, which is the same as having no check at all.
//
// Shutdown returns only after the worker goroutine has exited, so the assertion
// after it sees the final state without racing the loop.
func TestDispatcherBeatsAroundAJob(t *testing.T) {
	done := make(chan struct{}, 1)
	r := NewRunner(RunnerConfig{
		Executor: NewFakeExecutor(),
		Store:    newMemJobStore(),
		OnDone:   func(context.Context, *Job) error { done <- struct{}{}; return nil },
	})
	r.Start()

	if _, err := r.Submit(context.Background(), "q", "qube", ActionResume); err != nil {
		t.Fatalf("submit: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job never completed")
	}
	r.Shutdown(2 * time.Second)

	if got := r.running.Load(); got != 0 {
		t.Fatalf("running count stuck at %d after the job finished", got)
	}
	if h := r.Health(); h.LastBeat.IsZero() {
		t.Fatal("dispatch after a job left no heartbeat")
	}
}

// TestNilRunnerHealthIsDisabledNotUnhealthy — the server holds a nil Runner when
// orchestration is disabled. Health must say "nothing to report", not claim a
// dead worker: operations then run inline by design, and a red /health would
// take down a console that is working.
func TestNilRunnerHealthIsDisabledNotUnhealthy(t *testing.T) {
	var r *Runner

	h := r.Health()
	if h.Enabled {
		t.Fatal("a nil runner must not report itself enabled")
	}
	if h.DispatcherAlive {
		t.Fatal("a nil runner has no dispatcher to be alive")
	}
}
