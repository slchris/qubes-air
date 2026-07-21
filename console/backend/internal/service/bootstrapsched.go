// bootstrapsched.go — the sweep that finds qubes with no identity and gets
// them one.
//
// Nothing else will do it. Under the token design cloud-init delivers a
// credential the guest cannot use on its own; the certificate only exists once
// the console dials in and issues it. A qube whose bootstrap never runs boots
// cleanly, reports provisioned, and has an agent that refuses to serve — the
// shape docs/bootstrap-design.md exists to eliminate.
//
// Deliberately leaner than CertRenewalMonitor, and the difference is not an
// oversight. Renewal carries a watchdog and a liveness record because a stalled
// renewal sweep is INVISIBLE: every certificate is valid today either way, and
// the fleet reads healthy right up to the day they all expire together. A
// stalled bootstrap sweep is the opposite — the qube it failed is unreachable
// within the minute, and every existing health signal already says so. Building
// a second watchdog for a failure the console already reports would be
// machinery whose own correctness nobody would ever check.
//
// What it does keep is panic containment, for the same reason renewal has it:
// one nil dereference under the sweep would end this goroutine, and then no
// qube created afterwards would EVER be certified, with nothing left in the log
// once the trace scrolls away.

package service

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
)

// DefaultBootstrapInterval is how often uncertified qubes are dialed.
const DefaultBootstrapInterval = time.Minute

// bootstrapRetryBase and bootstrapRetryMax bound the per-qube backoff.
//
// The floor is short because the common failure is simply "cloud-init has not
// finished yet" — the guest is up enough to have an address but not yet
// listening — and that resolves on its own in seconds. The ceiling is low for
// the same reason it is high in renewal, inverted: a qube waiting for its first
// certificate is unusable right now, so an operator watching it wants attempts
// to keep coming rather than to be spaced out politely.
const (
	bootstrapRetryBase = 15 * time.Second
	bootstrapRetryMax  = 10 * time.Minute
)

// bootstrapRefusedRetry is the much longer gap after a REFUSED token.
//
// A refused token cannot become accepted by waiting: it is unknown, expired or
// already spent, and the only cure is re-provisioning the qube with fresh
// user-data. Retrying on the normal backoff would be a guaranteed failure every
// few seconds, and its log line would bury the ones that mean something.
// It is still retried at all, rather than given up on, because re-provisioning
// happens out of band and this sweep must notice when it has.
const bootstrapRefusedRetry = 30 * time.Minute

// QubeBootstrapper issues one qube its first certificate.
// Implemented by *AgentBootstrapper.
type QubeBootstrapper interface {
	Bootstrap(ctx context.Context, qube *models.Qube) BootstrapResult
}

// BootstrapQubes is the slice of the qube repository this needs: read-only,
// and unable to touch a qube's status. Whether a qube holds a certificate says
// nothing about whether its VM is running, and this sweep must not be able to
// conflate the two.
type BootstrapQubes interface {
	ListByStatus(ctx context.Context, statuses []models.QubeStatus) ([]*models.Qube, error)
}

// bootstrapState is what the sweep remembers about one qube between passes.
type bootstrapState struct {
	failures    int
	nextAttempt time.Time
	lastStatus  BootstrapStatus
	lastReason  string
}

// BootstrapMonitor dials running qubes that hold no certificate.
type BootstrapMonitor struct {
	qubes        BootstrapQubes
	certs        AgentCertLister
	bootstrapper QubeBootstrapper
	interval     time.Duration

	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	stop   sync.Once

	mu    sync.Mutex
	state map[string]*bootstrapState

	// afterBootstrap, when set, runs after a qube installs its first certificate.
	// It is where data-disk unlocking hangs: bootstrap is the one event that
	// fires on both first provision AND every resume (a resume re-mints the
	// identity), which is exactly when a freshly built compute VM needs its
	// encrypted disk opened. Optional, so a console without encryption wires
	// nothing.
	afterBootstrap func(context.Context, *models.Qube)

	// now is injectable so backoff can be tested without sleeping.
	now func() time.Time
}

// NewBootstrapMonitor builds the sweep. Call Start to spawn its goroutine.
func NewBootstrapMonitor(
	qubes BootstrapQubes, certs AgentCertLister, bootstrapper QubeBootstrapper, interval time.Duration,
) *BootstrapMonitor {
	base, cancel := context.WithCancel(context.Background())
	return &BootstrapMonitor{
		qubes:        qubes,
		certs:        certs,
		bootstrapper: bootstrapper,
		interval:     interval,
		base:         base,
		cancel:       cancel,
		state:        map[string]*bootstrapState{},
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// WithAfterBootstrap sets the hook run after a qube installs its first
// certificate (see the field's doc). Returns the monitor for chaining.
func (m *BootstrapMonitor) WithAfterBootstrap(fn func(context.Context, *models.Qube)) *BootstrapMonitor {
	m.afterBootstrap = fn
	return m
}

// Start spawns the sweep loop.
func (m *BootstrapMonitor) Start() {
	if m == nil {
		return
	}
	if m.interval <= 0 {
		// Loud, because the symptom appears one qube at a time and looks like a
		// networking problem: the qube is up, has an address, and its agent
		// will not answer. Nothing connects that to a disabled sweep except
		// this line.
		log.Printf("bootstrap: first-certificate issuance is DISABLED " +
			"(set orchestrator.agent_bootstrap_interval_seconds > 0); " +
			"qubes provisioned from here on will boot with a bootstrap token that nobody redeems, " +
			"and their agents will never obtain a certificate")
		return
	}
	if m.bootstrapper == nil || m.qubes == nil || m.certs == nil {
		log.Printf("bootstrap: sweep not started; it is missing a qube list, a certificate registry or a bootstrapper")
		return
	}

	m.wg.Add(1)
	go m.loop()
	log.Printf("bootstrap: dialing uncertified running qubes every %s", m.interval)
}

// Shutdown stops the sweep and waits for it, up to grace.
//
// An abandoned bootstrap is safe to abandon: either the token was never
// redeemed, in which case the next sweep starts over, or it was and the
// certificate is registered, in which case the qube is already certified and
// the next sweep skips it.
func (m *BootstrapMonitor) Shutdown(grace time.Duration) {
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
			log.Printf("bootstrap: shutdown grace of %s elapsed with a bootstrap still in flight; continuing", grace)
		}
	})
}

// loop sweeps on the configured interval.
func (m *BootstrapMonitor) loop() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.interval)
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

// sweepGuarded runs one sweep and refuses to let a panic end bootstrapping.
func (m *BootstrapMonitor) sweepGuarded(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("bootstrap: sweep PANICKED and was contained so it continues on the next tick; "+
				"no qube is being issued a first certificate while this repeats: %v\n%s",
				p, debug.Stack())
		}
	}()
	m.Sweep(ctx)
}

// Sweep dials every running qube that holds no usable certificate.
//
// Running only, like the renewal and health sweeps: a suspended qube has no
// compute instance to dial. A suspended qube that never bootstrapped is
// handled by the resume path, which re-renders its user-data and mints a fresh
// token — dialing a machine that does not exist would only produce a
// guaranteed failure every minute.
func (m *BootstrapMonitor) Sweep(ctx context.Context) {
	qubes, err := m.qubes.ListByStatus(ctx, []models.QubeStatus{models.QubeStatusRunning})
	if err != nil {
		log.Printf("bootstrap: could not list running qubes: %v", err)
		return
	}

	// Bounded so a slow sweep cannot run into the next one. Ticker drops missed
	// ticks rather than queueing them, so without this the sweep would silently
	// degrade to "as often as it can" with nobody told.
	budget := max(m.interval*3/4, 30*time.Second)
	deadline := m.now().Add(budget)

	var candidates, issued, failed, deferred int

	for _, qube := range qubes {
		if ctx.Err() != nil {
			return
		}
		if m.now().After(deadline) {
			// Said out loud: a sweep that quietly stops partway looks identical
			// to one that found nothing left to do.
			log.Printf("bootstrap: sweep budget of %s spent; %d qube(s) not reached this pass", budget, len(qubes)-candidates)
			break
		}
		if qube == nil || !m.needsBootstrap(ctx, qube) {
			continue
		}
		candidates++

		if !m.due(qube.ID) {
			deferred++
			continue
		}

		res := m.bootstrapper.Bootstrap(ctx, qube)
		switch res.Status {
		case BootstrapOK:
			issued++
			m.clear(qube.ID)
			// The qube just proved it holds its identity and is reachable over
			// verified mTLS. If it has an encrypted data disk, this is the moment
			// to push the key and open it — before anything tries to use /data.
			if m.afterBootstrap != nil {
				m.afterBootstrap(ctx, qube)
			}
		case BootstrapAlreadyDone:
			// The agent holds an identity this console's registry does not know
			// about. That should be impossible — registration happens before
			// delivery — so it means the registry lost a row or the guest was
			// rebuilt from a disk image carrying someone else's identity.
			// Either way it will fail every probe, and no retry fixes it.
			m.backOff(qube.ID, res, bootstrapRefusedRetry)
			log.Printf("bootstrap: qube %q reports it already holds an identity, but this console has no "+
				"certificate registered for it; its agent will fail every probe until it is re-provisioned", qube.Name)
		case BootstrapRefused:
			failed++
			m.backOff(qube.ID, res, bootstrapRefusedRetry)
		default:
			failed++
			m.backOff(qube.ID, res, 0)
		}
	}

	if candidates > 0 {
		log.Printf("bootstrap: %d uncertified qube(s): %d issued, %d failed, %d waiting on backoff",
			candidates, issued, failed, deferred)
	}
}

// needsBootstrap reports whether a qube has no unrevoked certificate on record.
//
// It asks the REGISTRY rather than the agent, because the registry is what
// authorizes a connection: a qube with no row there cannot authenticate no
// matter what it holds on disk.
//
// Deliberately NOT newestUsableCert, which renewal uses. That helper does not
// filter by expiry — it is looking for the certificate to REPLACE, so an
// expired one is exactly what it wants to find — and reusing it here would
// have been a quiet mistake in the other direction.
//
// The rule that survives is: revoked rows do not count, expired ones do.
//
//   - Revoked must not count, or resume breaks. Resume revokes every row for
//     the qube and then re-provisions it; if a revoked row still read as "has
//     an identity", the resumed qube would never be dialed and would never get
//     a certificate.
//   - Expired must count, because an expired certificate means the guest has
//     an identity ON DISK, so its agent came up in normal mode and does not
//     serve the bootstrap calls at all. Dialing it would fail every pass. That
//     qube is renewal's problem, or a rebuild — not this sweep's.
func (m *BootstrapMonitor) needsBootstrap(ctx context.Context, qube *models.Qube) bool {
	list, err := m.certs.ListByQube(ctx, qube.ID)
	if err != nil {
		// Unknown is not the same as absent. Bootstrapping on a failed read
		// would spend the qube's token against a console that cannot currently
		// tell whether it already has a certificate, and the token is
		// single-use — so the safe direction is to skip and try next sweep.
		log.Printf("bootstrap: could not read %q's certificates, skipping it this pass: %v", qube.Name, err)
		return false
	}
	for _, c := range list {
		if c != nil && !c.Revoked() {
			return false
		}
	}
	return true
}

// due reports whether a qube may be attempted now.
func (m *BootstrapMonitor) due(qubeID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	st, ok := m.state[qubeID]
	if !ok {
		return true
	}
	return !m.now().Before(st.nextAttempt)
}

// backOff records a failure and schedules the next attempt.
//
// fixed, when non-zero, overrides the exponential schedule — used for failures
// that cannot resolve by waiting.
func (m *BootstrapMonitor) backOff(qubeID string, res BootstrapResult, fixed time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	st, ok := m.state[qubeID]
	if !ok {
		st = &bootstrapState{}
		m.state[qubeID] = st
	}
	st.failures++
	st.lastStatus = res.Status
	st.lastReason = res.Reason

	wait := fixed
	if wait == 0 {
		wait = bootstrapRetryBase << min(st.failures-1, 16)
		if wait > bootstrapRetryMax || wait <= 0 {
			wait = bootstrapRetryMax
		}
	}
	st.nextAttempt = m.now().Add(wait)
}

// clear forgets a qube's failure history after a success.
func (m *BootstrapMonitor) clear(qubeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.state, qubeID)
}

// Failure returns the last bootstrap failure recorded for a qube, for callers
// that surface it alongside agent health. Empty when there is none.
func (m *BootstrapMonitor) Failure(qubeID string) (BootstrapStatus, string) {
	if m == nil {
		return "", ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	st, ok := m.state[qubeID]
	if !ok {
		return "", ""
	}
	return st.lastStatus, st.lastReason
}

// String makes a state readable in test failures.
func (s *bootstrapState) String() string {
	return fmt.Sprintf("failures=%d next=%s status=%s", s.failures, s.nextAttempt.Format(time.RFC3339), s.lastStatus)
}
