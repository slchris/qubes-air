package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
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
	// ActionRelease performs the same terraform work as ActionSuspend — destroy
	// the compute VM, keep the data disk — but records a different intent: the
	// user deleted the qube rather than parking it. They are distinguished so
	// job history reads truthfully and so the completion hook can land the qube
	// on "released" rather than "suspended".
	ActionRelease Action = "release"
)

// JobState is the lifecycle of a single terraform invocation.
type JobState string

// Job states.
const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
)

// Job is one terraform invocation. It outlives the HTTP request that asked for
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
// asynchronously, nothing else is still around to do it when terraform
// finishes.
type Completion func(ctx context.Context, j *Job)

// Runner errors.
var (
	ErrQueueFull    = errors.New("orchestration queue is full")
	ErrRunnerClosed = errors.New("orchestration runner is shutting down")
)

// Runner serializes every terraform invocation onto a single worker goroutine.
//
// One worker is the mutual exclusion: terraform's local backend takes a
// non-blocking fcntl lock on the state file, so a second concurrent process
// does not wait its turn — it fails outright with "Error acquiring the state
// lock". A mutex would also serialize, but it cannot be cancelled, gives no
// backpressure, and offers no way to report what is happening; with operations
// measured in minutes those matter. A queue additionally guarantees submission
// order, so a Stop immediately followed by a Start cannot execute in reverse.
type Runner struct {
	exec    Executor
	store   JobStore
	onDone  Completion
	timeout time.Duration
	// logs captures each job's terraform output. Nil disables it, which costs
	// visibility into a running apply but never blocks one.
	logs *JobLogStore

	queue chan *Job

	// base is the lifetime context for all terraform work. It is deliberately
	// derived from context.Background() and never from an HTTP request: a
	// client disconnect must not signal terraform away mid-apply, because a
	// half-applied run can leave VMs and disks that terraform has no record of.
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
	// Logs captures each job's terraform output as it is produced, so a running
	// apply can be watched rather than only reported on once it ends. Optional.
	Logs *JobLogStore
}

// DefaultQueueSize bounds how many operations may be waiting. Past this,
// Submit reports ErrQueueFull rather than growing without limit.
const DefaultQueueSize = 64

// NewRunner builds a Runner. Call Start to spawn the worker.
func NewRunner(cfg RunnerConfig) *Runner {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultQueueSize
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
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
func (r *Runner) Start() {
	r.wg.Add(1)
	go r.loop()
}

// Submit records a job and queues it, returning as soon as it is accepted.
//
// ctx bounds only the enqueue, not the work: the job runs under the Runner's
// own lifetime context. That separation is the point — the caller's request
// ends in milliseconds while terraform runs for minutes.
func (r *Runner) Submit(ctx context.Context, qubeID, qubeName string, action Action) (*Job, error) {
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

	// Guard the send: once Shutdown closes the queue, sending would panic.
	// Holding the read lock for the send is what makes close-vs-send safe.
	r.closeMu.RLock()
	defer r.closeMu.RUnlock()
	if r.closing {
		return nil, ErrRunnerClosed
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
			_ = r.store.Update(ctx, job)
		}
		return nil, ErrQueueFull
	}
}

// loop is the single worker. Everything it runs is serialized by construction.
func (r *Runner) loop() {
	defer r.wg.Done()
	// Ranging (rather than selecting on base.Done) means Shutdown can close the
	// queue and have the worker drain what is already accepted before exiting.
	// Cancellation of base remains the escape hatch for a shutdown that runs
	// out of patience, and it reaches terraform as a signal, not a kill.
	for job := range r.queue {
		r.run(job)
	}
}

// run executes one job and records its outcome.
func (r *Runner) run(job *Job) {
	started := time.Now().UTC()
	job.State = JobRunning
	job.StartedAt = &started
	if r.store != nil {
		_ = r.store.Update(r.base, job)
	}

	ctx, cancel := context.WithTimeout(r.base, r.timeout)
	defer cancel()

	// Capture terraform's output for this job. Failing to open the log must not
	// stop the job: not being able to watch an apply is a worse outcome than
	// not being able to watch it AND not running it.
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

	finished := time.Now().UTC()
	job.FinishedAt = &finished
	if err != nil {
		job.State = JobFailed
		job.Error = err.Error()
		log.Printf("orchestrator: job %s (%s %s) failed after %s: %v",
			job.ID, job.Action, job.QubeName, finished.Sub(started).Round(time.Second), err)
	} else {
		job.State = JobSucceeded
		log.Printf("orchestrator: job %s (%s %s) succeeded in %s",
			job.ID, job.Action, job.QubeName, finished.Sub(started).Round(time.Second))
	}
	if r.store != nil {
		_ = r.store.Update(r.base, job)
	}

	// The completion hook writes the qube's terminal status. It runs even on
	// failure — a qube left in a transient status would be permanently "busy".
	if r.onDone != nil {
		r.onDone(r.base, job)
	}
}

// Shutdown stops accepting work and waits for the in-flight job, up to the
// given grace period.
//
// Cancelling the base context signals terraform (SIGINT, not SIGKILL) so it can
// finish its current operation and persist state. Cutting that short is what
// strands infrastructure, so prefer a grace period longer than a typical apply.
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
			// Drained cleanly; nothing is running, so cancelling is free.
			r.cancel()
		case <-time.After(grace):
			log.Printf("orchestrator: shutdown grace of %s elapsed with a job still running; "+
				"signalling terraform to stop", grace)
			r.cancel()
			<-done
		}
	})
}
