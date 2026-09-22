package orchestrator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memJobStore is an in-memory JobStore for tests.
type memJobStore struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

func newMemJobStore() *memJobStore { return &memJobStore{jobs: map[string]*Job{}} }

func (m *memJobStore) Insert(_ context.Context, j *Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *j
	m.jobs[j.ID] = &cp
	return nil
}

func (m *memJobStore) Update(_ context.Context, j *Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *j
	m.jobs[j.ID] = &cp
	return nil
}

func (m *memJobStore) GetByID(_ context.Context, id string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, errors.New("job not found")
	}
	cp := *j
	return &cp, nil
}

func (m *memJobStore) ListByQube(_ context.Context, qubeID string, limit int) ([]*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Job
	for _, j := range m.jobs {
		if j.QubeID == qubeID {
			cp := *j
			out = append(out, &cp)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// blockingExecutor lets a test observe overlap: it reports the maximum number
// of calls that were ever in flight simultaneously.
type blockingExecutor struct {
	NoopExecutor
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	hold     time.Duration

	mu    sync.Mutex
	order []string
}

func (b *blockingExecutor) enter(name string) {
	n := b.inFlight.Add(1)
	for {
		cur := b.maxSeen.Load()
		if n <= cur || b.maxSeen.CompareAndSwap(cur, n) {
			break
		}
	}
	b.mu.Lock()
	b.order = append(b.order, name)
	b.mu.Unlock()
	time.Sleep(b.hold)
	b.inFlight.Add(-1)
}

func (b *blockingExecutor) Resume(_ context.Context, name string) error {
	b.enter(name)
	return nil
}

func (b *blockingExecutor) Suspend(_ context.Context, name string) error {
	b.enter(name)
	return nil
}

func (b *blockingExecutor) seenOrder() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.order...)
}

// TestRunnerSerializesWork is the core guarantee. terraform's local backend
// takes a NON-blocking lock on the state file, so two concurrent processes do
// not queue — the second fails outright. The single worker is what prevents
// that from ever happening.
func TestRunnerSerializesWork(t *testing.T) {
	be := &blockingExecutor{hold: 20 * time.Millisecond}
	store := newMemJobStore()
	done := make(chan string, 16)

	r := NewRunner(RunnerConfig{
		Executor: be,
		Store:    store,
		OnDone:   func(_ context.Context, j *Job) error { done <- j.ID; return nil },
		Timeout:  5 * time.Second,
	})
	r.Start()
	defer r.Shutdown(2 * time.Second)

	const n = 6
	for i := 0; i < n; i++ {
		if _, err := r.Submit(context.Background(), "q", "qube", ActionResume); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	for i := 0; i < n; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for job %d", i)
		}
	}

	if got := be.maxSeen.Load(); got != 1 {
		t.Errorf("at most one terraform invocation may be in flight, saw %d concurrent", got)
	}
}

// TestRunnerPreservesSubmissionOrder — a Stop followed by a Start must not
// execute in reverse, which is exactly what a mutex would permit since the
// runtime picks an arbitrary waiter.
func TestRunnerPreservesSubmissionOrder(t *testing.T) {
	be := &blockingExecutor{hold: 5 * time.Millisecond}
	done := make(chan struct{}, 8)

	r := NewRunner(RunnerConfig{
		Executor: be,
		Store:    newMemJobStore(),
		OnDone:   func(context.Context, *Job) error { done <- struct{}{}; return nil },
	})
	r.Start()
	defer r.Shutdown(2 * time.Second)

	want := []string{"first", "second", "third", "fourth"}
	for _, name := range want {
		if _, err := r.Submit(context.Background(), "q", name, ActionSuspend); err != nil {
			t.Fatalf("submit %s: %v", name, err)
		}
	}
	for range want {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out")
		}
	}

	got := be.seenOrder()
	if len(got) != len(want) {
		t.Fatalf("want %d executions, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("execution order: want %v, got %v", want, got)
			break
		}
	}
}

// TestRunnerCompletionRunsOnFailure — the completion hook is the only writer of
// a qube's terminal status. If it were skipped on failure the qube would stay
// in a transient status forever and every later operation would be refused as
// "busy".
func TestRunnerCompletionRunsOnFailure(t *testing.T) {
	fe := NewFakeExecutor()
	boom := errors.New("terraform exploded")
	fe.FailOn[ActionResume] = boom

	store := newMemJobStore()
	got := make(chan *Job, 1)

	r := NewRunner(RunnerConfig{
		Executor: fe,
		Store:    store,
		OnDone:   func(_ context.Context, j *Job) error { got <- j; return nil },
	})
	r.Start()
	defer r.Shutdown(2 * time.Second)

	job, err := r.Submit(context.Background(), "q1", "dev-work", ActionResume)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	select {
	case j := <-got:
		if j.State != JobFailed {
			t.Errorf("want JobFailed, got %q", j.State)
		}
		if j.Error == "" {
			t.Error("a failed job must carry the error text for the operator")
		}
		if j.FinishedAt == nil {
			t.Error("FinishedAt must be set even on failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion hook never ran on failure")
	}

	r.Shutdown(2 * time.Second)
	stored, err := store.GetByID(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if stored.State != JobFailed {
		t.Errorf("stored job state: want failed, got %q", stored.State)
	}
}

// TestRunnerSubmitDoesNotBlockOnLongWork — the whole reason for the queue. The
// server's WriteTimeout is 15s while a real apply takes ~6 minutes, so Submit
// must return promptly regardless of how long the work runs.
func TestRunnerSubmitDoesNotBlockOnLongWork(t *testing.T) {
	be := &blockingExecutor{hold: 300 * time.Millisecond}
	r := NewRunner(RunnerConfig{Executor: be, Store: newMemJobStore()})
	r.Start()
	defer r.Shutdown(2 * time.Second)

	start := time.Now()
	if _, err := r.Submit(context.Background(), "q", "slow", ActionResume); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Submit must not wait for the work; it took %s", elapsed)
	}
}

// TestRunnerQueueFullFailsFast — backpressure must be an immediate error, not
// an unbounded queue or a blocked HTTP handler.
func TestRunnerQueueFullFailsFast(t *testing.T) {
	be := &blockingExecutor{hold: 2 * time.Second}
	r := NewRunner(RunnerConfig{Executor: be, Store: newMemJobStore(), QueueSize: 1})
	r.Start()
	defer r.Shutdown(3 * time.Second)

	// One job occupies the worker, one fills the queue, the rest must be refused.
	var lastErr error
	for i := 0; i < 8; i++ {
		if _, err := r.Submit(context.Background(), "q", "x", ActionResume); err != nil {
			lastErr = err
			break
		}
	}
	if !errors.Is(lastErr, ErrQueueFull) {
		t.Errorf("want ErrQueueFull once the queue saturates, got %v", lastErr)
	}
}

type failingJobStore struct {
	*memJobStore
	failState JobState
}

func (s *failingJobStore) Update(ctx context.Context, j *Job) error {
	if j.State == s.failState {
		return errors.New("injected persistence failure")
	}
	return s.memJobStore.Update(ctx, j)
}

func TestRunnerDoesNotExecuteWithoutDurableStart(t *testing.T) {
	exec := NewFakeExecutor()
	store := &failingJobStore{memJobStore: newMemJobStore(), failState: JobRunning}
	r := NewRunner(RunnerConfig{Executor: exec, Store: store})
	job, err := r.Submit(context.Background(), "q", "remote-one", ActionDestroy)
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	r.Shutdown(time.Second)
	if len(exec.Calls()) != 0 {
		t.Fatal("provider executed despite failed durable running transition")
	}
	got, err := store.GetByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != JobFailed {
		t.Fatalf("start persistence failure left state %s", got.State)
	}
}

func TestRunnerCompletionFailureIsNotSuccess(t *testing.T) {
	store := newMemJobStore()
	r := NewRunner(RunnerConfig{Executor: NewFakeExecutor(), Store: store,
		OnDone: func(context.Context, *Job) error { return errors.New("RemoteVM cleanup failed") },
	})
	job, err := r.Submit(context.Background(), "q", "remote-one", ActionDestroy)
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	r.Shutdown(time.Second)
	got, err := store.GetByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != JobFailed || got.Error == "" {
		t.Fatalf("cleanup failure lost: %+v", got)
	}
}

func TestRunnerClosedDoesNotLeaveQueuedJob(t *testing.T) {
	store := newMemJobStore()
	r := NewRunner(RunnerConfig{Executor: NewFakeExecutor(), Store: store})
	r.Start()
	r.Shutdown(time.Second)
	_, err := r.Submit(context.Background(), "q", "remote-one", ActionDestroy)
	if !errors.Is(err, ErrRunnerClosed) {
		t.Fatal(err)
	}
	jobs, err := store.ListByQube(context.Background(), "q", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatal("closed submit recorded phantom work")
	}
}

func TestRunnerTimeoutCannotReportSuccess(t *testing.T) {
	store := newMemJobStore()
	r := NewRunner(RunnerConfig{Executor: &blockingExecutor{hold: 20 * time.Millisecond}, Store: store, Timeout: time.Millisecond})
	job, err := r.Submit(context.Background(), "q", "remote-timeout", ActionResume)
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	r.Shutdown(time.Second)
	got, err := store.GetByID(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != JobFailed {
		t.Fatalf("timed-out operation reported %s", got.State)
	}
}

// TestRunnerJobTimeoutComesFromConfig — the runner must use the configured
// bound when there is one, and a bound that clears a real provision when there
// is not. It previously fell back to 15 minutes while joblog.go and
// job_handler.go both document a 15-25 minute provision, so a healthy job could
// be canceled after its VM and disk already existed.
func TestRunnerJobTimeoutComesFromConfig(t *testing.T) {
	if got := NewRunner(RunnerConfig{}).timeout; got != DefaultJobTimeout {
		t.Fatalf("unconfigured timeout = %s, want %s", got, DefaultJobTimeout)
	}
	if DefaultJobTimeout < 30*time.Minute {
		t.Fatalf("default job timeout %s does not clear the documented 15-25 minute provision",
			DefaultJobTimeout)
	}
	if got := NewRunner(RunnerConfig{Timeout: 3 * time.Second}).timeout; got != 3*time.Second {
		t.Fatalf("configured timeout = %s, want 3s", got)
	}
}
