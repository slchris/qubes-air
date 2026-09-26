package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore records what the persister writes. err makes every write fail;
// hold makes AppendEvent wait until released or its deadline passes, and
// signals entered first so a test knows a write is in flight.
type fakeStore struct {
	mu        sync.Mutex
	events    []Event
	summaries []Suppression
	err       error
	hold      chan struct{}
	entered   chan struct{}
}

func (s *fakeStore) AppendEvent(ctx context.Context, ev Event) error {
	s.mu.Lock()
	hold, entered, err := s.hold, s.entered, s.err
	s.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

func (s *fakeStore) AppendSuppression(_ context.Context, sup Suppression) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.summaries = append(s.summaries, sup)
	return nil
}

func (s *fakeStore) snapshot() ([]Event, []Suppression) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...), append([]Suppression(nil), s.summaries...)
}

func (s *fakeStore) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// fakeClock is a settable clock for the budget and the log coalescing.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// logCapture collects Logf output.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logCapture) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *logCapture) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

var clockStart = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// newTestPersister wires a persister to a fake store, clock and log. tune
// adjusts the config before it is built.
func newTestPersister(t *testing.T, tune func(*PersisterConfig)) (*Persister, *fakeStore, *fakeClock, *logCapture) {
	t.Helper()
	store := &fakeStore{}
	clock := &fakeClock{t: clockStart}
	logs := &logCapture{}
	cfg := PersisterConfig{Budget: Budget{Burst: 5, Every: 10 * time.Second}, Now: clock.Now, Logf: logs.Logf}
	if tune != nil {
		tune(&cfg)
	}
	p := NewPersister(store, cfg)
	t.Cleanup(p.Stop)
	return p, store, clock, logs
}

func anonymousDenied(i int) Event {
	return Event{Time: clockStart.Add(time.Duration(i) * time.Millisecond), RequestID: fmt.Sprintf("anon-%d", i),
		Subject: AnonymousSubject, Status: 401, Outcome: OutcomeDenied, ZoneScope: "none"}
}

func operatorAction(i int) Event {
	return Event{Time: clockStart.Add(time.Duration(i) * time.Millisecond), RequestID: fmt.Sprintf("op-%d", i),
		Authenticated: true, Subject: "operator", Status: 403, Outcome: OutcomeDenied, ZoneScope: "fleet"}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// An unauthenticated flood is stored up to the burst and summarized after it:
// one summary row with the exact count and the events' own time span.
func TestPersisterBudgetsSampledEvents(t *testing.T) {
	p, store, _, logs := newTestPersister(t, nil)
	p.Start()

	for i := range 100 {
		p.Submit(anonymousDenied(i))
	}
	p.Stop()

	events, summaries := store.snapshot()
	if len(events) != 5 {
		t.Fatalf("stored %d sampled events, want the burst of 5", len(events))
	}
	for i, ev := range events {
		if ev.RequestID != fmt.Sprintf("anon-%d", i) {
			t.Errorf("stored %s at %d: the budget must admit the first events", ev.RequestID, i)
		}
	}
	want := Suppression{First: anonymousDenied(5).Time, Last: anonymousDenied(99).Time, Count: 95}
	if len(summaries) != 1 || summaries[0] != want {
		t.Fatalf("summaries = %+v, want exactly %+v", summaries, want)
	}
	if st := p.Stats(); st.Persisted != 5 || st.Suppressed != 95 || st.Dropped != 0 || st.Failed != 0 {
		t.Errorf("stats = %+v, want 5 persisted and 95 suppressed", st)
	}
	if !strings.Contains(logs.all(), "95 unauthenticated request(s)") {
		t.Errorf("a summary must also be logged, got: %s", logs.all())
	}
}

// The bucket refills at one token per Every and never beyond the burst, so a
// long quiet spell does not bank an unbounded allowance.
func TestPersisterBudgetRefillsUpToTheBurst(t *testing.T) {
	p, store, clock, _ := newTestPersister(t, func(c *PersisterConfig) {
		c.Budget = Budget{Burst: 2, Every: 10 * time.Second}
	})
	p.Start()
	stored := func() int {
		p.Stop()
		events, _ := store.snapshot()
		return len(events)
	}

	for i := range 3 {
		p.Submit(anonymousDenied(i)) // 2 admitted, 1 suppressed
	}
	clock.Advance(9 * time.Second)
	p.Submit(anonymousDenied(3)) // 0.9 of a token: suppressed
	clock.Advance(time.Second)
	p.Submit(anonymousDenied(4)) // one token earned: admitted
	clock.Advance(time.Hour)
	for i := 5; i < 10; i++ {
		p.Submit(anonymousDenied(i)) // an hour earns the burst (2), not 360
	}

	if got := stored(); got != 5 {
		t.Errorf("stored %d events, want 2 + 1 + 2", got)
	}
	if st := p.Stats(); st.Suppressed != 5 {
		t.Errorf("suppressed %d, want 5", st.Suppressed)
	}
}

// Authenticated events and unauthenticated successes are never budgeted, even
// while an anonymous flood has emptied the bucket.
func TestPersisterNeverBudgetsFullEvents(t *testing.T) {
	p, store, _, _ := newTestPersister(t, nil)
	p.Start()

	for i := range 50 {
		p.Submit(anonymousDenied(i))
	}
	for i := range 50 {
		p.Submit(operatorAction(i))
	}
	login := Event{RequestID: "login", Subject: AnonymousSubject, Status: 200, Outcome: OutcomeSuccess, ZoneScope: "none"}
	p.Submit(login)
	p.Stop()

	events, _ := store.snapshot()
	full := 0
	for _, ev := range events {
		if ev.Class() == ClassFull {
			full++
		}
	}
	if full != 51 {
		t.Errorf("stored %d full-class events, want all 51", full)
	}
}

// A stalled store never blocks Submit: the event goes to the queue, and once
// the queue is full the rest are dropped, counted and logged. When the store
// recovers, what was queued is written and the persister reports recovery.
func TestPersisterDoesNotBlockOnAStalledStore(t *testing.T) {
	p, store, _, logs := newTestPersister(t, func(c *PersisterConfig) {
		c.QueueSize = 2
		c.WriteTimeout = time.Minute
	})
	store.hold = make(chan struct{})
	store.entered = make(chan struct{}, 16)
	p.Start()

	p.Submit(operatorAction(0))
	<-store.entered // the writer is now stuck inside the store with event 0
	for i := 1; i <= 5; i++ {
		p.Submit(operatorAction(i)) // returns at once: 1-2 queue, 3-5 dropped
	}

	st := p.Stats()
	if st.Dropped != 3 || !st.Degraded {
		t.Fatalf("stats = %+v, want 3 dropped and degraded", st)
	}
	if !strings.Contains(logs.all(), "the write queue is full") {
		t.Errorf("a dropped event must be logged, got: %s", logs.all())
	}

	close(store.hold)
	p.Stop()
	events, _ := store.snapshot()
	if len(events) != 3 {
		t.Errorf("stored %d events after recovery, want the in-flight one and the 2 queued", len(events))
	}
	if st := p.Stats(); st.Degraded || st.Persisted != 3 {
		t.Errorf("stats after recovery = %+v, want 3 persisted and not degraded", st)
	}
	if !strings.Contains(logs.all(), "persisting audit events again") {
		t.Errorf("recovery must be logged, got: %s", logs.all())
	}
}

// A store error is counted, flags the persister degraded and is logged with
// the request ID; the next successful write clears the flag.
func TestPersisterReportsStoreFailures(t *testing.T) {
	p, store, _, logs := newTestPersister(t, nil)
	store.setErr(errors.New("disk I/O error"))
	p.Start()

	p.Submit(operatorAction(1))
	waitFor(t, "the failed write", func() bool { return p.Stats().Failed == 1 })
	if !p.Stats().Degraded {
		t.Error("a failed write must mark the persister degraded")
	}
	out := logs.all()
	if !strings.Contains(out, "request_id=op-1") || !strings.Contains(out, "disk I/O error") {
		t.Errorf("the failure log must name the request and the error, got: %s", out)
	}

	store.setErr(nil)
	p.Submit(operatorAction(2))
	waitFor(t, "the next write", func() bool { return p.Stats().Persisted == 1 })
	if p.Stats().Degraded {
		t.Error("a successful write must clear degraded")
	}
}

// A write that outlives WriteTimeout is abandoned and counted as failed; the
// writer moves on.
func TestPersisterTimesOutAStuckWrite(t *testing.T) {
	p, store, _, logs := newTestPersister(t, func(c *PersisterConfig) {
		c.WriteTimeout = 20 * time.Millisecond
	})
	store.hold = make(chan struct{}) // never released
	p.Start()

	p.Submit(operatorAction(1))
	waitFor(t, "the timed-out write", func() bool { return p.Stats().Failed == 1 })
	if !strings.Contains(logs.all(), context.DeadlineExceeded.Error()) {
		t.Errorf("a timed-out write must be logged as such, got: %s", logs.all())
	}
}

// A store failing on every write logs once per FailureLogEvery with a count,
// not once per event.
func TestPersisterCoalescesLossReports(t *testing.T) {
	p, _, clock, logs := newTestPersister(t, func(c *PersisterConfig) {
		c.FailureLogEvery = time.Minute
	})

	for i := range 5 {
		p.report(fmt.Sprintf("request_id=r%d", i), "database is locked")
	}
	if logs.count() != 1 {
		t.Fatalf("5 losses inside one window wrote %d lines, want 1", logs.count())
	}
	clock.Advance(time.Minute)
	p.report("request_id=r5", "database is locked")
	if logs.count() != 2 || !strings.Contains(logs.lines[1], "5 audit write(s) lost") {
		t.Errorf("the next window must report the 4 held back plus the new one, got: %s", logs.all())
	}
}

// Stop writes what is queued and the pending summary, is idempotent, and
// turns later submissions into counted drops instead of a panic or a leak.
func TestPersisterStopDrainsThenRefuses(t *testing.T) {
	p, store, _, logs := newTestPersister(t, nil)
	p.Start()
	for i := range 10 {
		p.Submit(operatorAction(i))
		p.Submit(anonymousDenied(i))
	}
	p.Stop()
	p.Stop()

	events, summaries := store.snapshot()
	if len(events) != 15 || len(summaries) != 1 || summaries[0].Count != 5 {
		t.Fatalf("after Stop: %d events and summaries %+v, want 15 and one of 5", len(events), summaries)
	}

	p.Submit(operatorAction(99))
	if st := p.Stats(); st.Dropped != 1 {
		t.Errorf("a submission after Stop must be dropped and counted, got %+v", st)
	}
	if !strings.Contains(logs.all(), "the persister is stopped") {
		t.Errorf("a submission after Stop must be logged, got: %s", logs.all())
	}
	if events, _ := store.snapshot(); len(events) != 15 {
		t.Error("nothing may be written after Stop")
	}
}

// Shutdown must not hang on a stalled store. With a write timeout far longer
// than the grace and a full queue behind the write in flight, Stop waits for
// that one write and then spends only the grace on the rest: writing the
// queue one timeout at a time would take 50 x 200ms. Every event is still
// accounted for, as failed or dropped.
func TestPersisterStopIsBoundedByTheGrace(t *testing.T) {
	const queued = 50
	p, store, _, _ := newTestPersister(t, func(c *PersisterConfig) {
		c.QueueSize = queued
		c.WriteTimeout = 200 * time.Millisecond
		c.StopGrace = 30 * time.Millisecond
	})
	store.hold = make(chan struct{}) // never released
	store.entered = make(chan struct{}, queued+1)
	p.Start()
	p.Submit(operatorAction(0))
	<-store.entered // one write in flight; the rest queue behind it
	for i := 1; i <= queued; i++ {
		p.Submit(operatorAction(i))
	}

	began := time.Now()
	stopped := make(chan struct{})
	go func() {
		p.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return while the store was stalled")
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("Stop took %s; the in-flight write (200ms) plus the grace (30ms) should bound it", took)
	}
	if st := p.Stats(); st.Failed+st.Dropped != queued+1 || st.Persisted != 0 {
		t.Errorf("stats = %+v, want all %d events failed or dropped", st, queued+1)
	}
}

// deadlineStore holds its first write until released and fails every later
// one at once, recording the deadline each later write was given.
type deadlineStore struct {
	release   chan struct{}
	entered   chan struct{}
	mu        sync.Mutex
	first     bool
	deadlines []time.Time
}

func (s *deadlineStore) AppendEvent(ctx context.Context, _ Event) error {
	s.mu.Lock()
	first := !s.first
	s.first = true
	s.mu.Unlock()
	if first {
		s.entered <- struct{}{}
		<-s.release
		return nil
	}
	deadline, _ := ctx.Deadline()
	s.mu.Lock()
	s.deadlines = append(s.deadlines, deadline)
	s.mu.Unlock()
	return errors.New("stalled")
}

func (s *deadlineStore) AppendSuppression(context.Context, Suppression) error { return nil }

// Once Stop is called, no queued event may be written under the full
// WriteTimeout: select picks at random among ready cases, so a writer that
// only noticed Stop when select happened to choose it would keep writing the
// queue. Each round below has a 1-in-2 chance of catching such a writer; 20
// rounds make a miss one in a million.
func TestPersisterStopTakesPriorityOverTheQueue(t *testing.T) {
	const grace = 50 * time.Millisecond
	for round := range 20 {
		store := &deadlineStore{release: make(chan struct{}), entered: make(chan struct{}, 1)}
		p := NewPersister(store, PersisterConfig{WriteTimeout: time.Hour, StopGrace: grace, Logf: (&logCapture{}).Logf})
		p.Start()
		p.Submit(operatorAction(0))
		<-store.entered
		for i := 1; i <= 10; i++ {
			p.Submit(operatorAction(i))
		}

		stopped := make(chan struct{})
		go func() {
			p.Stop()
			close(stopped)
		}()
		waitFor(t, "Stop to signal the writer", func() bool {
			select {
			case <-p.stop:
				return true
			default:
				return false
			}
		})
		signaled := time.Now()
		close(store.release)
		<-stopped

		store.mu.Lock()
		for _, d := range store.deadlines {
			if d.After(signaled.Add(grace + time.Second)) {
				t.Fatalf("round %d: a queued event was written after Stop with deadline %s, beyond the grace", round, d.Sub(signaled))
			}
		}
		store.mu.Unlock()
	}
}

// Without Start nothing is written; Stop accounts for what was queued.
func TestPersisterStopWithoutStartCountsQueued(t *testing.T) {
	p, store, _, _ := newTestPersister(t, nil)
	p.Submit(operatorAction(1))
	p.Submit(operatorAction(2))
	p.Stop()
	p.Start()

	if events, _ := store.snapshot(); len(events) != 0 {
		t.Errorf("wrote %d events without a writer", len(events))
	}
	if st := p.Stats(); st.Dropped != 2 {
		t.Errorf("stats = %+v, want the 2 queued events dropped", st)
	}
}

// A pending summary is written on the flush tick, not only at Stop.
func TestPersisterFlushesSummariesPeriodically(t *testing.T) {
	p, store, _, _ := newTestPersister(t, func(c *PersisterConfig) {
		c.Budget = Budget{Burst: 1, Every: time.Hour}
		c.FlushInterval = 5 * time.Millisecond
	})
	p.Start()
	for i := range 3 {
		p.Submit(anonymousDenied(i))
	}

	waitFor(t, "the periodic summary", func() bool {
		_, summaries := store.snapshot()
		return len(summaries) == 1 && summaries[0].Count == 2
	})
}

// Concurrent submitters racing Stop: every event is accounted for exactly
// once and nothing races (run under -race).
func TestPersisterAccountsForEveryEventUnderConcurrentStop(t *testing.T) {
	p, store, _, _ := newTestPersister(t, func(c *PersisterConfig) {
		c.QueueSize = 64
	})
	p.Start()

	const workers, each = 8, 200
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				if i%2 == 0 {
					p.Submit(operatorAction(w*each + i))
				} else {
					p.Submit(anonymousDenied(w*each + i))
				}
			}
		}()
	}
	time.Sleep(time.Millisecond)
	p.Stop()
	wg.Wait()

	st := p.Stats()
	if total := st.Persisted + st.Suppressed + st.Dropped + st.Failed; total != workers*each {
		t.Errorf("stats %+v account for %d events, want %d", st, total, workers*each)
	}
	if events, _ := store.snapshot(); uint64(len(events)) != st.Persisted {
		t.Errorf("the store holds %d events, stats say %d were persisted", len(events), st.Persisted)
	}
}
