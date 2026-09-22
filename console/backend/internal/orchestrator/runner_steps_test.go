package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// stepEventLog records the order in which the parts of one job happened. Step
// exists for its ordering guarantee, so these tests have to assert on a sequence
// rather than on a set of observations.
type stepEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *stepEventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *stepEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// tracingStore is a JobStore that records when the job row is written. The
// timestamp of the row is the whole reason a step may run at all: it is what
// makes "this destroyed something" traceable afterwards.
type tracingStore struct {
	*memJobStore
	log *stepEventLog
}

func (s *tracingStore) Insert(ctx context.Context, j *Job) error {
	s.log.add("insert " + string(j.State))
	return s.memJobStore.Insert(ctx, j)
}

func (s *tracingStore) Update(ctx context.Context, j *Job) error {
	s.log.add("update " + string(j.State))
	return s.memJobStore.Update(ctx, j)
}

// loggingExecutor records the provider call into the same log.
type loggingExecutor struct {
	NoopExecutor
	log *stepEventLog
}

func (e *loggingExecutor) Destroy(_ context.Context, qubeName string) error {
	e.log.add("action destroy " + qubeName)
	return nil
}

// waitForJob blocks until OnDone reports a job, so assertions read a settled
// runner rather than one mid-flight.
func waitForJob(t *testing.T, done <-chan *Job) *Job {
	t.Helper()
	select {
	case j := <-done:
		return j
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the job to finish")
		return nil
	}
}

// TestJobStepsRunAfterTheRowIsRecordedAndBeforeTheAction is the ordering
// contract the purge fix rests on: by the time a step can destroy anything, the
// job row exists and says the job is running, and the provider action has not
// been called yet.
func TestJobStepsRunAfterTheRowIsRecordedAndBeforeTheAction(t *testing.T) {
	log := &stepEventLog{}
	store := &tracingStore{memJobStore: newMemJobStore(), log: log}
	done := make(chan *Job, 1)
	r := NewRunner(RunnerConfig{
		Executor: &loggingExecutor{log: log},
		Store:    store,
		OnDone:   func(_ context.Context, j *Job) error { done <- j; return nil },
		Timeout:  5 * time.Second,
	})
	r.Start()
	defer r.Shutdown(2 * time.Second)

	step := func(ctx context.Context) error {
		recorded, err := store.ListByQube(ctx, "q1", 10)
		if err != nil {
			return err
		}
		if len(recorded) != 1 {
			return fmt.Errorf("step ran with %d recorded jobs, want exactly 1", len(recorded))
		}
		if recorded[0].State != JobRunning {
			return fmt.Errorf("step saw job state %q, want %q", recorded[0].State, JobRunning)
		}
		log.add("step")
		return nil
	}

	if _, err := r.Submit(context.Background(), "q1", "goner", ActionDestroy, step); err != nil {
		t.Fatalf("submit: %v", err)
	}
	settled := waitForJob(t, done)
	if settled.State != JobSucceeded {
		t.Fatalf("job state = %q, want %q (error: %s)", settled.State, JobSucceeded, settled.Error)
	}

	want := []string{"insert queued", "update running", "step", "action destroy goner", "update succeeded"}
	if got := log.snapshot(); !slices.Equal(got, want) {
		t.Errorf("job events = %v, want %v", got, want)
	}
}

// TestJobStepFailureSkipsTheProviderAction — a step that fails stops the job.
// Running the action anyway would destroy the very thing the step was preparing,
// or prepare something for an action that never happens.
func TestJobStepFailureSkipsTheProviderAction(t *testing.T) {
	log := &stepEventLog{}
	store := &tracingStore{memJobStore: newMemJobStore(), log: log}
	done := make(chan *Job, 1)
	r := NewRunner(RunnerConfig{
		Executor: &loggingExecutor{log: log},
		Store:    store,
		OnDone:   func(_ context.Context, j *Job) error { done <- j; return nil },
		Timeout:  5 * time.Second,
	})
	r.Start()
	defer r.Shutdown(2 * time.Second)

	stepErr := errors.New("purge: delete the per-qube data key: storage offline " +
		"(already done and not undone: lifted the data disk's protection); the data disk was not destroyed")
	job, err := r.Submit(context.Background(), "q1", "goner", ActionDestroy,
		func(context.Context) error { return stepErr })
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	settled := waitForJob(t, done)
	if settled.State != JobFailed {
		t.Fatalf("job state = %q, want %q", settled.State, JobFailed)
	}
	if !strings.Contains(settled.Error, "the data disk was not destroyed") {
		t.Errorf("job error must carry the step's account of partial execution, got %q", settled.Error)
	}
	if events := log.snapshot(); containsSubstring(events, "action destroy") {
		t.Errorf("the provider action ran despite a failed step: %v", events)
	}

	// The failed outcome is on the row, not only in OnDone: that row is the
	// record an operator reads later.
	persisted, err := store.GetByID(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if persisted.State != JobFailed || !strings.Contains(persisted.Error, "the data disk was not destroyed") {
		t.Errorf("persisted job = (%q, %q), want failed with the step's error", persisted.State, persisted.Error)
	}
}

// TestJobStepIsBoundedByTheJobTimeout — a step that hangs must not pin the
// single worker forever, and the job timeout has to cover it, not merely the
// provider call.
func TestJobStepIsBoundedByTheJobTimeout(t *testing.T) {
	log := &stepEventLog{}
	done := make(chan *Job, 2)
	r := NewRunner(RunnerConfig{
		Executor: &loggingExecutor{log: log},
		Store:    newMemJobStore(),
		OnDone:   func(_ context.Context, j *Job) error { done <- j; return nil },
		Timeout:  100 * time.Millisecond,
	})
	r.Start()
	defer r.Shutdown(2 * time.Second)

	if _, err := r.Submit(context.Background(), "q1", "goner", ActionDestroy,
		func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	settled := waitForJob(t, done)
	if settled.State != JobFailed {
		t.Fatalf("job state = %q, want %q", settled.State, JobFailed)
	}
	if !strings.Contains(settled.Error, context.DeadlineExceeded.Error()) {
		t.Errorf("job error = %q, want it to report %v", settled.Error, context.DeadlineExceeded)
	}
	if events := log.snapshot(); containsSubstring(events, "action destroy") {
		t.Errorf("the provider action ran despite the step timing out: %v", events)
	}

	// The worker survived: a later job still runs. A step that killed the
	// goroutine would leave the queue draining into nothing.
	if _, err := r.Submit(context.Background(), "q1", "next", ActionResume); err != nil {
		t.Fatalf("submit after a timed-out step: %v", err)
	}
	if next := waitForJob(t, done); next.State != JobSucceeded {
		t.Errorf("job after a timed-out step = %q, want %q", next.State, JobSucceeded)
	}
}

// TestNilJobStepIsSkipped — Step is exported, and calling a nil func would panic
// the single worker goroutine, taking every queued job with it.
func TestNilJobStepIsSkipped(t *testing.T) {
	done := make(chan *Job, 1)
	r := NewRunner(RunnerConfig{
		Executor: &loggingExecutor{log: &stepEventLog{}},
		Store:    newMemJobStore(),
		OnDone:   func(_ context.Context, j *Job) error { done <- j; return nil },
		Timeout:  5 * time.Second,
	})
	r.Start()
	defer r.Shutdown(2 * time.Second)

	var noStep Step
	if _, err := r.Submit(context.Background(), "q1", "goner", ActionDestroy, noStep); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if settled := waitForJob(t, done); settled.State != JobSucceeded {
		t.Errorf("job state = %q, want %q (error: %s)", settled.State, JobSucceeded, settled.Error)
	}
}

func containsSubstring(events []string, part string) bool {
	for _, e := range events {
		if strings.Contains(e, part) {
			return true
		}
	}
	return false
}
