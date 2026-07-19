package service

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles -----------------------------------------------------------

// fakeQubes is an in-memory stand-in for the qube repository.
type fakeQubes struct {
	mu     sync.Mutex
	byID   map[string]*models.Qube
	getErr error
	ips    map[string]string
}

func newFakeQubes(qubes ...*models.Qube) *fakeQubes {
	f := &fakeQubes{byID: map[string]*models.Qube{}, ips: map[string]string{}}
	for _, q := range qubes {
		f.byID[q.ID] = q
	}
	return f
}

func (f *fakeQubes) GetByID(_ context.Context, id string) (*models.Qube, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	q, ok := f.byID[id]
	if !ok {
		return nil, errors.New("qube not found")
	}
	copied := *q
	return &copied, nil
}

func (f *fakeQubes) ListByStatus(_ context.Context, statuses []models.QubeStatus) ([]*models.Qube, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*models.Qube
	for _, q := range f.byID {
		for _, s := range statuses {
			if q.Status == s {
				copied := *q
				out = append(out, &copied)
			}
		}
	}
	return out, nil
}

func (f *fakeQubes) UpdateIPAddress(_ context.Context, id, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ips[id] = ip
	if q, ok := f.byID[id]; ok {
		q.IPAddress = ip
	}
	return nil
}

func (f *fakeQubes) failGets(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getErr = err
}

// probeCall is one recorded ProbeAgent invocation.
type probeCall struct {
	qubeID string
	phase  AgentProbePhase
	health models.AgentHealth
}

// fakeProber stands in for QubeServiceImpl.ProbeAgent. It derives the recorded
// health through the real agentHealthFor, so a test asserting on "starting" vs
// "unreachable" is asserting on the production mapping and not on a copy of it.
type fakeProber struct {
	mu    sync.Mutex
	calls []probeCall
	// fn decides the result of attempt n (0-based) for a qube.
	fn func(n int, q *models.Qube, phase AgentProbePhase) AgentProbeResult
}

func (f *fakeProber) ProbeAgent(ctx context.Context, q *models.Qube, phase AgentProbePhase) AgentProbeResult {
	f.mu.Lock()
	n := len(f.calls)
	f.mu.Unlock()

	res := f.fn(n, q, phase)
	// The prober is what honours the caller's context in production; mirror
	// that so a shutdown test actually exercises cancellation.
	if ctx.Err() != nil {
		res.Reachable = false
		res.Status = AgentProbeUnreachable
		res.Reason = ctx.Err().Error()
	}

	f.mu.Lock()
	f.calls = append(f.calls, probeCall{qubeID: q.ID, phase: phase, health: agentHealthFor(res.Status, phase)})
	f.mu.Unlock()
	return res
}

func (f *fakeProber) recorded() []probeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]probeCall(nil), f.calls...)
}

func (f *fakeProber) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// --- helpers ----------------------------------------------------------------

func unreachableResult() AgentProbeResult {
	return AgentProbeResult{
		Status:    AgentProbeUnreachable,
		Reason:    "nothing is listening on 10.0.0.7:8443",
		CheckedAt: time.Now().UTC(),
	}
}

func healthyResult() AgentProbeResult {
	return AgentProbeResult{
		Reachable: true,
		Status:    AgentProbeOK,
		Pong:      "pong test-qube",
		CheckedAt: time.Now().UTC(),
	}
}

func runningQube(id, name, ip string) *models.Qube {
	return &models.Qube{ID: id, Name: name, IPAddress: ip, Status: models.QubeStatusRunning}
}

// waitFor polls until cond holds, failing the test if it never does. Used
// instead of a fixed sleep so a slow machine does not turn into a flake.
func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", within, what)
}

// --- settle: retry then give up ---------------------------------------------

// TestSettle_RetriesThenGivesUp is the core of the post-provision behaviour.
//
// A single immediate probe would report every healthy qube unreachable, because
// cloud-init installs the agent only after terraform has finished. So the
// monitor retries — but a budget that never ends would leave a genuinely broken
// agent sitting in "starting" forever, which hides the failure exactly as well
// as reporting nothing did. Both halves are asserted here.
func TestSettle_RetriesThenGivesUp(t *testing.T) {
	qubes := newFakeQubes(runningQube("q1", "never-comes-up", "10.0.0.7"))
	prober := &fakeProber{
		fn: func(int, *models.Qube, AgentProbePhase) AgentProbeResult { return unreachableResult() },
	}

	m := NewAgentHealthMonitor(qubes, prober, AgentHealthConfig{
		SettleBudget: 250 * time.Millisecond,
		SettleRetry:  40 * time.Millisecond,
	})
	m.Start()
	defer m.Shutdown(2 * time.Second)

	m.Settle("q1", "never-comes-up", "provision")

	waitFor(t, 5*time.Second, "the settle budget to be spent", func() bool {
		calls := prober.recorded()
		return len(calls) > 0 && calls[len(calls)-1].phase == AgentProbeSteady
	})

	calls := prober.recorded()
	require.GreaterOrEqual(t, len(calls), 3,
		"one probe is not enough: the agent is legitimately absent when terraform returns")

	// Every attempt but the last is inside the grace period and must record
	// "starting" — a qube that is still coming up is not a qube that failed.
	for i, c := range calls[:len(calls)-1] {
		assert.Equal(t, AgentProbeSettling, c.phase, "attempt %d", i)
		assert.Equal(t, models.AgentHealthStarting, c.health, "attempt %d", i)
	}

	// The last one is the verdict. Without this flip "starting" would be a
	// permanent resting state and nothing would ever be reported as broken.
	last := calls[len(calls)-1]
	assert.Equal(t, AgentProbeSteady, last.phase)
	assert.Equal(t, models.AgentHealthUnreachable, last.health)

	// And it really stops: a settle loop that kept probing forever would hammer
	// a dead qube for the life of the process.
	after := prober.count()
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, after, prober.count(), "probing must stop once the budget is spent")
}

// TestSettle_StopsAsSoonAsTheAgentAnswers — the common case. A qube that comes
// up on the third attempt must be recorded healthy and left alone.
func TestSettle_StopsAsSoonAsTheAgentAnswers(t *testing.T) {
	qubes := newFakeQubes(runningQube("q1", "slow-boot", "10.0.0.8"))
	prober := &fakeProber{
		fn: func(n int, _ *models.Qube, _ AgentProbePhase) AgentProbeResult {
			if n < 2 {
				return unreachableResult()
			}
			return healthyResult()
		},
	}

	m := NewAgentHealthMonitor(qubes, prober, AgentHealthConfig{
		SettleBudget: 5 * time.Second,
		SettleRetry:  20 * time.Millisecond,
	})
	m.Start()
	defer m.Shutdown(2 * time.Second)

	m.Settle("q1", "slow-boot", "provision")

	waitFor(t, 5*time.Second, "the agent to be seen healthy", func() bool {
		calls := prober.recorded()
		return len(calls) > 0 && calls[len(calls)-1].health == models.AgentHealthHealthy
	})

	calls := prober.recorded()
	require.Len(t, calls, 3, "it must stop on the first answer, not keep probing out the budget")
	assert.Equal(t, models.AgentHealthStarting, calls[0].health)
	assert.Equal(t, models.AgentHealthStarting, calls[1].health,
		"a qube that has not answered YET is not a qube that failed")
	assert.Equal(t, models.AgentHealthHealthy, calls[2].health)

	time.Sleep(100 * time.Millisecond)
	assert.Len(t, prober.recorded(), 3)
}

// TestSettle_LateAddressIsPickedUp — the qube is re-read on every attempt, so an
// address that terraform only reports after the job finished is still used. A
// cached copy would keep probing an empty address for the whole budget and then
// declare a perfectly healthy agent unreachable.
func TestSettle_LateAddressIsPickedUp(t *testing.T) {
	qube := runningQube("q1", "late-ip", "")
	qubes := newFakeQubes(qube)

	prober := &fakeProber{
		fn: func(_ int, q *models.Qube, _ AgentProbePhase) AgentProbeResult {
			if q.IPAddress == "" {
				return AgentProbeResult{Status: AgentProbeNoAddress, Reason: "no IP yet"}
			}
			return healthyResult()
		},
	}

	m := NewAgentHealthMonitor(qubes, prober, AgentHealthConfig{
		SettleBudget: 5 * time.Second,
		SettleRetry:  20 * time.Millisecond,
	})
	m.Start()
	defer m.Shutdown(2 * time.Second)

	m.Settle("q1", "late-ip", "provision")
	waitFor(t, 2*time.Second, "the first address-less probe", func() bool { return prober.count() > 0 })

	require.NoError(t, qubes.UpdateIPAddress(context.Background(), "q1", "10.0.0.9"))

	waitFor(t, 5*time.Second, "the agent to be seen healthy once the address arrived", func() bool {
		calls := prober.recorded()
		return len(calls) > 0 && calls[len(calls)-1].health == models.AgentHealthHealthy
	})
}

// TestSettle_DeletedQubeStopsProbing — a qube released mid-settle must end the
// loop rather than spin against a row that no longer exists.
func TestSettle_DeletedQubeStopsProbing(t *testing.T) {
	qubes := newFakeQubes(runningQube("q1", "doomed", "10.0.0.7"))
	qubes.failGets(errors.New("qube not found"))

	prober := &fakeProber{
		fn: func(int, *models.Qube, AgentProbePhase) AgentProbeResult { return unreachableResult() },
	}

	m := NewAgentHealthMonitor(qubes, prober, AgentHealthConfig{
		SettleBudget: 5 * time.Second,
		SettleRetry:  10 * time.Millisecond,
	})
	m.Start()
	defer m.Shutdown(2 * time.Second)

	m.Settle("q1", "doomed", "provision")
	time.Sleep(150 * time.Millisecond)

	assert.Zero(t, prober.count(), "a qube that cannot be read must not be probed")
}

// --- reconciler -------------------------------------------------------------

// TestReconcile_ReprobesRunningQubes — an agent that dies AFTER provisioning is
// the failure the settle loop cannot catch, because by then nobody is watching.
func TestReconcile_ReprobesRunningQubes(t *testing.T) {
	qubes := newFakeQubes(
		runningQube("q1", "alive", "10.0.0.1"),
		&models.Qube{ID: "q2", Name: "parked", IPAddress: "10.0.0.2", Status: models.QubeStatusSuspended},
	)
	prober := &fakeProber{
		fn: func(int, *models.Qube, AgentProbePhase) AgentProbeResult { return unreachableResult() },
	}

	m := NewAgentHealthMonitor(qubes, prober, AgentHealthConfig{
		Interval:     20 * time.Millisecond,
		SettleBudget: time.Second,
		SettleRetry:  time.Second,
	})
	m.Start()
	defer m.Shutdown(2 * time.Second)

	waitFor(t, 5*time.Second, "at least two sweeps", func() bool { return prober.count() >= 2 })
	m.Shutdown(2 * time.Second)

	for _, c := range prober.recorded() {
		assert.Equal(t, "q1", c.qubeID,
			"a suspended qube has no compute instance; probing it would record a failure that means nothing")
		// Steady, never settling: a qube that has been running long enough to be
		// swept is well past any boot grace, so silence is a real verdict.
		assert.Equal(t, AgentProbeSteady, c.phase)
		assert.Equal(t, models.AgentHealthUnreachable, c.health)
	}
}

// TestReconcile_DisabledByZeroInterval — the reconciler can be switched off, and
// when it is, nothing probes on its own.
func TestReconcile_DisabledByZeroInterval(t *testing.T) {
	qubes := newFakeQubes(runningQube("q1", "alive", "10.0.0.1"))
	prober := &fakeProber{
		fn: func(int, *models.Qube, AgentProbePhase) AgentProbeResult { return healthyResult() },
	}

	m := NewAgentHealthMonitor(qubes, prober, AgentHealthConfig{Interval: 0})
	m.Start()
	defer m.Shutdown(time.Second)

	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, prober.count())
}

// --- shutdown ---------------------------------------------------------------

// TestShutdown_ReturnsPromptlyWithASettleInFlight is the regression this project
// has already paid for once: a shutdown that waits on a worker which only exits
// on cancel deadlocks. A settle worker parked in a five-minute retry sleep must
// not hold the process.
func TestShutdown_ReturnsPromptlyWithASettleInFlight(t *testing.T) {
	qubes := newFakeQubes(runningQube("q1", "slow", "10.0.0.1"))
	prober := &fakeProber{
		fn: func(int, *models.Qube, AgentProbePhase) AgentProbeResult { return unreachableResult() },
	}

	m := NewAgentHealthMonitor(qubes, prober, AgentHealthConfig{
		Interval: 50 * time.Millisecond,
		// Far longer than the test: if Shutdown waited for the budget rather
		// than cancelling it, this would hang instead of failing.
		SettleBudget: time.Hour,
		SettleRetry:  30 * time.Minute,
	})
	m.Start()

	m.Settle("q1", "slow", "provision")
	waitFor(t, 5*time.Second, "the settle worker to park in its retry sleep",
		func() bool { return prober.count() > 0 })

	start := time.Now()
	m.Shutdown(5 * time.Second)
	assert.Less(t, time.Since(start), 2*time.Second,
		"an abandoned probe leaves nothing behind, so shutdown must not wait out a retry budget")
}

// TestShutdown_UnblocksAProbeThatIsWaitingOnItsContext — cancellation has to
// reach the probe itself, not only the sleep between probes.
func TestShutdown_UnblocksAProbeThatIsWaitingOnItsContext(t *testing.T) {
	qubes := newFakeQubes(runningQube("q1", "hung", "10.0.0.1"))
	entered := make(chan struct{}, 1)
	// Released only when the test ends, so the stuck worker does not outlive it.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	prober := &fakeProber{
		fn: func(_ int, _ *models.Qube, _ AgentProbePhase) AgentProbeResult {
			select {
			case entered <- struct{}{}:
			default:
			}
			// Stands in for a probe wedged in a syscall — one that does NOT
			// notice cancellation. Shutdown has to survive that, not just the
			// well-behaved case.
			<-release
			return unreachableResult()
		},
	}

	m := NewAgentHealthMonitor(qubes, prober, AgentHealthConfig{
		SettleBudget: time.Hour,
		SettleRetry:  time.Minute,
	})
	m.Start()
	m.Settle("q1", "hung", "provision")

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the probe never started")
	}

	// The grace is what bounds this: a probe genuinely stuck in a syscall is
	// left to time out on its own rather than holding the process hostage.
	start := time.Now()
	m.Shutdown(300 * time.Millisecond)
	assert.Less(t, time.Since(start), 3*time.Second, "shutdown must never block indefinitely")
}

// TestShutdown_IsIdempotentAndSettleAfterwardsIsSafe — Close runs on a defer
// path that can be reached twice, and a late completion hook can call Settle
// after shutdown has begun. Neither may panic on a closed channel.
func TestShutdown_IsIdempotentAndSettleAfterwardsIsSafe(t *testing.T) {
	qubes := newFakeQubes(runningQube("q1", "q", "10.0.0.1"))
	prober := &fakeProber{
		fn: func(int, *models.Qube, AgentProbePhase) AgentProbeResult { return healthyResult() },
	}

	m := NewAgentHealthMonitor(qubes, prober, AgentHealthConfig{Interval: 10 * time.Millisecond})
	m.Start()

	m.Shutdown(time.Second)
	assert.NotPanics(t, func() { m.Shutdown(time.Second) })
	assert.NotPanics(t, func() { m.Settle("q1", "q", "provision") },
		"a job finishing during shutdown must not send on a closed queue")
}

// TestShutdown_LeavesNoGoroutinesBehind — the workers and the reconciler must
// all exit. A monitor that leaked one goroutine per restart would be invisible
// until a long-running process ran out of them.
func TestShutdown_LeavesNoGoroutinesBehind(t *testing.T) {
	before := runtime.NumGoroutine()

	qubes := newFakeQubes(runningQube("q1", "q", "10.0.0.1"))
	prober := &fakeProber{
		fn: func(int, *models.Qube, AgentProbePhase) AgentProbeResult { return healthyResult() },
	}

	m := NewAgentHealthMonitor(qubes, prober, AgentHealthConfig{
		Interval:     10 * time.Millisecond,
		SettleBudget: time.Second,
		SettleRetry:  10 * time.Millisecond,
	})
	m.Start()
	m.Settle("q1", "q", "provision")
	waitFor(t, 5*time.Second, "some probing to happen", func() bool { return prober.count() > 0 })

	m.Shutdown(5 * time.Second)

	waitFor(t, 5*time.Second, "every monitor goroutine to exit", func() bool {
		return runtime.NumGoroutine() <= before+2 // tolerance for the test runtime's own
	})
}

// TestNilMonitorIsInert — main wires the monitor optionally, and the completion
// hook reaches it unconditionally. A nil monitor must be a no-op, not a panic
// during a terraform job.
func TestNilMonitorIsInert(t *testing.T) {
	var m *AgentHealthMonitor
	assert.NotPanics(t, func() {
		m.Start()
		m.Settle("q1", "q", "provision")
		m.Shutdown(time.Second)
	})
}

// --- health mapping ---------------------------------------------------------

// TestAgentHealthFor pins the one place a probe status becomes a stored health.
// Two callers classifying the same probe differently is how the same column
// ends up holding contradictory readings.
func TestAgentHealthFor(t *testing.T) {
	cases := []struct {
		status AgentProbeStatus
		phase  AgentProbePhase
		want   models.AgentHealth
	}{
		{AgentProbeOK, AgentProbeSteady, models.AgentHealthHealthy},
		// Success is success whenever it happens: a qube that answers during
		// its grace period is healthy, not "starting".
		{AgentProbeOK, AgentProbeSettling, models.AgentHealthHealthy},
		{AgentProbeUnreachable, AgentProbeSteady, models.AgentHealthUnreachable},
		{AgentProbeUnreachable, AgentProbeSettling, models.AgentHealthStarting},
		{AgentProbeTLSRejected, AgentProbeSteady, models.AgentHealthUnreachable},
		{AgentProbeRPCFailed, AgentProbeSteady, models.AgentHealthUnreachable},
		{AgentProbeNoAddress, AgentProbeSettling, models.AgentHealthStarting},
		{AgentProbeNoAddress, AgentProbeSteady, models.AgentHealthUnreachable},
		// A console that cannot probe has learned nothing, in either phase. It
		// must not report a health it never observed.
		{AgentProbeNotConfigured, AgentProbeSteady, models.AgentHealthUnknown},
		{AgentProbeNotConfigured, AgentProbeSettling, models.AgentHealthUnknown},
	}
	for _, c := range cases {
		got := agentHealthFor(c.status, c.phase)
		assert.Equal(t, c.want, got, "status=%s phase=%s", c.status, c.phase)
		assert.True(t, got.IsValid(), "every mapped health must be storable")
	}
}

// TestReconcilerSkipsSettlingQubes — without this the settle phase is dead code.
//
// makeCompletionHook writes status=running BEFORE calling Settle, so a
// just-provisioned qube appears in ListByStatus([running]) the instant the job
// finishes. The reconciler then probes it as Steady and overwrites "starting"
// with "unreachable" — up to five times inside a default 300s settle budget at a
// 60s interval, with the resting value decided by whichever write lands last.
//
// A health field that flaps red on every healthy boot is worse than no field:
// it teaches people to ignore it.
func TestReconcilerSkipsSettlingQubes(t *testing.T) {
	m := &AgentHealthMonitor{}

	assert.False(t, m.IsSettling("q1"), "a qube nobody settled is not settling")

	m.markSettling("q1", true)
	assert.True(t, m.IsSettling("q1"), "the reconciler must be able to see the grace window")

	m.markSettling("q1", false)
	assert.False(t, m.IsSettling("q1"),
		"a qube left marked settling would be skipped forever — a permanent blind spot")
}
