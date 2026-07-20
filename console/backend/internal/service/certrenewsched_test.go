package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles -----------------------------------------------------------

// fakeRenewer records which qubes were asked to renew and what happened.
type fakeRenewer struct {
	mu    sync.Mutex
	calls []string
	// fn decides the outcome of attempt n (0-based) for a qube.
	fn func(n int, q *models.Qube) CertRenewalResult
}

func (f *fakeRenewer) Renew(_ context.Context, q *models.Qube) CertRenewalResult {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, q.ID)
	f.mu.Unlock()

	if f.fn == nil {
		return CertRenewalResult{QubeID: q.ID, QubeName: q.Name, Status: CertRenewalOK}
	}
	res := f.fn(n, q)
	res.QubeID, res.QubeName = q.ID, q.Name
	return res
}

func (f *fakeRenewer) renewed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// healthWrite is one recorded agent-health update.
type healthWrite struct {
	qubeID  string
	health  models.AgentHealth
	failure string
}

type fakeHealthWriter struct {
	mu     sync.Mutex
	writes []healthWrite
}

func (f *fakeHealthWriter) UpdateAgentHealth(
	_ context.Context, id string, health models.AgentHealth, _ time.Time, failure string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, healthWrite{qubeID: id, health: health, failure: failure})
	return nil
}

func (f *fakeHealthWriter) last() (healthWrite, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) == 0 {
		return healthWrite{}, false
	}
	return f.writes[len(f.writes)-1], true
}

// --- helpers ----------------------------------------------------------------

// certFor builds a registry row for a qube holding a standard-lifetime
// certificate that expires in `remaining`.
func certFor(qubeID string, remaining time.Duration, now time.Time) *repository.AgentCert {
	expires := now.Add(remaining)
	return &repository.AgentCert{
		Fingerprint: qubeID + "-fp",
		QubeID:      qubeID,
		SubjectCN:   "agent-" + qubeID,
		IssuedAt:    expires.Add(-pki.DefaultAgentCertLifetime),
		ExpiresAt:   &expires,
	}
}

func renewableQube(id string) *models.Qube {
	return &models.Qube{ID: id, Name: id, IPAddress: "10.0.0.9", Status: models.QubeStatusRunning}
}

// newTestMonitor wires a monitor over a frozen clock the test controls.
func newTestMonitor(
	now *time.Time, qubes CertRenewalQubes, certs AgentCertLister,
	renewer QubeCertRenewer, health AgentHealthWriter,
) *CertRenewalMonitor {
	m := NewCertRenewalMonitor(qubes, certs, renewer, health, CertRenewalConfig{
		Interval:  time.Hour,
		Threshold: DefaultCertRenewalThreshold,
	})
	m.now = func() time.Time { return *now }
	return m
}

// --- tests ------------------------------------------------------------------

// TestRenewsAtTheThresholdAndNotBefore — the scheduling rule itself. A
// certificate is renewed once less than a third of its lifetime remains, and a
// fresh one is left alone: renewing on every sweep would sign a new certificate
// every hour and fill the registry with rows nobody uses.
func TestRenewsAtTheThresholdAndNotBefore(t *testing.T) {
	const lifetime = pki.DefaultAgentCertLifetime // 90 days
	now := time.Now().UTC()

	// dueAt is computed per qube, so derive the boundary for THIS id rather than
	// assuming the un-jittered one — that is the point of the jitter.
	const id = "q-threshold"
	cert := certFor(id, lifetime, now)
	due := renewalDueAt(id, cert, DefaultCertRenewalThreshold)

	// Sanity on the window itself: renewal starts inside the last third — bar the
	// fixed clock-skew margin, which deliberately brings every qube forward — and
	// leaves at least three weeks of runway even for the latest-jittered qube.
	remainingAtDue := cert.ExpiresAt.Sub(due)
	assert.LessOrEqual(t, remainingAtDue, lifetime/3+certRenewalClockSkewMargin)
	assert.GreaterOrEqual(t, remainingAtDue, 22*24*time.Hour,
		"even the last qube in the jitter window needs weeks of retries before expiry")

	t.Run("not before", func(t *testing.T) {
		clock := due.Add(-time.Hour)
		renewer := &fakeRenewer{}
		m := newTestMonitor(&clock,
			&fakeQubeLister{qubes: []*models.Qube{renewableQube(id)}},
			newFakeRegistry(cert), renewer, &fakeHealthWriter{})

		m.Sweep(context.Background())
		assert.Empty(t, renewer.renewed(), "a certificate with more than a third of its life left is fresh")
	})

	t.Run("at the threshold", func(t *testing.T) {
		clock := due.Add(time.Minute)
		renewer := &fakeRenewer{}
		m := newTestMonitor(&clock,
			&fakeQubeLister{qubes: []*models.Qube{renewableQube(id)}},
			newFakeRegistry(cert), renewer, &fakeHealthWriter{})

		m.Sweep(context.Background())
		assert.Equal(t, []string{id}, renewer.renewed())
	})
}

// TestJitterSpreadsAFleet — every qube in a rollout is issued its certificate
// within the same few minutes. Without jitter every one of them crosses the
// threshold in the same HOUR ~60 days later: the whole fleet renews at once, and
// any systematic breakage is discovered everywhere simultaneously with nothing
// having gone first to warn anyone.
func TestJitterSpreadsAFleet(t *testing.T) {
	const (
		lifetime = pki.DefaultAgentCertLifetime
		fleet    = 200
	)
	now := time.Now().UTC()

	// One rollout: identical issuance and expiry for every qube.
	dues := make([]time.Time, 0, fleet)
	for i := 0; i < fleet; i++ {
		id := "qube-" + string(rune('a'+i%26)) + "-" + time.Duration(i).String()
		dues = append(dues, renewalDueAt(id, certFor(id, lifetime, now), DefaultCertRenewalThreshold))
	}

	earliest, latest := dues[0], dues[0]
	for _, d := range dues {
		if d.Before(earliest) {
			earliest = d
		}
		if d.After(latest) {
			latest = d
		}
	}

	spread := latest.Sub(earliest)
	// The window is a quarter of the 30-day renewal window: ~7.5 days. A fleet
	// this size should cover most of it; anything under a day would mean the
	// fleet still renews in effectively one go.
	assert.Greater(t, spread, 5*24*time.Hour,
		"a rollout must not renew in one hour: spread was %s", spread)
	assert.LessOrEqual(t, spread, 8*24*time.Hour,
		"jitter must stay inside the renewal window, never past expiry")

	// No two qubes in the same hour is too strong for 200 qubes over 7.5 days,
	// but no more than a handful should collide in any one hour.
	buckets := map[int64]int{}
	worst := 0
	for _, d := range dues {
		b := d.Unix() / 3600
		buckets[b]++
		if buckets[b] > worst {
			worst = buckets[b]
		}
	}
	assert.Less(t, worst, fleet/4, "renewals are still bunched: %d of %d in one hour", worst, fleet)
}

// TestJitterIsStableAcrossRestarts — a random draw per process would re-bunch
// the fleet after a console restart, which is exactly when a rollout is most
// likely to have just happened.
func TestJitterIsStableAcrossRestarts(t *testing.T) {
	a := jitterFraction("qube-7")
	b := jitterFraction("qube-7")
	assert.Equal(t, a, b)
	assert.NotEqual(t, jitterFraction("qube-7"), jitterFraction("qube-8"))
	for _, id := range []string{"", "a", "qube-7", "a-very-long-qube-identifier-0123456789"} {
		f := jitterFraction(id)
		assert.GreaterOrEqual(t, f, 0.0)
		assert.Less(t, f, 1.0)
	}
}

// TestFailureIsRecordedAndRetried — the whole point of the feature. A failed
// renewal must be visible in the agent-health fields, must not be retried
// immediately (which would hammer a dead qube every sweep), and must be retried
// once the backoff elapses.
func TestFailureIsRecordedAndRetried(t *testing.T) {
	const id = "q-fail"
	now := time.Now().UTC()
	cert := certFor(id, 10*24*time.Hour, now)
	clock := now

	renewer := &fakeRenewer{fn: func(n int, _ *models.Qube) CertRenewalResult {
		if n < 2 {
			return CertRenewalResult{
				Status:           CertRenewalConsoleFailed,
				Reason:           "the CA refused to sign",
				PreviousNotAfter: *cert.ExpiresAt,
			}
		}
		return CertRenewalResult{Status: CertRenewalOK}
	}}
	health := &fakeHealthWriter{}
	m := newTestMonitor(&clock,
		&fakeQubeLister{qubes: []*models.Qube{renewableQube(id)}},
		newFakeRegistry(cert), renewer, health)

	// First sweep: fails, and the failure reaches the health fields.
	m.Sweep(context.Background())
	require.Len(t, renewer.renewed(), 1)

	w, ok := health.last()
	require.True(t, ok)
	assert.Equal(t, models.AgentHealthHealthy, w.health,
		"the agent answered; blaming the qube would send an operator to the one machine that is fine")
	assert.Contains(t, w.failure, certRenewalWarningPrefix)
	assert.Contains(t, w.failure, "the CA refused to sign")
	assert.Contains(t, w.failure, "day(s)", "an operator needs to know how long is left, not just that it failed")

	// Immediately afterwards: still inside the backoff, so no second attempt.
	m.Sweep(context.Background())
	assert.Len(t, renewer.renewed(), 1, "a failing qube must not be retried on every sweep")

	// The warning survives in the meantime — this is what the probe path
	// re-publishes so a successful ping cannot erase it.
	assert.Contains(t, m.RenewalWarning(id), certRenewalWarningPrefix)

	// Once the backoff elapses it is retried, fails again, and backs off further.
	clock = clock.Add(certRenewalRetryBase + time.Minute)
	m.Sweep(context.Background())
	require.Len(t, renewer.renewed(), 2)

	clock = clock.Add(certRenewalRetryBase + time.Minute)
	m.Sweep(context.Background())
	assert.Len(t, renewer.renewed(), 2, "the second failure must back off further than the first")

	// After the longer backoff it succeeds, and the warning clears.
	clock = clock.Add(2 * certRenewalRetryBase)
	m.Sweep(context.Background())
	require.Len(t, renewer.renewed(), 3)
	assert.Empty(t, m.RenewalWarning(id), "a renewed qube must stop reporting a stale warning")

	w, ok = health.last()
	require.True(t, ok)
	assert.Equal(t, models.AgentHealthHealthy, w.health)
	assert.Empty(t, w.failure)
}

// TestUnreachableRenewalRecordsUnreachable — a renewal that never reached the
// agent is the same diagnosis a failed probe would reach from the same evidence,
// and must not be dressed up as a healthy qube with a paperwork problem.
func TestUnreachableRenewalRecordsUnreachable(t *testing.T) {
	const id = "q-down"
	now := time.Now().UTC()
	cert := certFor(id, 5*24*time.Hour, now)
	clock := now

	health := &fakeHealthWriter{}
	m := newTestMonitor(&clock,
		&fakeQubeLister{qubes: []*models.Qube{renewableQube(id)}},
		newFakeRegistry(cert),
		&fakeRenewer{fn: func(int, *models.Qube) CertRenewalResult {
			return CertRenewalResult{Status: CertRenewalUnreachable, Reason: "nothing is listening"}
		}}, health)

	m.Sweep(context.Background())

	w, ok := health.last()
	require.True(t, ok)
	assert.Equal(t, models.AgentHealthUnreachable, w.health)
	assert.Contains(t, w.failure, certRenewalWarningPrefix)
}

// TestNotConfiguredLeavesHealthAlone — a console that cannot renew has learned
// nothing about the agent. Writing its own misconfiguration over a real probe
// result would destroy the reading instead of adding to it.
func TestNotConfiguredLeavesHealthAlone(t *testing.T) {
	const id = "q-noca"
	now := time.Now().UTC()
	cert := certFor(id, 5*24*time.Hour, now)
	clock := now

	health := &fakeHealthWriter{}
	m := newTestMonitor(&clock,
		&fakeQubeLister{qubes: []*models.Qube{renewableQube(id)}},
		newFakeRegistry(cert),
		&fakeRenewer{fn: func(int, *models.Qube) CertRenewalResult {
			return CertRenewalResult{Status: CertRenewalNotConfigured, Reason: "no CA"}
		}}, health)

	m.Sweep(context.Background())

	_, ok := health.last()
	assert.False(t, ok, "an unconfigured console must not overwrite a real health reading")
}

// TestRenewalWarningSurvivesAHealthyProbe — the failure this whole feature is
// built to prevent. recordAgentHealth rewrites agent_last_error on every sweep,
// so a renewal failure recorded once would be erased within a minute by the next
// successful ping and the fleet would read healthy until the certificates ran
// out.
func TestRenewalWarningSurvivesAHealthyProbe(t *testing.T) {
	const id = "q-warn"
	now := time.Now().UTC()
	cert := certFor(id, 12*24*time.Hour, now)
	clock := now

	m := newTestMonitor(&clock,
		&fakeQubeLister{qubes: []*models.Qube{renewableQube(id)}},
		newFakeRegistry(cert),
		&fakeRenewer{fn: func(int, *models.Qube) CertRenewalResult {
			return CertRenewalResult{
				Status: CertRenewalInstallFailed, Reason: "agent refused to install",
				PreviousNotAfter: *cert.ExpiresAt,
			}
		}}, &fakeHealthWriter{})
	m.Sweep(context.Background())

	// A successful probe recorded through the production path must still carry
	// the renewal warning.
	repo := &recordingQubeRepo{}
	svc := &QubeServiceImpl{qubeRepo: repo, renewals: m}
	svc.recordAgentHealth(context.Background(), renewableQube(id),
		AgentProbeResult{Reachable: true, Status: AgentProbeOK, Authoritative: true, CheckedAt: now},
		AgentProbeSteady)

	require.Len(t, repo.writes, 1)
	assert.Equal(t, models.AgentHealthHealthy, repo.writes[0].health)
	assert.Contains(t, repo.writes[0].failure, certRenewalWarningPrefix,
		"a healthy ping must not erase the fact that this qube's certificate is not being renewed")
}

// TestBackoffIsBounded — doubling without a cap would push a qube that was
// unreachable for a week out to a retry measured in days, so it would keep
// failing long after the fault was fixed; an unclamped shift would overflow to a
// negative duration and retry forever instead.
func TestBackoffIsBounded(t *testing.T) {
	assert.Equal(t, certRenewalRetryBase, backoffFor(1))
	assert.Equal(t, 2*certRenewalRetryBase, backoffFor(2))
	assert.Equal(t, certRenewalRetryMax, backoffFor(100))
	for _, n := range []int{-5, 0, 1, 7, 64, 1 << 20} {
		d := backoffFor(n)
		assert.Greater(t, d, time.Duration(0), "backoff must never be zero or negative for n=%d", n)
		assert.LessOrEqual(t, d, certRenewalRetryMax)
	}
}

// TestCertWithoutExpiryIsDueImmediately — an unknown expiry cannot be reasoned
// about. Treating it as fresh would silently exclude the row from renewal
// forever; an unnecessary renewal costs one signature.
func TestCertWithoutExpiryIsDueImmediately(t *testing.T) {
	assert.True(t, renewalDueAt("q", &repository.AgentCert{}, DefaultCertRenewalThreshold).IsZero())
	assert.True(t, renewalDueAt("q", nil, DefaultCertRenewalThreshold).IsZero())
}

// TestMissingIssuedAtAssumesTheStandardLifetime — an old row or a clock that
// went backwards must not make a certificate look infinitely fresh.
func TestMissingIssuedAtAssumesTheStandardLifetime(t *testing.T) {
	now := time.Now().UTC()
	expires := now.Add(pki.DefaultAgentCertLifetime)
	cert := &repository.AgentCert{QubeID: "q", ExpiresAt: &expires} // IssuedAt zero

	due := renewalDueAt("q", cert, DefaultCertRenewalThreshold)
	assert.True(t, due.After(now), "a fresh certificate is not due yet even without an issuance time")
	assert.True(t, due.Before(expires.Add(-20*24*time.Hour)),
		"but it must still fall inside a renewal window rather than never")
}

// TestQubeWithoutCertificateIsSkipped — nothing to renew, and it must not be
// mistaken for a renewal failure: that is the issuer's problem and is already
// visible as a failing agent probe.
func TestQubeWithoutCertificateIsSkipped(t *testing.T) {
	clock := time.Now().UTC()
	renewer := &fakeRenewer{}
	health := &fakeHealthWriter{}
	m := newTestMonitor(&clock,
		&fakeQubeLister{qubes: []*models.Qube{renewableQube("q-bare")}},
		newFakeRegistry(), renewer, health)

	m.Sweep(context.Background())

	assert.Empty(t, renewer.renewed())
	_, ok := health.last()
	assert.False(t, ok)
}

// TestSweepSurvivesAListFailure — a database hiccup must not take the sweep
// loop down with it.
func TestSweepSurvivesAListFailure(t *testing.T) {
	clock := time.Now().UTC()
	renewer := &fakeRenewer{}
	m := newTestMonitor(&clock,
		&fakeQubeLister{err: errors.New("database is locked")},
		newFakeRegistry(), renewer, &fakeHealthWriter{})

	m.Sweep(context.Background())
	assert.Empty(t, renewer.renewed())
}

// TestDisabledMonitorDoesNothing — Start with a non-positive interval must not
// spawn a sweep loop, and Shutdown must still return.
func TestDisabledMonitorDoesNothing(t *testing.T) {
	m := NewCertRenewalMonitor(&fakeQubeLister{}, newFakeRegistry(), &fakeRenewer{}, &fakeHealthWriter{},
		CertRenewalConfig{Interval: 0})
	m.Start()
	m.Shutdown(time.Second)

	var nilMonitor *CertRenewalMonitor
	nilMonitor.Start()
	nilMonitor.Shutdown(time.Second)
	assert.Empty(t, nilMonitor.RenewalWarning("anything"))
}

// TestThresholdFallsBackOnNonsense — a misconfigured percentage must not
// silently mean "never renew" (threshold 0) or "renew constantly" (threshold 1).
func TestThresholdFallsBackOnNonsense(t *testing.T) {
	for _, bad := range []float64{0, -1, 1, 4} {
		m := NewCertRenewalMonitor(&fakeQubeLister{}, newFakeRegistry(), &fakeRenewer{}, nil,
			CertRenewalConfig{Interval: time.Hour, Threshold: bad})
		assert.InDelta(t, DefaultCertRenewalThreshold, m.cfg.Threshold, 1e-9)
	}
}

// --- the runway arithmetic --------------------------------------------------

// TestGuaranteedRunwayIsNotTheRenewalWindow — the number three separate comments
// used to get wrong.
//
// Jitter is added FORWARD from notAfter−W, so it can only ever delay a renewal.
// The 30-day window on a 90-day certificate therefore guarantees 22.5 days, not
// 30. That gap is not academic: it is the margin somebody consults before
// lowering the threshold, and a comment claiming 30 is how the real number gets
// discovered in production.
//
// This test pins the arithmetic so the claim and the code cannot drift apart
// again — including the direction, which is what a centered jitter would change.
func TestGuaranteedRunwayIsNotTheRenewalWindow(t *testing.T) {
	const lifetime = pki.DefaultAgentCertLifetime
	window := time.Duration(float64(lifetime) * DefaultCertRenewalThreshold)

	assert.InDelta(t, (30 * 24 * time.Hour).Hours(), window.Hours(), 1,
		"fixture check: a third of 90 days is the 30-day window everyone quotes")

	runway := renewalRunway(lifetime, DefaultCertRenewalThreshold)
	assert.Less(t, runway, window, "jitter is forward-only, so the runway is SHORTER than the window")
	// 22.5 days of jitter loss, plus the day the skew margin gives back.
	assert.InDelta(t, (23*24*time.Hour + 12*time.Hour).Hours(), runway.Hours(), 1,
		"the guaranteed runway is 23.5 days (22.5 from the forward-only jitter, +1 skew margin), not 30")

	// And the same number reached from the other direction: the worst-placed qube
	// in a real fleet must not do better than renewalRunway claims.
	now := time.Now().UTC()
	worst := window
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("runway-%d", i)
		cert := certFor(id, lifetime, now)
		if left := cert.ExpiresAt.Sub(renewalDueAt(id, cert, DefaultCertRenewalThreshold)); left < worst {
			worst = left
		}
	}
	assert.GreaterOrEqual(t, worst, runway,
		"no qube may get less runway than renewalRunway promises")
	assert.Less(t, worst, window,
		"and some qube must actually be near the worst case, or the jitter is not spreading")
}

// TestSkewMarginCannotConsumeAShortWindow — the threshold is a fraction so that
// shortening the certificate lifetime shortens the renewal period with it. A
// fixed one-day skew margin would break that promise at the small end: on a
// three-day certificate the window is one day, and an unclamped margin would
// make every certificate due the moment it was issued — a console signing a new
// one every sweep, forever.
func TestSkewMarginCannotConsumeAShortWindow(t *testing.T) {
	now := time.Now().UTC()
	short := 3 * 24 * time.Hour
	expires := now.Add(short)
	cert := &repository.AgentCert{
		QubeID: "q", IssuedAt: now, ExpiresAt: &expires,
	}

	due := renewalDueAt("q", cert, DefaultCertRenewalThreshold)
	assert.True(t, due.After(now),
		"a freshly issued short-lived certificate must not be born already due")
	assert.True(t, due.Before(expires), "but it must still come due before it expires")

	assert.Zero(t, skewMarginFor(0))
	assert.LessOrEqual(t, skewMarginFor(time.Hour), time.Hour/8)
	assert.Equal(t, certRenewalClockSkewMargin, skewMarginFor(30*24*time.Hour),
		"the full margin applies on a window wide enough to absorb it")
}

// --- liveness ---------------------------------------------------------------

// TestStalledSweepsAreVisible — the failure this reporting exists for.
//
// A renewal sweep that stopped running produces exactly the same observations as
// a fleet where nothing is due: no errors, no failing probes, no health field
// changes, every certificate still valid. Nothing else in the console can tell
// the two apart, and the difference only becomes visible on the day every
// certificate expires at once.
func TestStalledSweepsAreVisible(t *testing.T) {
	clock := time.Now().UTC()
	m := newTestMonitor(&clock,
		&fakeQubeLister{qubes: []*models.Qube{renewableQube("q-live")}},
		newFakeRegistry(certFor("q-live", 80*24*time.Hour, clock)),
		&fakeRenewer{}, &fakeHealthWriter{})

	m.Start()
	defer m.Shutdown(time.Second)

	m.Sweep(context.Background())
	l := m.Liveness()
	require.True(t, l.Enabled)
	assert.False(t, l.Stalled, "a monitor that just swept is not stalled")
	assert.Equal(t, int64(1), l.Sweeps)
	assert.Positive(t, l.Runway, "the stall report must carry how long the stall may safely last")

	// Three intervals later with nothing having run, which is the shape of a loop
	// that died rather than one that is merely slow.
	clock = clock.Add(4 * time.Hour)
	l = m.Liveness()
	assert.True(t, l.Stalled)
	assert.Contains(t, l.StallReason, "last sweep finished")

	// And it clears by itself when sweeping resumes: a stall report that stuck
	// would be ignored within a week.
	m.Sweep(context.Background())
	assert.False(t, m.Liveness().Stalled)
}

// TestWedgedSweepIsNotConfusedWithADeadLoop — three ways of not sweeping, three
// different investigations. A sweep that went in and never came out points at a
// repository call with no deadline; a loop that is simply gone points at a
// panic; sweeps that run but cannot read the fleet point at the database. An
// operator handed the wrong one of these looks in the wrong place for hours.
func TestWedgedSweepIsNotConfusedWithADeadLoop(t *testing.T) {
	now := time.Now().UTC()
	const interval = time.Hour
	base := CertRenewalLiveness{Enabled: true, StartedAt: now.Add(-24 * time.Hour)}

	t.Run("a sweep that never returned", func(t *testing.T) {
		l := base
		l.LastSweepStart = now.Add(-5 * time.Hour)
		l.LastSweepEnd = now.Add(-6 * time.Hour)
		stalled, reason := renewalStalled(l, interval, now)
		assert.True(t, stalled)
		assert.Contains(t, reason, "wedged")
	})

	t.Run("a loop that stopped between sweeps", func(t *testing.T) {
		l := base
		l.LastSweepStart = now.Add(-5 * time.Hour)
		l.LastSweepEnd = now.Add(-5 * time.Hour)
		stalled, reason := renewalStalled(l, interval, now)
		assert.True(t, stalled)
		assert.Contains(t, reason, "last sweep finished")
		assert.NotContains(t, reason, "wedged")
	})

	t.Run("a loop that never swept at all", func(t *testing.T) {
		stalled, reason := renewalStalled(base, interval, now)
		assert.True(t, stalled)
		assert.Contains(t, reason, "no sweep has completed")
	})

	t.Run("sweeping normally", func(t *testing.T) {
		l := base
		l.LastSweepStart = now.Add(-30 * time.Minute)
		l.LastSweepEnd = now.Add(-29 * time.Minute)
		l.LastFleetRead = l.LastSweepEnd
		stalled, _ := renewalStalled(l, interval, now)
		assert.False(t, stalled)
	})

	t.Run("a slow sweep still in flight is not yet a stall", func(t *testing.T) {
		l := base
		l.LastSweepStart = now.Add(-90 * time.Minute)
		l.LastSweepEnd = now.Add(-3 * time.Hour)
		stalled, _ := renewalStalled(l, interval, now)
		assert.False(t, stalled, "a sweep on a large fleet may legitimately outrun one interval")
	})

	t.Run("renewal that is switched off is not a stall", func(t *testing.T) {
		l := base
		l.Enabled = false
		stalled, _ := renewalStalled(l, interval, now)
		assert.False(t, stalled, "a disabled monitor is a configuration statement, and Start already says so")
	})
}

// TestSweepsThatCannotSeeTheFleetAreStalled — the quietest of the three. The
// loop is alive, every sweep completes on schedule, and not one certificate is
// being renewed because the qube list has been failing for hours. Each
// individual failure is logged and each is easy to scroll past.
func TestSweepsThatCannotSeeTheFleetAreStalled(t *testing.T) {
	clock := time.Now().UTC()
	m := newTestMonitor(&clock,
		&fakeQubeLister{err: errors.New("database is locked")},
		newFakeRegistry(), &fakeRenewer{}, &fakeHealthWriter{})
	m.Start()
	defer m.Shutdown(time.Second)

	m.Sweep(context.Background())
	assert.False(t, m.Liveness().Stalled, "one failed listing is not a stall")

	clock = clock.Add(4 * time.Hour)
	m.Sweep(context.Background())

	l := m.Liveness()
	assert.True(t, l.Stalled, "sweeps running blind are not sweeps")
	assert.Contains(t, l.StallReason, "not been readable")
	assert.Contains(t, l.StallReason, "database is locked")

	// Recovery clears it without anyone intervening.
	m.qubes = &fakeQubeLister{}
	m.Sweep(context.Background())
	assert.False(t, m.Liveness().Stalled)
}

// TestAPanickingSweepDoesNotEndRenewal — driven through the REAL loop, because
// the property is about the goroutine surviving, not about a helper returning.
//
// Without containment, one nil dereference anywhere under Sweep kills the sweep
// goroutine and renewal never happens again: no log after the trace scrolls
// away, no health field changes, every certificate still valid, and the fleet
// goes dark together three months later. It is the single worst way this
// component can fail.
func TestAPanickingSweepDoesNotEndRenewal(t *testing.T) {
	const id = "q-panic"
	now := time.Now().UTC()
	cert := certFor(id, 10*24*time.Hour, now)

	renewer := &fakeRenewer{fn: func(n int, _ *models.Qube) CertRenewalResult {
		if n == 0 {
			panic("a repository returned something nobody expected")
		}
		return CertRenewalResult{Status: CertRenewalOK}
	}}

	m := NewCertRenewalMonitor(
		&fakeQubeLister{qubes: []*models.Qube{renewableQube(id)}},
		newFakeRegistry(cert), renewer, &fakeHealthWriter{},
		CertRenewalConfig{Interval: 20 * time.Millisecond, Threshold: DefaultCertRenewalThreshold})

	m.Start()
	defer m.Shutdown(2 * time.Second)

	require.Eventually(t, func() bool { return len(renewer.renewed()) >= 2 }, 3*time.Second, 10*time.Millisecond,
		"the sweep loop must survive a panic and keep renewing")

	l := m.Liveness()
	assert.Equal(t, int64(1), l.Panics, "a contained panic must still be counted, not swallowed")
	assert.Contains(t, l.LastPanic, "nobody expected")
	assert.False(t, l.LastPanicAt.IsZero())
}

// TestLivenessOnANilOrDisabledMonitor — the accessor is meant for a health
// endpoint, which will call it before anything is wired.
func TestLivenessOnANilOrDisabledMonitor(t *testing.T) {
	var nilMonitor *CertRenewalMonitor
	assert.False(t, nilMonitor.Liveness().Enabled)

	m := NewCertRenewalMonitor(&fakeQubeLister{}, newFakeRegistry(), &fakeRenewer{}, nil,
		CertRenewalConfig{Interval: 0})
	m.Start()
	l := m.Liveness()
	assert.False(t, l.Enabled)
	assert.False(t, l.Stalled, "renewal that was never switched on must not be reported as a stall")
}

// --- the real renewer, driven by the real scheduler --------------------------

// exchangeRenewer runs the REAL CertRenewer protocol — signing, the purge guard,
// registration, the uninstalled-certificate cleanup — against a fake agent and a
// real registry.
//
// It exists because fakeRenewer is simpler than production in the one way that
// matters: it never writes to the registry. That is exactly how defect C2
// survived a green suite — a failed install left a longer-lived row that made
// the scheduler treat the qube as permanently fresh, and no test could see it
// because no test's renewer ever registered anything.
type exchangeRenewer struct {
	inner *CertRenewer
	agent agentCaller
}

func (e *exchangeRenewer) Renew(ctx context.Context, q *models.Qube) CertRenewalResult {
	res := CertRenewalResult{QubeID: q.ID, QubeName: q.Name, At: time.Now().UTC()}
	peer := &verifiedPeer{}
	if prev := e.inner.currentCert(ctx, q.ID); prev != nil {
		// Standing in for the handshake, which is the only part skipped here.
		peer.note(prev.Fingerprint)
		res.OldFingerprint = prev.Fingerprint
		if prev.ExpiresAt != nil {
			res.PreviousNotAfter = *prev.ExpiresAt
		}
	}
	done := func(status CertRenewalStatus, format string, args ...any) CertRenewalResult {
		res.Status = status
		if format != "" {
			res.Reason = fmt.Sprintf(format, args...)
		}
		return res
	}
	return e.inner.exchange(ctx, q, e.agent, peer, &res, done)
}

// TestSchedulerRetriesAfterAFailedInstallAgainstTheRealRegistry — defect C2,
// end to end, through objects that actually write rows.
//
// Registration happens before installation on purpose (the reverse order locks a
// qube out of its own fleet), so a failed install leaves a row expiring 90 days
// out while the agent still holds one expiring in weeks. The scheduler picks the
// longest-lived unrevoked certificate — so without the cleanup it sees that
// orphan, concludes the qube was just renewed, and never tries again.
func TestSchedulerRetriesAfterAFailedInstallAgainstTheRealRegistry(t *testing.T) {
	certs := repository.NewAgentCertRepository(certTestDB(t))
	qube := renewableQube("q-c2")
	ctx := context.Background()

	// The certificate the agent is holding: 20 days left on a 90-day lifetime, so
	// comfortably inside the renewal window.
	expires := time.Now().Add(20 * 24 * time.Hour).UTC()
	// The agent has been handshaking with this certificate, so the registry has
	// SEEN it. Modeling that is not decoration: dueness is computed from the
	// certificate the agent is observed to be using, precisely so an orphan the
	// agent never received cannot masquerade as a fresh renewal. A test where
	// nothing was ever seen is a state production does not reach.
	oldFP := "f" + strings.Repeat("5", 63)
	require.NoError(t, certs.Register(ctx, &repository.AgentCert{
		Fingerprint: oldFP, QubeID: qube.ID,
		SubjectCN: AgentCommonName(qube.Name),
		IssuedAt:  expires.Add(-pki.DefaultAgentCertLifetime), ExpiresAt: &expires,
	}))

	agent := &fakeAgent{
		nonce: "n1", csrPEM: makeCSR(t, AgentCommonName(qube.Name)),
		completeErr: errors.New("tunnel dropped mid-install"),
	}
	renewer := &exchangeRenewer{
		inner: NewCertRenewer(nil, &fakeSigner{}, certs, certs, "0.0.0.0:8443", time.Second),
		agent: agent,
	}

	clock := time.Now().UTC()
	m := newTestMonitor(&clock, &fakeQubeLister{qubes: []*models.Qube{qube}}, certs, renewer, &fakeHealthWriter{})

	// Sweep one: signed, registered, install failed.
	m.Sweep(ctx)
	assert.Contains(t, m.RenewalWarning(qube.ID), certRenewalWarningPrefix)

	require.NoError(t, certs.TouchLastSeen(ctx, oldFP))

	list, err := certs.ListByQube(ctx, qube.ID)
	require.NoError(t, err)
	require.Len(t, list, 2, "the certificate WAS registered before the install was attempted")
	assert.NotNil(t, newestUsableCert(list))
	assert.Equal(t, 20*24*time.Hour.Round(time.Hour),
		time.Until(*newestUsableCert(list).ExpiresAt).Round(time.Hour),
		"an orphan the agent never received must not outrank the certificate it is actually using")

	// Sweep two, after the backoff, with the tunnel healthy again.
	agent.mu.Lock()
	agent.completeErr = nil
	agent.mu.Unlock()
	clock = clock.Add(certRenewalRetryBase + time.Minute)
	m.Sweep(ctx)

	assert.Empty(t, m.RenewalWarning(qube.ID), "a qube that renewed must stop warning")
	list, err = certs.ListByQube(ctx, qube.ID)
	require.NoError(t, err)
	best := newestUsableCert(list)
	require.NotNil(t, best)
	assert.Greater(t, time.Until(*best.ExpiresAt), 80*24*time.Hour,
		"the qube now holds a freshly renewed certificate")

	// And it is no longer due, so the console does not sign one every hour.
	clock = clock.Add(time.Hour)
	before := len(agent.order())
	m.Sweep(ctx)
	assert.Equal(t, before, len(agent.order()), "a renewed qube must go quiet")
}

// TestSchedulerReportsAPurgeRaceDistinctlyFromAConsoleFault — through the real
// renewer and the real registry, because the distinction is made by a SQL
// statement and reported by the scheduler; a fake in between could only prove
// the two halves agree with the fake.
func TestSchedulerReportsAPurgeRaceDistinctlyFromAConsoleFault(t *testing.T) {
	certs := repository.NewAgentCertRepository(certTestDB(t))
	qube := renewableQube("q-purged")
	ctx := context.Background()

	expires := time.Now().Add(15 * 24 * time.Hour).UTC()
	fp := "g" + strings.Repeat("6", 63)
	require.NoError(t, certs.Register(ctx, &repository.AgentCert{
		Fingerprint: fp, QubeID: qube.ID, SubjectCN: AgentCommonName(qube.Name),
		IssuedAt: expires.Add(-pki.DefaultAgentCertLifetime), ExpiresAt: &expires,
	}))

	// The purge lands after the agent has authenticated and answered BeginRenewal,
	// and before the console writes the new row. That interval is the entire race
	// the guard exists to close — purging earlier would simply fail the
	// handshake, which is already covered by TestRevokedCertCannotRenewItself.
	renewer := &exchangeRenewer{
		inner: NewCertRenewer(nil, &fakeSigner{}, certs, certs, "0.0.0.0:8443", time.Second),
		agent: &purgingAgent{
			inner:  &fakeAgent{nonce: "n1", csrPEM: makeCSR(t, AgentCommonName(qube.Name))},
			certs:  certs,
			qubeID: qube.ID,
		},
	}

	clock := time.Now().UTC()
	health := &fakeHealthWriter{}
	m := newTestMonitor(&clock, &fakeQubeLister{qubes: []*models.Qube{qube}}, certs, renewer, health)
	m.Sweep(ctx)

	w, ok := health.last()
	require.True(t, ok)
	assert.Equal(t, models.AgentHealthHealthy, w.health,
		"the agent answered; a purge race says nothing bad about the qube")
	assert.Contains(t, w.failure, string(CertRenewalWithdrawn))
	assert.NotContains(t, w.failure, string(CertRenewalConsoleFailed),
		"a purge must not be reported as a broken console")

	// Nothing unrevoked survives: the purged qube did not get its access back.
	list, err := certs.ListByQube(ctx, qube.ID)
	require.NoError(t, err)
	for _, c := range list {
		assert.True(t, c.Revoked(), "certificate %s survived the purge", c.Fingerprint)
	}
}

// purgingAgent commits a purge in the gap between the agent answering and the
// console recording the certificate it signed in reply.
type purgingAgent struct {
	inner  agentCaller
	certs  *repository.AgentCertRepository
	qubeID string
	purged bool
}

func (p *purgingAgent) address() string { return p.inner.address() }

func (p *purgingAgent) call(ctx context.Context, target, service string, in []byte) ([]byte, error) {
	out, err := p.inner.call(ctx, target, service, in)
	if service == beginRenewalService && !p.purged {
		p.purged = true
		if _, rerr := p.certs.RevokeByQube(ctx, p.qubeID, "qube purged"); rerr != nil {
			return nil, rerr
		}
	}
	return out, err
}

// --- more doubles -----------------------------------------------------------

// fakeQubeLister is the qube repository slice the scheduler uses.
type fakeQubeLister struct {
	qubes []*models.Qube
	err   error
}

func (f *fakeQubeLister) ListByStatus(
	_ context.Context, statuses []models.QubeStatus,
) ([]*models.Qube, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []*models.Qube
	for _, q := range f.qubes {
		for _, s := range statuses {
			if q.Status == s {
				out = append(out, q)
			}
		}
	}
	return out, nil
}

// recordingQubeRepo captures agent-health writes made through the production
// recordAgentHealth path.
type recordingQubeRepo struct {
	repository.QubeRepository
	writes []healthWrite
}

func (r *recordingQubeRepo) UpdateAgentHealth(
	_ context.Context, id string, health models.AgentHealth, _ time.Time, failure string,
) error {
	r.writes = append(r.writes, healthWrite{qubeID: id, health: health, failure: failure})
	return nil
}

var (
	_ CertRenewalQubes  = (*fakeQubeLister)(nil)
	_ QubeCertRenewer   = (*fakeRenewer)(nil)
	_ AgentHealthWriter = (*fakeHealthWriter)(nil)
	_ RenewalWatch      = (*CertRenewalMonitor)(nil)
)
