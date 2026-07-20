package service

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"runtime/debug"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// Certificate renewal scheduling defaults. Overridden from configuration; see
// config.OrchestratorConfig.AgentCertRenew*.
const (
	// DefaultCertRenewalInterval is how often the fleet is checked for
	// certificates that are due.
	//
	// Hourly, which is far more often than any certificate becomes due. That is
	// the point: the sweep is cheap (one query, then work only for the qubes
	// inside their window), and a long interval would make the retry schedule
	// coarser than the failures it is meant to survive.
	DefaultCertRenewalInterval = time.Hour

	// DefaultCertRenewalThreshold is the fraction of a certificate's TOTAL
	// lifetime that must remain for it to be considered still fresh. Below it,
	// the certificate is due for renewal.
	//
	// One third: on the 90-day agent certificate that opens a 30-day window.
	//
	// The window width is NOT the runway any particular qube
	// gets. Jitter is added forward from the start of the window, so the qube
	// that renews last starts trying with markedly less than that (see
	// certRenewalJitterFraction, and renewalRunway, which computes the real
	// number rather than leaving it to be inferred from prose). Every claim about
	// this window has to be made against renewalRunway(), never the window width
	// — quoting the width is how a threshold gets "optimized" down by someone
	// reading the wrong number.
	//
	// Sized by how much repeated failure it has to absorb, not by taste. With an
	// hourly sweep and a retry backoff capped at six hours, a qube that is
	// unreachable for a fortnight — a suspended workload, a holiday, a broken
	// hypervisor node — still gets many tens of attempts in what remains, at the
	// capped backoff. Start logs the actual runway; trust that over this comment.
	// the last 10% would leave 6.75 days for the last qube in the jitter window,
	// which is inside the range where a single unnoticed weekend plus one bad
	// deploy ends with the fleet dark.
	//
	// Expressed as a fraction of the total lifetime rather than as a fixed number
	// of days so that shortening the certificate lifetime automatically shortens
	// the renewal period with it. A fixed 30 days against a 14-day certificate
	// would mean every certificate is born already due.
	DefaultCertRenewalThreshold = 1.0 / 3.0

	// certRenewalJitterFraction is how much of the renewal window is used to
	// spread the fleet out.
	//
	// Jitter is not politeness here, it is the difference between a fleet-wide
	// event and a leading edge. Every qube in a rollout is issued its certificate
	// within the same few minutes, so without jitter every qube in the fleet
	// crosses the threshold in the same HOUR, ~60 days later: one sweep tries to
	// renew everything at once, one CA signs everything at once, and — the part
	// that actually hurts — any systematic breakage (a CA that will not sign, an
	// agent build with a broken builtin) is discovered by the whole fleet
	// simultaneously, with nothing having gone first to warn anyone.
	//
	// A quarter of the window spreads a 90-day fleet over about a week: the
	// earliest qube renews near the threshold, the latest a week later.
	// renewalRunway() has the exact figures for the configured threshold.
	//
	// FORWARD-ONLY, and deliberately so. The offset is added to notAfter−W, never
	// subtracted, so jitter can only ever DELAY a renewal: the guaranteed runway
	// is W−J+S, not W, and the mean sits between them. Centring the offset
	// instead (±J/2) would raise the worst case by J/2 and make the mean exactly
	// W — but it would also renew the earliest qubes appreciably early
	// left, weakening the property the threshold states: a certificate is not
	// renewed while substantially more than `Threshold` of its life remains.
	// Forward-only keeps the earliest renewal at the threshold itself, which is
	// checkable and stops a misconfigured threshold quietly minting certificates
	// early.
	//
	// Note the property is NOT exact, and saying so matters: certRenewalClockSkewMargin
	// below pulls every renewal forward by a further day, so the earliest qube is
	// due at ~34.4% remaining against a 33.3% threshold. Forward-only jitter was
	// chosen partly to preserve an invariant the skew margin then bends — an
	// honest "approximately" here is worth more than a clean claim that a reader
	// would later discover is false.
	//
	// The runway is NOT a constant to quote from memory. It depends on the
	// configured threshold, which defaults to 33 (not 1/3), so it moves. Hence
	// renewalRunway computes it, Start logs the real figure at boot, and a test
	// pins it — because the way this goes wrong is not the arithmetic, it is a
	// number in a comment that outlives whoever knew better.
	certRenewalJitterFraction = 0.25

	// certRenewalClockSkewMargin brings every renewal forward by a day.
	//
	// Not a fix for agent clock skew — nothing in the scheduler can be, because
	// the due decision is made entirely on the console's clock against a value
	// the console's own CA wrote (the full account is in certrenew.go, above
	// renewRelayCertLifetime). This is a margin for the disagreements the console
	// cannot see: a registry row whose expiry was recorded by a process whose
	// clock has since been corrected, and the general case of the console being
	// the last thing in the cluster to find out its own time was wrong.
	//
	// A day, because the trade is lopsided. Renewing a day early costs one
	// signature that would have been spent anyway; renewing a day late costs the
	// qube. It is clamped against the window in skewMarginFor so a short-lived
	// certificate is not born already due.
	certRenewalClockSkewMargin = 24 * time.Hour

	// certRenewalMaxSkewFraction caps the skew margin at an eighth of the renewal
	// window. Without it, shortening pki.DefaultAgentCertLifetime to, say, three
	// days would leave a one-day window and a one-day margin — every certificate
	// due the moment it was issued, and a console signing a new one every sweep
	// forever.
	certRenewalMaxSkewFraction = 0.125

	// certRenewalRetryBase is the wait after a first failed renewal.
	certRenewalRetryBase = 15 * time.Minute
	// certRenewalRetryMax caps the backoff.
	//
	// Bounded at both ends on purpose. Doubling without a cap would push a qube
	// that was unreachable for a week out to a retry interval measured in days,
	// so it would keep failing long after the fault was fixed. Six hours means a
	// recovered qube is retried within a working day at worst, while a
	// permanently dead one costs the sweep four attempts a day instead of
	// twenty-four.
	certRenewalRetryMax = 6 * time.Hour
)

// certRenewalWarningPrefix marks a renewal failure inside the agent-health
// fields.
//
// A stable prefix because that field is the only place this can surface: it is
// what lets "the agent is fine but its certificate is not being renewed" be told
// apart from "the agent is not answering". The remedies have nothing in common —
// the first is a console/CA problem on a working qube, the second is a broken
// qube — and a fleet reading the wrong one wastes the weeks of warning this
// whole mechanism exists to buy.
const certRenewalWarningPrefix = "agent certificate renewal FAILING"

// CertRenewalQubes is the slice of the qube repository the scheduler needs.
//
// Read-only over qubes and write-only over the agent_* columns. It must never
// be able to set a qube's status: a certificate that will not renew says nothing
// about whether the VM is running, and this monitor conflating the two would
// recreate exactly the merge that models.Qube documents as forbidden.
type CertRenewalQubes interface {
	ListByStatus(ctx context.Context, statuses []models.QubeStatus) ([]*models.Qube, error)
}

// AgentHealthWriter records an agent observation on the qube row. Implemented
// by repository.QubeRepository.
type AgentHealthWriter interface {
	UpdateAgentHealth(
		ctx context.Context, id string, health models.AgentHealth, probedAt time.Time, failure string,
	) error
}

// QubeCertRenewer renews one qube's certificate. Implemented by *CertRenewer.
type QubeCertRenewer interface {
	Renew(ctx context.Context, qube *models.Qube) CertRenewalResult
}

// CertRenewalConfig configures the scheduler. Zero values take the defaults.
type CertRenewalConfig struct {
	// Interval is the gap between sweeps. Zero or negative DISABLES renewal,
	// which puts the fleet back on rebuild-to-rotate and is logged as such.
	Interval time.Duration
	// Threshold is the fraction of total lifetime that must remain for a
	// certificate to count as fresh. Values outside (0,1) take the default.
	Threshold float64
}

// renewalState is what the scheduler remembers about one qube between sweeps.
type renewalState struct {
	// failures is the consecutive failure count driving the backoff. Reset to
	// zero by a success, so a qube that renews after trouble is not left on a
	// six-hour retry.
	failures int
	// nextAttempt is when this qube may be tried again.
	nextAttempt time.Time
	// status/reason/notAfter describe the last failure, and are what
	// RenewalWarning re-publishes into the health fields on every probe.
	status   CertRenewalStatus
	reason   string
	notAfter time.Time
	since    time.Time
}

// CertRenewalMonitor renews agent certificates before they expire.
//
// It exists because an agent certificate could previously only be replaced by
// REBUILDING the qube: cloud-init, the sole delivery channel, is read once at
// first boot. That made the 90-day lifetime a fleet rebuild period rather than a
// security parameter, with every certificate in a rollout expiring on the same
// day.
//
// The monitor is deliberately separate from AgentHealthMonitor even though both
// sweep running qubes. They answer different questions on different timescales —
// "is this agent alive right now" every minute, "will this agent still be able
// to authenticate next month" every hour — and folding renewal into the health
// sweep would put a slow, CA-signing, registry-writing operation on the path of
// the console's fastest feedback loop.
type CertRenewalMonitor struct {
	qubes   CertRenewalQubes
	certs   AgentCertLister
	renewer QubeCertRenewer
	health  AgentHealthWriter
	cfg     CertRenewalConfig

	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	stop   sync.Once

	mu    sync.RWMutex
	state map[string]*renewalState
	// cursor is where the next sweep starts in the qube list. A sweep that runs
	// out of budget must not always abandon the SAME tail of the fleet, which
	// would leave those qubes never renewed while every sweep reported work done.
	cursor int
	// live is the monitor's record of its own sweeping, guarded by mu. See
	// Liveness: a renewal loop that stopped is invisible from the outside, and
	// stays invisible for weeks.
	live sweepLiveness

	// now is injectable so the scheduling rules can be tested without waiting
	// months for a certificate to age.
	now func() time.Time
}

// NewCertRenewalMonitor builds a scheduler. Call Start to spawn its goroutine.
//
// health may be nil, which disables the health-field reporting but not renewal.
// That combination is not recommended and Start says so: silent renewal is the
// failure this feature was built to make impossible.
func NewCertRenewalMonitor(
	qubes CertRenewalQubes, certs AgentCertLister, renewer QubeCertRenewer,
	health AgentHealthWriter, cfg CertRenewalConfig,
) *CertRenewalMonitor {
	if cfg.Threshold <= 0 || cfg.Threshold >= 1 {
		cfg.Threshold = DefaultCertRenewalThreshold
	}
	base, cancel := context.WithCancel(context.Background())
	return &CertRenewalMonitor{
		qubes:   qubes,
		certs:   certs,
		renewer: renewer,
		health:  health,
		cfg:     cfg,
		base:    base,
		cancel:  cancel,
		state:   map[string]*renewalState{},
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// Start spawns the sweep loop.
func (m *CertRenewalMonitor) Start() {
	if m == nil {
		return
	}
	if m.cfg.Interval <= 0 {
		// Loud, because a console that is not renewing looks exactly like one
		// that is: every certificate is valid today either way. The difference
		// only becomes visible on the day the whole fleet stops authenticating.
		log.Printf("certrenew: agent certificate renewal is DISABLED " +
			"(set orchestrator.agent_cert_renew_interval_seconds > 0); " +
			"certificates can then only be replaced by REBUILDING each qube, " +
			"and every certificate issued in the same rollout expires on the same day")
		return
	}
	if m.health == nil {
		log.Printf("certrenew: no agent-health writer wired; renewal failures will only appear in the log")
	}

	m.mu.Lock()
	m.live.startedAt = m.now()
	m.live.running = true
	m.mu.Unlock()

	m.wg.Add(1)
	go m.loop()
	// A second goroutine, on purpose. A watchdog running inside the sweep loop
	// cannot report that the sweep loop has stopped, which is the failure it
	// exists for.
	m.wg.Add(1)
	go m.watchdog()

	// The runway is LOGGED, not just commented, and it is the computed worst case
	// rather than the nominal window. Whoever changes the threshold sees the real
	// number in the console's own startup line the first time they restart it,
	// instead of inheriting a comment.
	log.Printf("certrenew: sweeping every %s, renewing once less than %.0f%% of a certificate's lifetime remains "+
		"(spread over %.0f%% of that window so a rollout does not renew all at once); "+
		"on a %s certificate the LAST qube in the spread begins renewing with %s left, "+
		"which is the real safety margin -- not %s",
		m.cfg.Interval, m.cfg.Threshold*100, certRenewalJitterFraction*100,
		pki.DefaultAgentCertLifetime,
		renewalRunway(pki.DefaultAgentCertLifetime, m.cfg.Threshold).Round(time.Hour),
		time.Duration(float64(pki.DefaultAgentCertLifetime)*m.cfg.Threshold).Round(time.Hour))
}

// Shutdown stops the sweep and waits for it, up to grace.
//
// The base context is canceled before waiting, matching AgentHealthMonitor: an
// abandoned renewal leaves nothing behind — the agent keeps the certificate it
// already had — so there is no reason to hold a restart open for one.
func (m *CertRenewalMonitor) Shutdown(grace time.Duration) {
	if m == nil {
		return
	}
	m.stop.Do(func() {
		m.cancel()

		done := make(chan struct{})
		go func() {
			m.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(grace):
			log.Printf("certrenew: shutdown grace of %s elapsed with a renewal still in flight; continuing", grace)
		}
	})
}

// loop sweeps on the configured interval.
func (m *CertRenewalMonitor) loop() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.base.Done():
			return
		case <-ticker.C:
			m.sweepGuarded(m.base)
		}
	}
}

// sweepGuarded runs one sweep and refuses to let a panic end renewal.
//
// Without the recover, a nil dereference anywhere under Sweep — in a repository,
// in a renewer, in a result nobody expected to be empty — kills this goroutine
// and renewal simply never happens again. Nothing logs it after the panic trace
// scrolls away, no health field changes, and the fleet reads perfectly healthy
// until every certificate expires together. That is the single worst way this
// component can fail, and it costs six lines to make it survivable.
//
// The panic is NOT swallowed quietly: it is logged in full, counted, and
// published through Liveness, so a sweep that panics on every tick is loud
// rather than merely non-fatal.
func (m *CertRenewalMonitor) sweepGuarded(ctx context.Context) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		m.mu.Lock()
		m.live.panics++
		m.live.lastPanic = fmt.Sprint(p)
		m.live.lastPanicAt = m.now()
		count := m.live.panics
		m.mu.Unlock()

		log.Printf("certrenew: sweep PANICKED (%d time(s)) and was contained so renewal continues "+
			"on the next tick; certificates are NOT being renewed while this repeats: %v\n%s",
			count, p, debug.Stack())
	}()
	m.Sweep(ctx)
}

// watchdog says out loud when sweeping has stopped.
//
// It runs on its own goroutine and its own timer so that it survives whatever
// killed the sweep. The failure it exists for is stated in Liveness: a renewal
// sweep that stopped running produces exactly the same observations as a fleet
// where nothing is due — no errors, no health changes, nothing — right up to the
// day every certificate expires at once. Something has to notice the ABSENCE.
func (m *CertRenewalMonitor) watchdog() {
	defer m.wg.Done()

	// Faster than the sweep, so a stall is reported within a fraction of the
	// window it is eating, and floored so a tiny configured interval cannot spin.
	every := m.cfg.Interval / 2
	if every < time.Minute {
		every = time.Minute
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-m.base.Done():
			return
		case <-ticker.C:
			if l := m.Liveness(); l.Stalled {
				// Every tick, not once. A stall that is announced once and then
				// goes quiet is indistinguishable from a stall that resolved, and
				// this one is measured against certificate expiry — the operator
				// needs it still saying so tomorrow.
				log.Printf("certrenew: renewal sweeps have STALLED: %s. "+
					"No agent certificate is being renewed. This looks identical to a fleet "+
					"with nothing due, so nothing else will report it; %d sweep(s) have run since %s",
					l.StallReason, l.Sweeps, expiryText(l.StartedAt))
			}
		}
	}
}

// Sweep renews every running qube whose certificate is due.
//
// Running only, like the health reconciler: a suspended qube has no compute
// instance to dial, so trying would produce a guaranteed failure every sweep and
// bury the real ones. A suspended qube whose certificate expires while it is
// down is a genuine gap, and it is left to the resume path rather than solved by
// dialing a machine that does not exist.
func (m *CertRenewalMonitor) Sweep(ctx context.Context) {
	// Marked before and after, and the end is deferred so it lands however this
	// returns. The pair is what lets a wedged sweep be told apart from a dead
	// loop — see renewalStalled — and neither is inferable from the outside.
	m.sweepStarted()
	defer m.sweepEnded()

	qubes, err := m.qubes.ListByStatus(ctx, []models.QubeStatus{models.QubeStatusRunning})
	m.noteFleetRead(err)
	if err != nil {
		log.Printf("certrenew: could not list running qubes to check their certificates: %v", err)
		return
	}
	if len(qubes) == 0 {
		return
	}

	// A sweep is bounded so it cannot run into the next one. Renewals are
	// sequential and each is allowed up to CertRenewer.timeout, so on a large
	// fleet in a total outage the arithmetic alone would overrun the interval —
	// and Ticker drops the missed ticks rather than queueing them, so the sweep
	// would silently become "as often as it can" without anyone being told.
	//
	// The floor matters for a Sweep called directly rather than from the loop:
	// with a zero interval the budget would be zero, and the sweep would stop
	// after a single qube while reporting that it had run.
	budget := m.cfg.Interval * 3 / 4
	if budget < time.Minute {
		budget = time.Minute
	}
	deadline := m.now().Add(budget)

	start := m.takeCursor(len(qubes))
	var due, renewed, failed, uncertified, skipped int

	for i := 0; i < len(qubes); i++ {
		if ctx.Err() != nil {
			return
		}
		if m.now().After(deadline) {
			// Where the sweep stopped is remembered by takeCursor, so the next
			// one resumes here instead of starting over and starving the tail.
			log.Printf("certrenew: sweep budget spent after %d of %d qubes; the rest are checked next sweep",
				i, len(qubes))
			break
		}
		m.cursorAt(start + i + 1)

		q := qubes[(start+i)%len(qubes)]
		outcome := m.considerQube(ctx, q)
		switch outcome {
		case considerRenewed:
			due++
			renewed++
		case considerFailed:
			due++
			failed++
		case considerNoCert:
			uncertified++
		case considerBackoff:
			due++
			skipped++
		case considerFresh:
		}
	}

	if due > 0 || uncertified > 0 {
		log.Printf("certrenew: sweep complete: %d due, %d renewed, %d failed, %d waiting on backoff, "+
			"%d running qube(s) have no registered certificate",
			due, renewed, failed, skipped, uncertified)
	}
}

// sweepStarted records that a sweep is in flight.
func (m *CertRenewalMonitor) sweepStarted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.live.lastStart = m.now()
	m.live.sweeps++
	// Set here as well as in Start so a Sweep called directly — from a test, or
	// any future manual trigger — is not reported as a monitor that never ran.
	if m.live.startedAt.IsZero() {
		m.live.startedAt = m.live.lastStart
	}
}

// sweepEnded records that a sweep finished, and complains if it outran the
// interval — a sweep slower than its own schedule silently degrades to "as often
// as it can", because Ticker drops missed ticks rather than queueing them.
func (m *CertRenewalMonitor) sweepEnded() {
	m.mu.Lock()
	now := m.now()
	took := now.Sub(m.live.lastStart)
	m.live.lastEnd = now
	m.mu.Unlock()

	if m.cfg.Interval > 0 && took > m.cfg.Interval {
		log.Printf("certrenew: a sweep took %s, longer than the %s interval; "+
			"ticks are being dropped and the fleet is now checked as often as it can be rather than "+
			"on schedule -- the renewal window is being consumed faster than it is being reported",
			took.Round(time.Second), m.cfg.Interval)
	}
}

// noteFleetRead records whether this sweep could see the fleet at all.
func (m *CertRenewalMonitor) noteFleetRead(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.live.listErr = err.Error()
		return
	}
	m.live.lastListed = m.now()
	m.live.listErr = ""
}

// considerOutcome is what one qube contributed to a sweep.
type considerOutcome int

const (
	considerFresh considerOutcome = iota
	considerRenewed
	considerFailed
	considerBackoff
	considerNoCert
)

// considerQube decides whether one qube is due, and renews it if so.
func (m *CertRenewalMonitor) considerQube(ctx context.Context, q *models.Qube) considerOutcome {
	if q == nil {
		return considerFresh
	}
	cert := m.currentCert(ctx, q.ID)
	if cert == nil {
		// No registered certificate at all. Nothing to renew — this qube either
		// predates certificate issuance or never got one, which is the issuer's
		// problem and already visible as a failing agent probe. Counted rather
		// than logged per qube so it cannot drown the sweep summary.
		return considerNoCert
	}

	now := m.now()
	if now.Before(m.dueAt(q.ID, cert)) {
		return considerFresh
	}

	// Backoff is checked AFTER dueness so a qube that is not yet due does not
	// consume its retry budget, and so the log line above counts it as due.
	if !m.mayAttempt(q.ID, now) {
		return considerBackoff
	}

	res := m.renewer.Renew(ctx, q)
	m.record(ctx, q, cert, res)
	if res.Status == CertRenewalOK {
		return considerRenewed
	}
	return considerFailed
}

// currentCert reads the certificate a qube is expected to be holding.
func (m *CertRenewalMonitor) currentCert(ctx context.Context, qubeID string) *repository.AgentCert {
	if m.certs == nil {
		return nil
	}
	list, err := m.certs.ListByQube(ctx, qubeID)
	if err != nil {
		log.Printf("certrenew: could not read the certificates issued to qube %s: %v", qubeID, err)
		return nil
	}
	return newestUsableCert(list)
}

// dueAt is when this qube's certificate should be renewed.
//
// The threshold gives every certificate the same proportional window; the jitter
// then moves each qube to a different point inside the leading quarter of that
// window, so a fleet issued in one rollout does not renew in one hour. Because
// the jitter only ever pushes LATER, the runway a qube is actually guaranteed is
// the window minus the jitter span — renewalRunway, not the window.
func (m *CertRenewalMonitor) dueAt(qubeID string, cert *repository.AgentCert) time.Time {
	return renewalDueAt(qubeID, cert, m.cfg.Threshold)
}

// renewalDueAt is dueAt as a pure function, so the scheduling rule can be
// asserted directly rather than inferred from the behavior of a loop.
func renewalDueAt(qubeID string, cert *repository.AgentCert, threshold float64) time.Time {
	if cert == nil || cert.ExpiresAt == nil {
		// No expiry recorded means nothing can be reasoned about. Returning the
		// zero time makes it due immediately, which is the safe direction: an
		// unnecessary renewal costs one signature, a missed one costs the qube.
		return time.Time{}
	}
	notAfter := *cert.ExpiresAt

	// A missing or nonsensical issuance time (a row written before issued_at was
	// recorded, a clock that went backwards) falls back to the standard lifetime.
	//
	// The zero time is tested for explicitly rather than by checking the
	// subtraction: notAfter.Sub(time.Time{}) is about two thousand years, which
	// saturates time.Duration instead of going negative, so a "total <= 0" guard
	// never fires. The window would then be centuries wide, every such
	// certificate would be due on every sweep, and the console would sign a new
	// one every hour for as long as the row stayed that way.
	total := pki.DefaultAgentCertLifetime
	if !cert.IssuedAt.IsZero() {
		if d := notAfter.Sub(cert.IssuedAt); d > 0 {
			total = d
		}
	}

	window := time.Duration(float64(total) * threshold)
	spread := time.Duration(float64(window) * certRenewalJitterFraction)
	offset := time.Duration(float64(spread) * jitterFraction(qubeID))

	// Forward from the start of the window, so the offset only ever delays. The
	// skew margin then pulls everything back by a fixed amount, which is the one
	// adjustment that must NOT be per-qube: a margin that varied with the id
	// would be indistinguishable from more jitter and would not bound anything.
	return notAfter.Add(-window).Add(offset).Add(-skewMarginFor(window))
}

// skewMarginFor is the clock-skew margin that fits inside a renewal window.
func skewMarginFor(window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	margin := certRenewalClockSkewMargin
	if limit := time.Duration(float64(window) * certRenewalMaxSkewFraction); margin > limit {
		margin = limit
	}
	return margin
}

// renewalRunway is the WORST-CASE time a qube has between its certificate
// becoming due and that certificate expiring.
//
// Computed rather than written down, because the number that matters is not the
// renewal window: jitter is forward-only, so the last qube in the spread starts
// trying a full jitter span after the first. Every operator-facing statement
// about the safety margin goes through here, so a change to the threshold or
// the jitter fraction moves the claim with it instead of leaving a comment
// asserting a margin that stopped being true.
func renewalRunway(lifetime time.Duration, threshold float64) time.Duration {
	if lifetime <= 0 || threshold <= 0 {
		return 0
	}
	window := time.Duration(float64(lifetime) * threshold)
	spread := time.Duration(float64(window) * certRenewalJitterFraction)
	return window - spread + skewMarginFor(window)
}

// jitterFraction maps a qube id to a stable value in [0,1).
//
// Deterministic, not random. A random draw per sweep would re-roll every hour
// and give a qube no settled position at all; a random draw per process would
// re-bunch the fleet after a console restart, which is exactly when a rollout is
// most likely to have happened. Hashing the id spreads the fleet once and keeps
// it spread, and makes "when is this qube due" reproducible when someone asks.
func jitterFraction(qubeID string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(qubeID))
	// The top 53 bits are the ones a float64 can hold exactly.
	return float64(h.Sum64()>>11) / float64(uint64(1)<<53)
}

// mayAttempt reports whether a qube is out of its retry backoff.
func (m *CertRenewalMonitor) mayAttempt(qubeID string, now time.Time) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.state[qubeID]
	if !ok {
		return true
	}
	return !now.Before(st.nextAttempt)
}

// record updates the retry schedule and publishes the outcome.
func (m *CertRenewalMonitor) record(
	ctx context.Context, q *models.Qube, cert *repository.AgentCert, res CertRenewalResult,
) {
	now := m.now()

	m.mu.Lock()
	st, ok := m.state[q.ID]
	if !ok {
		st = &renewalState{}
		m.state[q.ID] = st
	}
	if res.Status == CertRenewalOK {
		// Cleared entirely rather than kept with a zero counter, so a recovered
		// qube stops publishing a warning the moment it renews.
		delete(m.state, q.ID)
	} else {
		st.failures++
		st.nextAttempt = now.Add(backoffFor(st.failures))
		st.status = res.Status
		st.reason = res.Reason
		st.notAfter = res.PreviousNotAfter
		if st.notAfter.IsZero() && cert != nil && cert.ExpiresAt != nil {
			st.notAfter = *cert.ExpiresAt
		}
		if st.since.IsZero() {
			st.since = now
		}
	}
	failures, since := 0, time.Time{}
	if s, ok := m.state[q.ID]; ok {
		failures, since = s.failures, s.since
	}
	m.mu.Unlock()

	if res.Status != CertRenewalOK {
		// Said out loud on every failure, with the date the qube stops being able
		// to authenticate. "Renewal failed" is a log line people scroll past;
		// "this qube goes dark on the 14th" is not.
		log.Printf("certrenew: qube %q has now failed to renew %d time(s) since %s (%s): %s "+
			"-- its current certificate expires %s",
			q.Name, failures, expiryText(since), res.Status, res.Reason, expiryText(m.expiryOf(q.ID, cert)))
	}
	m.publishHealth(ctx, q, res)
}

// expiryOf is the expiry to quote in a failure message.
func (m *CertRenewalMonitor) expiryOf(qubeID string, cert *repository.AgentCert) time.Time {
	m.mu.RLock()
	st, ok := m.state[qubeID]
	m.mu.RUnlock()
	if ok && !st.notAfter.IsZero() {
		return st.notAfter
	}
	if cert != nil && cert.ExpiresAt != nil {
		return *cert.ExpiresAt
	}
	return time.Time{}
}

// backoffFor is the wait after n consecutive failures: 15m, 30m, 1h, 2h, 4h,
// then capped at 6h.
func backoffFor(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	// Shifting rather than math.Pow, and clamped before the shift: a qube that
	// has been failing for a month would otherwise overflow the exponent and
	// wrap to a negative duration, which reads as "retry immediately, forever".
	const maxShift = 12
	shift := failures - 1
	if shift > maxShift {
		shift = maxShift
	}
	d := certRenewalRetryBase << uint(shift)
	if d > certRenewalRetryMax || d <= 0 {
		return certRenewalRetryMax
	}
	return d
}

// publishHealth writes the renewal outcome into the agent-health fields.
//
// The health VALUE stays honest about the agent rather than about the renewal.
// A renewal that failed after the agent answered is positive proof the agent is
// alive, so it records healthy — and puts the renewal failure in the error
// field, where it is distinguishable from a probe failure by
// certRenewalWarningPrefix. Recording "unreachable" for a working qube whose CA
// will not sign would send an operator to SSH into the one machine that is fine.
//
// A renewal that never reached the agent records unreachable, which is the same
// diagnosis a failed probe would reach by the same evidence.
func (m *CertRenewalMonitor) publishHealth(ctx context.Context, q *models.Qube, res CertRenewalResult) {
	if m.health == nil {
		return
	}
	if res.Status == CertRenewalNotConfigured {
		// This console learned nothing about the agent. Overwriting a real probe
		// result with the console's own misconfiguration would destroy the
		// reading rather than add to it; Start already says this loudly.
		return
	}

	// KNOWN INTERACTION: this can overwrite AgentHealthStarting on a qube that is
	// inside its post-boot grace window, since the monitor has no view of
	// AgentHealthMonitor.IsSettling. It is not reachable in practice — a qube
	// that just booted holds a freshly issued certificate and is nowhere near its
	// renewal threshold — and wiring the two monitors together to close it would
	// couple the fastest feedback loop to the slowest one.
	health := models.AgentHealthHealthy
	if !res.Status.AgentAnswered() {
		health = models.AgentHealthUnreachable
	}
	failure := ""
	if res.Status != CertRenewalOK {
		failure = renewalWarningText(res.Status, res.Reason, res.PreviousNotAfter, m.now())
	}

	// Detached from the sweep's deadline: the attempt already happened, and
	// losing the record because a shutdown landed a millisecond later would hide
	// the failure until the next sweep — or forever, if the process is stopping.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := m.health.UpdateAgentHealth(ctx, q.ID, health, m.now(), failure); err != nil {
		log.Printf("certrenew: qube %q renewal outcome %s could not be recorded: %v", q.Name, res.Status, err)
	}
}

// RenewalWarning returns the outstanding renewal problem for a qube, or "".
//
// This is what keeps a renewal failure visible. The health reconciler rewrites
// agent_last_error on EVERY sweep — every minute by default — so a failure
// written once by this monitor would be erased by the next successful probe, and
// the fleet would go back to looking perfectly healthy right up until the
// certificates expired. The probe path therefore asks here on every write and
// re-attaches the warning until renewal actually succeeds.
//
// Implements RenewalWatch.
func (m *CertRenewalMonitor) RenewalWarning(qubeID string) string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	st, ok := m.state[qubeID]
	if !ok {
		m.mu.RUnlock()
		return ""
	}
	status, reason, notAfter := st.status, st.reason, st.notAfter
	m.mu.RUnlock()

	return renewalWarningText(status, reason, notAfter, m.now())
}

// renewalWarningText renders a renewal failure for an operator.
//
// It always names the expiry and the days left, because that is the number that
// decides whether this is a ticket for Monday or an incident tonight — and it is
// the number nobody has when the only signal is "renewal failed".
func renewalWarningText(status CertRenewalStatus, reason string, notAfter, now time.Time) string {
	if status == "" || status == CertRenewalOK {
		return ""
	}
	if notAfter.IsZero() {
		return fmt.Sprintf("%s (%s): %s -- current certificate expiry unknown", certRenewalWarningPrefix, status, reason)
	}
	days := int(math.Floor(notAfter.Sub(now).Hours() / 24))
	if days < 0 {
		return fmt.Sprintf("%s (%s): %s -- this qube's certificate EXPIRED on %s and it can no longer authenticate",
			certRenewalWarningPrefix, status, reason, expiryText(notAfter))
	}
	return fmt.Sprintf("%s (%s): %s -- this qube stops being able to authenticate on %s, in %d day(s)",
		certRenewalWarningPrefix, status, reason, expiryText(notAfter), days)
}

// sweepLiveness is the monitor's private record of its own sweeping.
type sweepLiveness struct {
	running   bool
	startedAt time.Time
	// sweeps counts sweeps that RAN. lastEnd is when the most recent one
	// finished; lastStart lets a sweep that is stuck be told apart from a loop
	// that has died, which are different faults with different remedies.
	sweeps    int64
	lastStart time.Time
	lastEnd   time.Time
	// lastListed is the last time a sweep actually read the fleet. A sweep that
	// runs on schedule but cannot list qubes is still not renewing anything, and
	// without this it would look perfectly alive.
	lastListed time.Time
	listErr    string
	// panics record contained panics, which are otherwise only in the log.
	panics      int64
	lastPanic   string
	lastPanicAt time.Time
}

// CertRenewalLiveness is what the scheduler can prove about its own operation.
//
// It exists because "renewal is working" and "renewal stopped weeks ago" produce
// identical observations everywhere else in this console: no errors, no failing
// probes, no health field changes, every certificate still valid. The difference
// only becomes visible on the day the fleet stops authenticating, which is the
// exact failure the renewal feature was built to prevent — so the mechanism has
// to be able to report its own absence.
type CertRenewalLiveness struct {
	// Enabled is whether renewal is configured to run at all. False is a
	// configuration statement, not a fault; Start already logs it loudly.
	Enabled bool `json:"enabled"`
	// Stalled is Enabled and not sweeping. StallReason says which way.
	Stalled     bool   `json:"stalled"`
	StallReason string `json:"stall_reason,omitempty"`

	StartedAt      time.Time `json:"started_at,omitempty"`
	Sweeps         int64     `json:"sweeps"`
	LastSweepStart time.Time `json:"last_sweep_start,omitempty"`
	LastSweepEnd   time.Time `json:"last_sweep_end,omitempty"`
	// LastFleetRead is the last sweep that successfully listed running qubes.
	LastFleetRead time.Time `json:"last_fleet_read,omitempty"`
	LastListError string    `json:"last_list_error,omitempty"`

	Panics      int64     `json:"panics"`
	LastPanic   string    `json:"last_panic,omitempty"`
	LastPanicAt time.Time `json:"last_panic_at,omitempty"`

	// Runway is the worst-case gap between a certificate falling due and
	// expiring, on the standard lifetime. It is reported alongside the stall so
	// whoever reads this knows how long the stall may go on before it is fatal.
	Runway time.Duration `json:"runway_seconds"`
}

// certRenewalStallAfter is how many missed sweeps count as stalled.
//
// Three, so an ordinary slow sweep on a large fleet does not cry wolf — the
// sweep budget is three quarters of the interval, so a fleet big enough to spend
// it still finishes well inside two intervals. Anything past three is not
// slowness.
const certRenewalStallAfter = 3

// Liveness reports whether the sweep loop is still doing its job.
func (m *CertRenewalMonitor) Liveness() CertRenewalLiveness {
	if m == nil {
		return CertRenewalLiveness{}
	}
	m.mu.RLock()
	live := m.live
	m.mu.RUnlock()

	l := CertRenewalLiveness{
		Enabled:        m.cfg.Interval > 0 && live.running,
		StartedAt:      live.startedAt,
		Sweeps:         live.sweeps,
		LastSweepStart: live.lastStart,
		LastSweepEnd:   live.lastEnd,
		LastFleetRead:  live.lastListed,
		LastListError:  live.listErr,
		Panics:         live.panics,
		LastPanic:      live.lastPanic,
		LastPanicAt:    live.lastPanicAt,
		Runway:         renewalRunway(pki.DefaultAgentCertLifetime, m.cfg.Threshold),
	}
	l.Stalled, l.StallReason = renewalStalled(l, m.cfg.Interval, m.now())
	return l
}

// renewalStalled decides whether sweeping has stopped, as a pure function so the
// rule can be asserted directly instead of being inferred from a timing test.
//
// The three ways it stops are kept apart because they are three different
// investigations: the loop is gone (a panic outside the guard, a goroutine that
// returned), a sweep went in and never came out (a repository call with no
// deadline, a renewer wedged on a socket), or sweeps are running fine but cannot
// read the fleet (a database that has been failing for hours, which every
// individual log line already reported and everybody scrolled past).
func renewalStalled(l CertRenewalLiveness, interval time.Duration, now time.Time) (bool, string) {
	if !l.Enabled {
		return false, ""
	}
	if interval <= 0 {
		return false, ""
	}
	tolerance := interval * certRenewalStallAfter

	// A sweep in flight: started, never finished.
	if !l.LastSweepStart.IsZero() && l.LastSweepStart.After(l.LastSweepEnd) {
		if age := now.Sub(l.LastSweepStart); age > tolerance {
			return true, fmt.Sprintf(
				"a sweep started %s ago and has not finished (the sweep is wedged, not the loop)",
				age.Round(time.Minute))
		}
		return false, ""
	}

	// Never swept at all, long after starting.
	if l.LastSweepEnd.IsZero() {
		if age := now.Sub(l.StartedAt); age > tolerance {
			return true, fmt.Sprintf("no sweep has completed in the %s since renewal started",
				age.Round(time.Minute))
		}
		return false, ""
	}

	if age := now.Sub(l.LastSweepEnd); age > tolerance {
		return true, fmt.Sprintf("the last sweep finished %s ago, but they are due every %s",
			age.Round(time.Minute), interval)
	}

	// Sweeping, but blind. Measured from the last successful read rather than
	// from the error, so a database that recovers clears this by itself — and
	// from startup when there has never been one, since the zero time would
	// otherwise read as "blind since year one" and fire on the first hiccup.
	since := l.LastFleetRead
	if since.IsZero() {
		since = l.StartedAt
	}
	if age := now.Sub(since); l.LastListError != "" && age > tolerance {
		return true, fmt.Sprintf(
			"sweeps are running but the fleet has not been readable for %s, so nothing is being "+
				"renewed: %s", age.Round(time.Minute), l.LastListError)
	}
	return false, ""
}

// takeCursor returns where this sweep should start and is safe to call
// concurrently with the accessors above.
func (m *CertRenewalMonitor) takeCursor(n int) int {
	if n <= 0 {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return ((m.cursor % n) + n) % n
}

// cursorAt remembers how far the current sweep got.
func (m *CertRenewalMonitor) cursorAt(i int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursor = i
}
