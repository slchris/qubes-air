package service

import (
	"context"
	"log"
	"sync"
	"time"
)

// Audit trail retention defaults (docs/runtime-defaults.md UD-24).
const (
	// DefaultAuditRetention is how long a persisted audit row is kept.
	DefaultAuditRetention = 90 * 24 * time.Hour
	// DefaultAuditPruneInterval is how often expired rows are removed, so a
	// row outlives the window by at most this much.
	DefaultAuditPruneInterval = time.Hour
	// DefaultAuditPruneTimeout bounds one prune run; a backlog it does not
	// finish is picked up by the next run.
	DefaultAuditPruneTimeout = 30 * time.Second
)

// AuditPruner deletes audit rows older than a cutoff (strictly older: a row
// exactly at the cutoff stays) and reports how many it deleted.
type AuditPruner interface {
	PruneBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// AuditRetentionConfig tunes an AuditRetention. Zero values take the
// defaults above.
type AuditRetentionConfig struct {
	MaxAge   time.Duration
	Interval time.Duration
	Timeout  time.Duration
	Now      func() time.Time
	Logf     func(format string, args ...any)
}

// AuditRetention removes persisted audit rows once they are older than the
// retention window: once at Start, then every Interval. It is the age half of
// the trail's bound; the row caps are enforced on insert by the repository.
//
// Stop cancels a prune in flight and waits for the loop to exit, so it must be
// called before the database closes.
type AuditRetention struct {
	pruner AuditPruner
	cfg    AuditRetentionConfig

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	started bool
	stopped bool
}

// NewAuditRetention builds a retention job over pruner. Nothing runs until
// Start.
func NewAuditRetention(pruner AuditPruner, cfg AuditRetentionConfig) *AuditRetention {
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = DefaultAuditRetention
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultAuditPruneInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultAuditPruneTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &AuditRetention{pruner: pruner, cfg: cfg, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// Start launches the loop. It does nothing if called again or after Stop.
func (r *AuditRetention) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return
	}
	r.started = true
	go r.loop(time.NewTicker(r.cfg.Interval))
}

// Stop cancels any prune in flight and returns once the loop has exited. It is
// safe to call more than once and without Start.
func (r *AuditRetention) Stop() {
	r.mu.Lock()
	started, stopped := r.started, r.stopped
	r.stopped = true
	r.mu.Unlock()
	if stopped {
		return
	}
	r.cancel()
	if started {
		<-r.done
	}
}

// Prune removes the rows older than the retention window, measured from now.
func (r *AuditRetention) Prune(ctx context.Context) (int64, error) {
	return r.pruner.PruneBefore(ctx, r.cfg.Now().Add(-r.cfg.MaxAge))
}

func (r *AuditRetention) loop(ticker *time.Ticker) {
	defer close(r.done)
	defer ticker.Stop()
	r.pruneOnce()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.pruneOnce()
		}
	}
}

// pruneOnce runs one bounded prune and logs what it did. A failure is logged
// and retried on the next tick: the row caps still bound the table meanwhile.
func (r *AuditRetention) pruneOnce() {
	ctx, cancel := context.WithTimeout(r.ctx, r.cfg.Timeout)
	defer cancel()
	removed, err := r.Prune(ctx)
	if err != nil {
		if r.ctx.Err() == nil {
			r.cfg.Logf("audit: retention prune failed after removing %d row(s): %v", removed, err)
		}
		return
	}
	if removed > 0 {
		r.cfg.Logf("audit: retention removed %d row(s) older than %s", removed, r.cfg.MaxAge)
	}
}
