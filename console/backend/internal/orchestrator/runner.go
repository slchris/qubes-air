package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Action is the infrastructure verb a job performs.
type Action string

// Job actions.
const (
	ActionProvision Action = "provision"
	ActionResume    Action = "resume"
	ActionSuspend   Action = "suspend"
	ActionDestroy   Action = "destroy"
	// ActionRelease performs the same provider work as ActionSuspend — destroy
	// the compute VM, keep the data disk — but records a different intent: the
	// user deleted the qube rather than parking it. They are distinguished so
	// job history reads truthfully and so the completion hook can land the qube
	// on "released" rather than "suspended".
	ActionRelease Action = "release"
)

// JobState is the lifecycle of a single orchestration invocation.
type JobState string

// Job states.
const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	// JobUnknown means the job was interrupted by a process restart and the
	// provider could not confirm whether its work completed. It is deliberately
	// distinct from JobFailed, which records an observed execution error.
	// Both can have partial side effects; retries must use persisted identity.
	JobUnknown JobState = "unknown"
)

// Job is one orchestration invocation. It outlives the HTTP request that asked for
// it: a real apply takes minutes, far longer than any request may block, so the
// caller is handed a job id and polls for the outcome.
type Job struct {
	ID         string     `json:"id"`
	QubeID     string     `json:"qube_id"`
	QubeName   string     `json:"qube_name"`
	Action     Action     `json:"action"`
	State      JobState   `json:"state"`
	Error      string     `json:"error,omitempty"`
	EnqueuedAt time.Time  `json:"enqueued_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// JobStore records jobs so a client can poll for an outcome.
type JobStore interface {
	Insert(ctx context.Context, j *Job) error
	Update(ctx context.Context, j *Job) error
	GetByID(ctx context.Context, id string) (*Job, error)
	ListByQube(ctx context.Context, qubeID string, limit int) ([]*Job, error)
}

// Completion runs on the worker goroutine once a job terminates, so a qube's
// stored status can follow the real outcome rather than the intent.
//
// It is the ONLY place a terminal status is written. With operations running
// asynchronously, nothing else is still around to do it when an operation
// finishes.
type Completion func(ctx context.Context, j *Job) error

// Runner errors.
var (
	ErrQueueFull    = errors.New("orchestration queue is full")
	ErrRunnerClosed = errors.New("orchestration runner is shutting down")
)

// Runner serializes every provider operation onto a single worker goroutine.
//
// One worker is the mutual exclusion. A mutex would also serialize, but it
// cannot be canceled, gives no backpressure, and offers no way to report what is
// happening; with operations measured in minutes those matter. A queue
// additionally guarantees submission order, so a Stop immediately followed by a
// Start cannot execute in reverse.
type Runner struct {
	exec    Executor
	store   JobStore
	onDone  Completion
	timeout time.Duration
	// logs captures each job's operation output. Nil disables it, which costs
	// visibility into a running operation but never blocks one.
	logs *JobLogStore

	queue chan *Job

	// beatNs is when the dispatcher last reported for duty, in Unix
	// nanoseconds; zero means it never has. See health.go for the staleness
	// rules /health applies to it.
	beatNs atomic.Int64
	// running counts the jobs the worker is executing. The worker is a single
	// goroutine, so today this is 0 or 1; it is a counter rather than a flag so
	// it stays correct if the pool ever grows.
	running atomic.Int32

	// base is the lifetime context for all provider work. It is deliberately
	// derived from context.Background() and never from an HTTP request: a
	// client disconnect must not abort an operation midway, because a
	// half-finished run can leave VMs and disks that qube_infra has no record
	// of.
	base   context.Context
	cancel context.CancelFunc

	wg   sync.WaitGroup
	stop sync.Once

	// closeMu/closing guard the queue against a send racing its close.
	// Submit takes the read lock; Shutdown takes the write lock before closing.
	closeMu sync.RWMutex
	closing bool
}

// RunnerConfig configures a Runner.
type RunnerConfig struct {
	Executor  Executor
	Store     JobStore
	OnDone    Completion
	QueueSize int
	Timeout   time.Duration
	// Logs captures each job's operation output as it is produced, so a running
	// apply can be watched rather than only reported on once it ends. Optional.
	Logs *JobLogStore
}

// DefaultQueueSize bounds how many operations may be waiting. Past this,
// Submit reports ErrQueueFull rather than growing without limit.
const DefaultQueueSize = 64

// DefaultJobTimeout bounds one orchestration job when the caller configures no
// timeout.
//
// It has to cover the whole job, not the provider calls inside it: the context
// built from it wraps the entire action, and provider-level waits (proxmox
// Client.WaitTask) poll until that context expires rather than bounding
// themselves. A provision clones a template, installs the agent package,
// attaches and unlocks the data disk — 15-25 minutes on hardware, with the
// package install alone measured at 857 seconds (internal/config/config.go).
// The previous 15-minute bound sat inside that range, so a healthy provision
// could be canceled after its VM and disk already existed.
const DefaultJobTimeout = 45 * time.Minute

// NewRunner builds a Runner. Call Start to spawn the worker.
func NewRunner(cfg RunnerConfig) *Runner {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultQueueSize
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultJobTimeout
	}
	base, cancel := context.WithCancel(context.Background())
	return &Runner{
		exec:    cfg.Executor,
		store:   cfg.Store,
		onDone:  cfg.OnDone,
		timeout: cfg.Timeout,
		logs:    cfg.Logs,
		queue:   make(chan *Job, cfg.QueueSize),
		base:    base,
		cancel:  cancel,
	}
}

// Start spawns the single worker goroutine.
//
// The heartbeat is stamped before the goroutine is spawned, not only inside it:
// a probe arriving in the window between the two would otherwise read a worker
// that has not reported, which is the opposite of what just happened.
func (r *Runner) Start() {
	r.beat(time.Now())
	r.wg.Add(1)
	go r.loop()
}

// Submit records a job and queues it, returning as soon as it is accepted.
//
// ctx bounds only the enqueue, not the work: the job runs under the Runner's
// own lifetime context. That separation is the point — the caller's request
// ends in milliseconds while an operation runs for minutes.
func (r *Runner) Submit(ctx context.Context, qubeID, qubeName string, action Action) (*Job, error) {
	// Check shutdown before recording anything, and serialize it with enqueue.
	r.closeMu.RLock()
	defer r.closeMu.RUnlock()
	if r.closing {
		return nil, ErrRunnerClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	job := &Job{
		ID:         uuid.NewString(),
		QubeID:     qubeID,
		QubeName:   qubeName,
		Action:     action,
		State:      JobQueued,
		EnqueuedAt: time.Now().UTC(),
	}
	if r.store != nil {
		if err := r.store.Insert(ctx, job); err != nil {
			return nil, fmt.Errorf("record job: %w", err)
		}
	}

	select {
	case r.queue <- job:
		return job, nil
	default:
		// Fail fast rather than block the HTTP handler behind a full queue.
		job.State = JobFailed
		job.Error = ErrQueueFull.Error()
		now := time.Now().UTC()
		job.FinishedAt = &now
		if r.store != nil {
			if err := r.store.Update(ctx, job); err != nil {
				return nil, errors.Join(ErrQueueFull, fmt.Errorf("record rejected job: %w", err))
			}
		}
		return nil, ErrQueueFull
	}
}

// loop is the single worker. Everything it runs is serialized by construction.
//
// It selects on a ticker instead of ranging over the queue so that WAITING for
// work is itself observable. A worker parked forever on an empty queue and a
// worker that has exited look identical from outside — jobs never run while the
// process keeps answering HTTP — and that is the failure /health has to see. A
// closed queue still yields its buffered jobs before the receive reports closed,
// so Shutdown drains what is already accepted exactly as it did with range, and
// cancellation of base remains the escape hatch for a shutdown that runs out of
// patience.
func (r *Runner) loop() {
	defer r.wg.Done()
	ticker := time.NewTicker(DispatcherPollInterval)
	defer ticker.Stop()
	for {
		select {
		case job, ok := <-r.queue:
			if !ok {
				return
			}
			r.runJob(job)
			r.beat(time.Now())
		case <-ticker.C:
			r.beat(time.Now())
		}
	}
}

// runJob executes one job and keeps the queue state /health reports in step with
// it: while this runs the worker cannot poll, which is why the health budget
// allows for a job in flight.
func (r *Runner) runJob(job *Job) {
	r.running.Add(1)
	defer r.running.Add(-1)
	r.run(job)
}

// run executes one job and records its outcome.
func (r *Runner) run(job *Job) {
	started := time.Now().UTC()
	job.State = JobRunning
	job.StartedAt = &started
	if r.store != nil {
		if err := r.store.Update(r.base, job); err != nil {
			r.failBeforeExecution(job, err)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.base, r.timeout)
	defer cancel()

	// Capture the operation's output for this job. Failing to open the log must
	// not stop the job: not being able to watch an operation is a worse outcome
	// than not being able to watch it AND not running it.
	if r.logs != nil {
		if f, lerr := r.logs.Create(job.ID); lerr == nil {
			defer func() { _ = f.Close() }()
			ctx = WithLogSink(ctx, f)
		} else {
			log.Printf("orchestrator: job %s: no log (%v)", job.ID, lerr)
		}
	}

	var err error
	switch job.Action {
	case ActionProvision:
		err = r.exec.Provision(ctx, job.QubeName)
	case ActionResume:
		err = r.exec.Resume(ctx, job.QubeName)
	case ActionSuspend, ActionRelease:
		err = r.exec.Suspend(ctx, job.QubeName)
	case ActionDestroy:
		err = r.exec.Destroy(ctx, job.QubeName)
	default:
		err = fmt.Errorf("unknown action %q", job.Action)
	}

	if err == nil {
		err = ctx.Err()
	}
	r.finish(job, started, err)
}

// finish includes cleanup in the job outcome before persisting a terminal state.
func (r *Runner) finish(job *Job, started time.Time, err error) {
	finished := time.Now().UTC()
	job.FinishedAt = &finished
	job.State = JobSucceeded
	if err != nil {
		job.State, job.Error = JobFailed, err.Error()
	}
	// Cleanup is part of the result, not a best-effort action after success.
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer finalCancel()
	if r.onDone != nil {
		if err := r.onDone(finalCtx, job); err != nil {
			job.State = JobFailed
			if job.Error != "" {
				job.Error += "; "
			}
			job.Error += "completion: " + err.Error()
		}
	}
	if r.store != nil {
		if err := r.store.Update(finalCtx, job); err != nil {
			log.Printf("orchestrator: job %s outcome not persisted: %v", job.ID, err)
			return
		}
	}
	log.Printf("orchestrator: job %s (%s %s) %s after %s: %s",
		job.ID, job.Action, job.QubeName, job.State, finished.Sub(started).Round(time.Second), job.Error)
}

// Shutdown stops accepting work and waits for the in-flight job, up to the
// given grace period.
//
// Canceling the base context lets the in-flight operation stop cleanly rather
// than being killed mid-way. Cutting that short is what strands infrastructure,
// so prefer a grace period longer than a typical operation.
func (r *Runner) Shutdown(grace time.Duration) {
	r.stop.Do(func() {
		// Stop accepting, then close the queue so the worker drains and exits.
		r.closeMu.Lock()
		r.closing = true
		close(r.queue)
		r.closeMu.Unlock()

		done := make(chan struct{})
		go func() {
			r.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Drained cleanly; nothing is running, so canceling is free.
			r.cancel()
		case <-time.After(grace):
			log.Printf("orchestrator: shutdown grace of %s elapsed with a job still running; "+
				"aborting the in-flight operation", grace)
			r.cancel()
			<-done
		}
	})
}

// failBeforeExecution uses a fresh deadline so canceled shutdown/request work
// cannot prevent recording a failure when the database is still writable.
func (r *Runner) failBeforeExecution(job *Job, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := time.Now().UTC()
	job.State, job.FinishedAt = JobFailed, &finished
	job.Error = "not executed: recording start failed: " + cause.Error()
	if r.onDone != nil {
		if err := r.onDone(ctx, job); err != nil {
			job.Error += "; completion: " + err.Error()
		}
	}
	if err := r.store.Update(ctx, job); err != nil {
		log.Printf("orchestrator: job %s failure not persisted: %v", job.ID, err)
	}
	log.Printf("orchestrator: job %s %s", job.ID, job.Error)
}
