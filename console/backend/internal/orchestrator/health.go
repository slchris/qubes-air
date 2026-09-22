package orchestrator

import "time"

// DispatcherPollInterval is how often an idle dispatcher refreshes the heartbeat
// that /health reads.
//
// It is a poll only in the sense that it bounds how long a dead worker can look
// alive: the worker still wakes the instant a job arrives, so this adds no
// latency to any operation.
const DispatcherPollInterval = 5 * time.Second

// DispatcherStaleAfter is how long a heartbeat may go unrefreshed before the
// dispatcher is presumed gone.
//
// Expressed as missed polls rather than a fixed duration: the bound has to
// follow the poll interval, and "three missed polls" is the smallest rule that
// cannot trip on a single late tick — one missed poll is scheduler jitter, not a
// dead worker. The consumer is docker-compose's healthcheck (interval 5s,
// retries 20), which turns a red /health into a restart, so the bound only has
// to be far below that; it is 15 seconds.
const DispatcherStaleAfter = 3 * DispatcherPollInterval

// RunnerHealth is the worker snapshot GET /health reports.
//
// Every field here is read without a lock or a query: the heartbeat is one
// atomic load, the queue depth is the channel's own length, and neither touches
// the database. A health check that costs a query per poll would be its own
// availability problem.
type RunnerHealth struct {
	// Enabled is false when there is no runner at all — orchestration disabled.
	// DispatcherAlive then means nothing, and callers must not read it as a
	// failure: with orchestration off, operations run inline by design.
	Enabled bool
	// DispatcherAlive is false when the worker has not refreshed its heartbeat
	// within its budget (see Runner.dispatcherAlive). A dead or wedged worker
	// means queued jobs never run while the process still answers HTTP, which is
	// exactly the outage a liveness probe must be able to see.
	DispatcherAlive bool
	// LastBeat is when the dispatcher last reported for duty, zero if it never
	// has (never started).
	LastBeat time.Time
	// Queued is how many jobs are waiting for the worker. Informational: with a
	// finite worker pool a legitimately queued job can wait a long time, so a
	// large count is not by itself a failure.
	Queued int
	// Running is how many jobs the worker is executing.
	Running int
}

// Health returns the worker's liveness and queue depth now.
//
// Nil-safe, and deliberately not a panic: a nil Runner is what the server holds
// when orchestration is disabled, and a health endpoint that crashes the process
// it is meant to report on is worse than no endpoint.
func (r *Runner) Health() RunnerHealth {
	return r.HealthAt(time.Now())
}

// HealthAt is Health evaluated at an injected instant, which is what makes the
// staleness rule testable without sleeping.
func (r *Runner) HealthAt(now time.Time) RunnerHealth {
	if r == nil {
		return RunnerHealth{}
	}
	last := r.lastBeat()
	return RunnerHealth{
		Enabled:         true,
		DispatcherAlive: r.dispatcherAlive(now, last),
		LastBeat:        last,
		Queued:          len(r.queue),
		Running:         int(r.running.Load()),
	}
}

// dispatcherAlive decides whether the worker goroutine is still doing its job.
//
// The rule differs by what the worker is doing, because "not polling" means two
// different things:
//
//   - Idle, it polls every DispatcherPollInterval, so a heartbeat older than
//     DispatcherStaleAfter means the loop is not running at all — it exited, or
//     it is blocked somewhere no poll can reach.
//   - Inside a job, it CANNOT poll: the job runs on the same goroutine. A
//     legitimate provision takes 15-25 minutes (see DefaultJobTimeout), so the
//     budget becomes the job's own configured timeout plus the same staleness
//     margin. That is the bound the runner already puts on the work, not a new
//     rule, and past it the job is being canceled on a context deadline anyway.
//
// A job merely WAITING in the queue is never a failure. One worker means a long
// legitimate job makes every other job wait, and a false "unhealthy" restarts a
// console that is working exactly as designed — worse than the missing signal.
//
// A dispatcher that has exited reports dead once its last heartbeat ages out;
// Shutdown closes the queue (so the loop returns) without clearing the stamp.
func (r *Runner) dispatcherAlive(now, last time.Time) bool {
	if last.IsZero() {
		return false
	}
	budget := DispatcherStaleAfter
	if r.running.Load() > 0 {
		budget += r.timeout
	}
	return now.Sub(last) <= budget
}

// beat records that the dispatcher is alive at the given instant.
func (r *Runner) beat(at time.Time) { r.beatNs.Store(at.UnixNano()) }

// lastBeat returns the recorded heartbeat, or the zero time if the dispatcher
// never reported for duty.
func (r *Runner) lastBeat() time.Time {
	ns := r.beatNs.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}
