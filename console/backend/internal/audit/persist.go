package audit

import (
	"context"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Persisted-trail defaults. They are compile-time values, registered in
// docs/runtime-defaults.md (UD-24 family); PersisterConfig overrides them only
// for tests.
const (
	// DefaultQueueSize bounds the events waiting for the store. It only fills
	// when the store has stalled; an event that finds it full is dropped and
	// counted, never waited for.
	DefaultQueueSize = 1024
	// DefaultWriteTimeout bounds one store write.
	DefaultWriteTimeout = 5 * time.Second
	// DefaultStopGrace bounds how long Stop spends writing what is still
	// queued; whatever is left after it is dropped and counted.
	DefaultStopGrace = 5 * time.Second
	// DefaultSampledBurst and DefaultSampledEvery are the unauthenticated
	// budget: up to 20 sampled events at once, then one per 10 seconds
	// (8,640 a day at most, plus at most one summary row per flush).
	DefaultSampledBurst = 20
	DefaultSampledEvery = 10 * time.Second
	// DefaultFlushInterval is how often a pending suppression summary is
	// written, so a summary row is at most this late.
	DefaultFlushInterval = time.Minute
	// DefaultFailureLogEvery coalesces persistence-failure log lines: the
	// first failure is logged at once, later ones at most this often, with a
	// count.
	DefaultFailureLogEvery = time.Minute
)

// OutcomeSuppressed is the outcome of a summary row: it stands for sampled
// events the budget did not store one by one. It is never an HTTP outcome.
const OutcomeSuppressed = "suppressed"

// Store is where a Persister writes. Only the Persister's writer goroutine
// calls it, one call at a time, each under a deadline.
type Store interface {
	AppendEvent(ctx context.Context, ev Event) error
	AppendSuppression(ctx context.Context, s Suppression) error
}

// Budget is a token bucket: Burst tokens, one more every Every.
type Budget struct {
	Burst int
	Every time.Duration
}

// PersisterConfig tunes a Persister. Zero values take the defaults above.
type PersisterConfig struct {
	QueueSize       int
	WriteTimeout    time.Duration
	StopGrace       time.Duration
	FlushInterval   time.Duration
	FailureLogEvery time.Duration
	// Budget admits ClassSampled events.
	Budget Budget
	// Now is the clock for the budget and the log coalescing.
	Now func() time.Time
	// Logf reports failures, recoveries and suppression summaries.
	Logf func(format string, args ...any)
}

func (c PersisterConfig) withDefaults() PersisterConfig {
	if c.QueueSize <= 0 {
		c.QueueSize = DefaultQueueSize
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = DefaultWriteTimeout
	}
	if c.StopGrace <= 0 {
		c.StopGrace = DefaultStopGrace
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = DefaultFlushInterval
	}
	if c.FailureLogEvery <= 0 {
		c.FailureLogEvery = DefaultFailureLogEvery
	}
	if c.Budget.Burst <= 0 {
		c.Budget.Burst = DefaultSampledBurst
	}
	if c.Budget.Every <= 0 {
		c.Budget.Every = DefaultSampledEvery
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	return c
}

// Stats is a Persister's running account. Every event handed to Submit is
// counted once it is settled: Persisted, Suppressed (summarized), Dropped, or
// Failed when its write failed. Failed also counts failed summary rows.
type Stats struct {
	Persisted  uint64
	Suppressed uint64
	// Dropped counts events that never reached the store: the queue was full,
	// the persister was stopped, or the shutdown grace ran out.
	Dropped uint64
	// Failed counts store writes (events and summary rows) that returned an
	// error or ran past the write timeout.
	Failed uint64
	// Degraded is true from a failed write or a dropped event until the next
	// successful write.
	Degraded bool
}

// Persister writes audit events to a Store off the request path.
//
// Submit never blocks: it applies the unauthenticated budget and puts the
// event on a bounded queue that one writer goroutine drains. A store that
// fails or stalls therefore never changes or delays a response; it shows up as
// Stats (Failed, Dropped, Degraded) and as a coalesced log line, while every
// event keeps its JSON log line, which Recorder writes before calling Submit.
//
// The budget is what keeps an unauthenticated flood from filling the table:
// ClassSampled events are stored while tokens last and otherwise counted into
// a summary row written every FlushInterval. ClassFull events are never
// budgeted. The store bounds what is kept (see repository.AuditRepository);
// the budget bounds how fast the sampled part of it can churn.
type Persister struct {
	store Store
	cfg   PersisterConfig
	queue chan Event

	// gate is held shared by Submit and exclusively by Stop to close the
	// door, so no event can be queued after the writer's final drain.
	gate   sync.RWMutex
	closed bool

	// mu guards the budget and the pending summary.
	mu      sync.Mutex
	tokens  float64
	refill  time.Time
	pending suppressionWindow

	logMu      sync.Mutex
	lastLog    time.Time
	unreported uint64

	persisted  atomic.Uint64
	suppressed atomic.Uint64
	dropped    atomic.Uint64
	failed     atomic.Uint64
	degraded   atomic.Bool

	started   bool
	stop      chan struct{}
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

// NewPersister builds a Persister over store. Nothing is written until Start.
func NewPersister(store Store, cfg PersisterConfig) *Persister {
	cfg = cfg.withDefaults()
	return &Persister{
		store:  store,
		cfg:    cfg,
		queue:  make(chan Event, cfg.QueueSize),
		tokens: float64(cfg.Budget.Burst),
		refill: cfg.Now(),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start launches the writer. Calling it again, or after Stop, does nothing.
func (p *Persister) Start() {
	p.startOnce.Do(func() {
		p.gate.Lock()
		defer p.gate.Unlock()
		if p.closed {
			return
		}
		p.started = true
		ticker := time.NewTicker(p.cfg.FlushInterval)
		go p.run(ticker)
	})
}

// Stop closes the door to new events, writes what is queued and any pending
// summary within StopGrace, and returns when the writer has exited. It waits
// at most for the write already in flight (WriteTimeout) plus StopGrace. Call
// it before the store's database is closed. It is safe to call more than once,
// and without Start (queued events are then dropped and counted).
func (p *Persister) Stop() {
	p.stopOnce.Do(func() {
		p.gate.Lock()
		p.closed = true
		started := p.started
		p.gate.Unlock()
		if !started {
			p.dropQueued("the persister was never started")
			return
		}
		close(p.stop)
		<-p.done
	})
}

// Submit queues ev for the store, subject to the budget. It never blocks.
func (p *Persister) Submit(ev Event) {
	p.gate.RLock()
	defer p.gate.RUnlock()
	if p.closed {
		p.lose(ev.RequestID, "the persister is stopped")
		return
	}
	if ev.Class() == ClassSampled && !p.admit(ev) {
		return
	}
	select {
	case p.queue <- ev:
	default:
		p.lose(ev.RequestID, "the write queue is full")
	}
}

// Stats returns the running account.
func (p *Persister) Stats() Stats {
	return Stats{
		Persisted:  p.persisted.Load(),
		Suppressed: p.suppressed.Load(),
		Dropped:    p.dropped.Load(),
		Failed:     p.failed.Load(),
		Degraded:   p.degraded.Load(),
	}
}

// admit takes a budget token for a sampled event, or counts it into the
// pending summary.
func (p *Persister) admit(ev Event) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if now := p.cfg.Now(); now.After(p.refill) {
		earned := float64(now.Sub(p.refill)) / float64(p.cfg.Budget.Every)
		p.tokens = math.Min(float64(p.cfg.Budget.Burst), p.tokens+earned)
		p.refill = now
	}
	if p.tokens >= 1 {
		p.tokens--
		return true
	}
	p.pending.add(ev)
	p.suppressed.Add(1)
	return false
}

// run is the writer. Stop is checked first on every turn: select picks at
// random among ready cases, so without that a full queue could keep winning
// and write one event after another, each under the full WriteTimeout, after
// Stop was called. Once Stop is seen, finish takes over under StopGrace.
func (p *Persister) run(ticker *time.Ticker) {
	defer close(p.done)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			p.finish()
			return
		default:
		}
		select {
		case ev := <-p.queue:
			p.writeEvent(context.Background(), ev)
		case <-ticker.C:
			p.flush(context.Background())
		case <-p.stop:
			p.finish()
			return
		}
	}
}

// finish writes what Stop found queued, then the pending summary, all within
// StopGrace. Once the grace is spent the rest is dropped rather than waited
// for: shutdown must not hang on a stalled store.
func (p *Persister) finish() {
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.StopGrace)
	defer cancel()
	for {
		select {
		case ev := <-p.queue:
			if ctx.Err() != nil {
				p.lose(ev.RequestID, "the shutdown grace ran out")
				continue
			}
			p.writeEvent(ctx, ev)
		default:
			p.flush(ctx)
			return
		}
	}
}

// dropQueued empties the queue without a writer, counting every event.
func (p *Persister) dropQueued(reason string) {
	for {
		select {
		case ev := <-p.queue:
			p.lose(ev.RequestID, reason)
		default:
			return
		}
	}
}

func (p *Persister) writeEvent(parent context.Context, ev Event) {
	if p.write(parent, "request_id="+ev.RequestID, func(ctx context.Context) error {
		return p.store.AppendEvent(ctx, ev)
	}) {
		p.persisted.Add(1)
	}
}

// flush writes the pending summary, if any.
func (p *Persister) flush(parent context.Context) {
	p.mu.Lock()
	s, ok := p.pending.take()
	p.mu.Unlock()
	if !ok {
		return
	}
	p.cfg.Logf("audit: %d throttled or unauthenticated request(s) that did not succeed, between %s and %s, "+
		"from %d source prefix(es) (%s), were logged but not stored one by one (persisted-trail budget); "+
		"one summary row stands for them",
		s.Count, s.First.UTC().Format(time.RFC3339Nano), s.Last.UTC().Format(time.RFC3339Nano),
		s.Sources, s.SourcesText())
	p.write(parent, "suppression summary", func(ctx context.Context) error {
		return p.store.AppendSuppression(ctx, s)
	})
}

// write runs one store call under the write timeout and accounts for it.
func (p *Persister) write(parent context.Context, what string, call func(context.Context) error) bool {
	ctx, cancel := context.WithTimeout(parent, p.cfg.WriteTimeout)
	err := call(ctx)
	cancel()
	if err != nil {
		// The counter moves last, so whoever sees it has already been
		// given the degraded flag and the log line.
		p.degraded.Store(true)
		p.report(what, err.Error())
		p.failed.Add(1)
		return false
	}
	if p.degraded.CompareAndSwap(true, false) {
		p.logMu.Lock()
		p.cfg.Logf("audit: persisting audit events again (%d loss(es) since the last report)", p.unreported)
		p.unreported = 0
		p.logMu.Unlock()
	}
	return true
}

// lose accounts for an event that will never reach the store.
func (p *Persister) lose(requestID, reason string) {
	p.degraded.Store(true)
	p.report("request_id="+requestID, reason)
	p.dropped.Add(1)
}

// report logs a loss at once if none was logged within FailureLogEvery, and
// otherwise only counts it for the next line. A store that fails on every
// write must not turn the audit trail's failure into a log flood of its own.
func (p *Persister) report(what, reason string) {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	p.unreported++
	now := p.cfg.Now()
	if !p.lastLog.IsZero() && now.Sub(p.lastLog) < p.cfg.FailureLogEvery {
		return
	}
	p.cfg.Logf("audit: %d audit write(s) lost since the last report (latest: %s: %s); "+
		"each event's JSON log line was still written", p.unreported, what, reason)
	p.unreported = 0
	p.lastLog = now
}
