package service

import (
	"context"
	"errors"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"log"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
)

// Agent health monitor defaults. Overridden from configuration; see
// config.OrchestratorConfig.AgentProbe*.
const (
	// DefaultAgentProbeInterval is how often running qubes are re-probed.
	DefaultAgentProbeInterval = time.Minute
	// DefaultAgentSettleBudget is how long a freshly provisioned qube is given
	// to bring its agent up before the console calls it unreachable.
	//
	// Five minutes is not caution, it is measured: cloud-init only begins
	// installing the agent once the VM has an address, and that install is an
	// apt download over the LAN. A budget shorter than the install produces a
	// confident "unreachable" for a qube that is merely still working.
	DefaultAgentSettleBudget = 5 * time.Minute
	// DefaultAgentSettleRetry is the gap between attempts inside the budget.
	DefaultAgentSettleRetry = 15 * time.Second

	// settleQueueSize bounds how many just-finished jobs may be waiting for a
	// settle probe.
	settleQueueSize = 64
	// settleWorkers is how many qubes may be settling at once. More than one
	// because a settle spends its whole budget waiting: with a single worker a
	// batch of ten provisions would take the tenth qube nearly an hour to reach
	// a verdict, by which point the health reading answers a question nobody is
	// still asking.
	settleWorkers = 4
)

// AgentHealthQubes is the slice of the qube repository the monitor needs.
//
// Narrowed on purpose: the monitor must never be able to write a qube's status.
// A background probe that could set status would be able to declare a qube
// stopped because its agent was quiet, which is the exact conflation this
// feature exists to prevent.
type AgentHealthQubes interface {
	GetByID(ctx context.Context, id string) (*models.Qube, error)
	ListByStatus(ctx context.Context, statuses []models.QubeStatus) ([]*models.Qube, error)
	UpdateIPAddress(ctx context.Context, id, ipAddress string) error
}

// AgentProbeRunner probes one qube and records the result. Implemented by
// QubeServiceImpl, so the monitor and the on-demand endpoint share one answer.
type AgentProbeRunner interface {
	ProbeAgent(ctx context.Context, qube *models.Qube, phase AgentProbePhase) AgentProbeResult
}

// AgentAddressReader learns a qube's address from the infrastructure.
//
// This exists because the console does not otherwise find out: terraform emits
// ip_address in its remote_qubes output, but nothing was reading it back, so
// qubes.ip_address stayed empty and there was no address to dial. Optional —
// implemented by *orchestrator.TerraformExecutor, absent when orchestration is
// disabled.
type AgentAddressReader interface {
	Address(ctx context.Context, qubeName string) (string, error)
}

// AgentHealthConfig configures the monitor. Zero values take the defaults above.
type AgentHealthConfig struct {
	// Interval is the gap between reconciler sweeps. Negative or zero disables
	// the sweep entirely, leaving only post-job settle probes.
	Interval time.Duration
	// SettleBudget bounds the post-provision retry loop.
	SettleBudget time.Duration
	// SettleRetry is the gap between attempts inside SettleBudget.
	SettleRetry time.Duration
}

// settleRequest is one qube to watch after its job finished.
type settleRequest struct {
	qubeID   string
	qubeName string
	action   string
}

// AgentHealthMonitor keeps agent health honest without anyone asking for it.
//
// Two jobs, deliberately separate:
//
//   - After a provision or resume succeeds, retry-probe the qube until its agent
//     answers or a bounded budget runs out. A single immediate probe cannot work
//     here: terraform returns before cloud-init has installed the agent, so every
//     healthy qube would be reported unreachable.
//   - Sweep every running qube on an interval, so an agent that dies LATER is
//     noticed. Without this a qube that was healthy once reads healthy forever.
//
// Neither can fail a job or block one. The monitor is told about a finished job
// and returns immediately; all the waiting happens on its own goroutines.
type AgentHealthMonitor struct {
	qubes  AgentHealthQubes
	prober AgentProbeRunner
	addrs  AgentAddressReader
	cfg    AgentHealthConfig

	queue chan settleRequest

	// base is the monitor's lifetime context, from context.Background() rather
	// than any request: a probe outlives the HTTP call that provoked it.
	base   context.Context
	cancel context.CancelFunc

	wg   sync.WaitGroup
	stop sync.Once

	// settlingMu/settling track which qubes are inside their boot grace window.
	//
	// Without this the settle phase is dead code. makeCompletionHook writes
	// status=running BEFORE calling Settle, so a just-provisioned qube is in
	// ListByStatus([running]) from the instant the job finishes — and the
	// reconciler, sweeping every interval, probes it as Steady and overwrites
	// "starting" with "unreachable". With a 60s interval inside a 300s budget
	// that happens up to five times per boot, and the resting value is whichever
	// write lands last. A field that flaps red on every healthy boot is worse
	// than no field: it teaches people to ignore it.
	settlingMu sync.RWMutex
	settling   map[string]struct{}

	// closeMu/closing guard the queue against a send racing its close, the same
	// arrangement orchestrator.Runner uses. Settle takes the read lock;
	// Shutdown takes the write lock before closing.
	closeMu sync.RWMutex
	closing bool
}

// AgentHealthOption customises a monitor at construction.
type AgentHealthOption func(*AgentHealthMonitor)

// WithAgentAddressReader lets the monitor refresh a qube's IP address from the
// infrastructure before probing it. Without it a qube whose address the console
// never learned can only ever be reported as having no address.
func WithAgentAddressReader(r AgentAddressReader) AgentHealthOption {
	return func(m *AgentHealthMonitor) { m.addrs = r }
}

// NewAgentHealthMonitor builds a monitor. Call Start to spawn its goroutines.
func NewAgentHealthMonitor(
	qubes AgentHealthQubes, prober AgentProbeRunner, cfg AgentHealthConfig, opts ...AgentHealthOption,
) *AgentHealthMonitor {
	if cfg.SettleBudget <= 0 {
		cfg.SettleBudget = DefaultAgentSettleBudget
	}
	if cfg.SettleRetry <= 0 {
		cfg.SettleRetry = DefaultAgentSettleRetry
	}
	base, cancel := context.WithCancel(context.Background())
	m := &AgentHealthMonitor{
		qubes:  qubes,
		prober: prober,
		cfg:    cfg,
		queue:  make(chan settleRequest, settleQueueSize),
		base:   base,
		cancel: cancel,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Start spawns the settle workers and, unless disabled, the reconciler.
func (m *AgentHealthMonitor) Start() {
	if m == nil {
		return
	}
	for i := 0; i < settleWorkers; i++ {
		m.wg.Add(1)
		go m.settleLoop()
	}

	if m.cfg.Interval <= 0 {
		// Loud, because the difference between "no agent has failed" and "nobody
		// is looking" is invisible in the UI: both show whatever the last probe
		// found, forever.
		log.Printf("agenthealth: periodic re-probing is DISABLED " +
			"(set orchestrator.agent_probe_interval_seconds > 0); " +
			"an agent that dies after provisioning will NOT be noticed")
		return
	}
	m.wg.Add(1)
	go m.reconcileLoop()
	log.Printf("agenthealth: probing every %s, settle budget %s (retry every %s)",
		m.cfg.Interval, m.cfg.SettleBudget, m.cfg.SettleRetry)
}

// Settle asks the monitor to watch a qube whose job just succeeded.
//
// It returns immediately, which is the contract that matters: this is called
// from the orchestrator's completion hook, on the single terraform worker
// goroutine. Waiting here for an agent to come up would stall every queued
// apply behind a qube that is merely booting.
func (m *AgentHealthMonitor) Settle(qubeID, qubeName, action string) {
	if m == nil {
		return
	}
	m.markSettling(qubeID, true)
	m.closeMu.RLock()
	defer m.closeMu.RUnlock()
	if m.closing {
		return
	}

	select {
	case m.queue <- settleRequest{qubeID: qubeID, qubeName: qubeName, action: action}:
	default:
		// Dropped rather than blocking the terraform worker. Recoverable — the
		// reconciler sweeps every running qube anyway — so this costs a delayed
		// first reading, not a lost one. Still said out loud: a queue this full
		// means provisioning is outrunning probing.
		log.Printf("agenthealth: settle queue is full, not scheduling a post-%s probe for qube %q; "+
			"its agent health will be picked up by the next sweep instead", action, qubeName)
	}
}

// settleLoop drains queued settle requests.
func (m *AgentHealthMonitor) settleLoop() {
	defer m.wg.Done()
	// Ranging over the queue (rather than selecting on base.Done) is what lets
	// Shutdown close the channel and have the worker exit on its own. The
	// cancelled base is checked as well, so a shutdown mid-burst does not have
	// to wait out the whole backlog.
	for req := range m.queue {
		if m.base.Err() != nil {
			continue
		}
		m.settle(req)
	}
}

// settle probes one qube repeatedly until its agent answers or the budget ends.
//
// The retry is the point. cloud-init installs the agent only after the VM
// reports its address, so at the moment terraform returns the agent is reliably
// absent. Probing once would mark every healthy qube unreachable — a signal
// worse than none, because a field that cries wolf gets ignored, and the next
// genuinely dead agent goes unnoticed exactly like the one that started this.
func (m *AgentHealthMonitor) settle(req settleRequest) {
	// Cleared however this returns, including an early exit or a panic: a qube
	// left marked settling would be skipped by the reconciler forever, turning
	// a guard against false red into a permanent blind spot.
	defer m.markSettling(req.qubeID, false)

	started := time.Now()
	deadline := started.Add(m.cfg.SettleBudget)
	attempts := 0

	for {
		attempts++
		last := !time.Now().Add(m.cfg.SettleRetry).Before(deadline)

		// Steady on the final attempt: the budget is spent, so "no answer" is
		// now a verdict rather than a qube still coming up. That flip is what
		// makes "starting" mean something — without it a broken agent would sit
		// in "starting" forever and hide just as well as it did before.
		phase := AgentProbeSettling
		if last {
			phase = AgentProbeSteady
		}

		res, ok := m.probeOnce(m.base, req.qubeID, req.qubeName, phase)
		if !ok {
			return // the qube is gone, or we are shutting down
		}
		if res.Reachable {
			log.Printf("agenthealth: qube %q agent answered %s after %s (attempt %d): %s",
				req.qubeName, req.action, time.Since(started).Round(time.Second), attempts, res.Pong)
			return
		}
		if last {
			// The failure that used to be invisible. Named qube, real reason,
			// and an explicit statement that the VM is fine — so nobody goes
			// looking for a broken hypervisor when the problem is a package.
			log.Printf("agenthealth: qube %q agent NEVER came up within %s of a successful %s "+
				"(%d attempts, status=%s): %s -- the compute VM is running; "+
				"check the agent package install and the qubes-air-agent unit inside the qube",
				req.qubeName, m.cfg.SettleBudget, req.action, attempts, res.Status, res.Reason)
			return
		}

		select {
		case <-m.base.Done():
			return
		case <-time.After(m.cfg.SettleRetry):
		}
	}
}

// reconcileLoop re-probes running qubes on an interval.
func (m *AgentHealthMonitor) reconcileLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.base.Done():
			return
		case <-ticker.C:
			// A sweep slower than the interval simply drops ticks — Ticker does
			// not queue them — so a large fleet degrades to "as often as it can"
			// rather than piling sweeps on top of each other.
			m.reconcile()
		}
	}
}

// reconcile probes every running qube once.
//
// Running only. A suspended qube has no compute instance, so an unreachable
// agent there is the expected state and probing it would fill the health column
// with failures that mean nothing.
func (m *AgentHealthMonitor) reconcile() {
	qubes, err := m.qubes.ListByStatus(m.base, []models.QubeStatus{models.QubeStatusRunning})
	if err != nil {
		log.Printf("agenthealth: could not list running qubes to re-probe: %v", err)
		return
	}

	for _, q := range qubes {
		// Checked between qubes so shutdown does not have to wait out a whole
		// sweep of a large fleet.
		if m.base.Err() != nil {
			return
		}
		// A qube inside its boot grace window is already being probed by the
		// settle loop, at the phase that knows it is still coming up. Probing it
		// here as Steady would overwrite "starting" with "unreachable" on a qube
		// that is perfectly healthy and simply not finished booting.
		if m.IsSettling(q.ID) {
			continue
		}
		// Steady: not settling, so no answer means the agent is broken.
		m.probeQube(m.base, q, AgentProbeSteady)
	}
}

// probeOnce loads the qube fresh and probes it.
//
// Re-reading every attempt is deliberate: a just-provisioned qube may not have
// had its address recorded when the job finished, and a stale copy would keep
// probing an empty address long after the real one was known.
func (m *AgentHealthMonitor) probeOnce(
	ctx context.Context, qubeID, qubeName string, phase AgentProbePhase,
) (AgentProbeResult, bool) {
	qube, err := m.qubes.GetByID(ctx, qubeID)
	if err != nil {
		// Deleted mid-settle, or the database is unavailable. Either way there
		// is nothing to record against, so stop rather than spin.
		log.Printf("agenthealth: stopping probes for qube %q: %v", qubeName, err)
		return AgentProbeResult{}, false
	}
	return m.probeQube(ctx, qube, phase), true
}

// probeQube refreshes the qube's address if needed, then probes it.
func (m *AgentHealthMonitor) probeQube(
	ctx context.Context, qube *models.Qube, phase AgentProbePhase,
) AgentProbeResult {
	m.refreshAddress(ctx, qube)
	return m.prober.ProbeAgent(ctx, qube, phase)
}

// refreshAddress fills in a qube's IP from the infrastructure when the console
// does not have one.
//
// Only when it is missing: terraform is the authority on the address, but
// asking it costs a subprocess, and a qube that already answers on a known
// address has nothing to gain from re-reading it. A failure here is not fatal —
// the probe then reports "no address", which is its own honest diagnosis.
func (m *AgentHealthMonitor) refreshAddress(ctx context.Context, qube *models.Qube) {
	if m.addrs == nil || qube == nil || qube.IPAddress != "" {
		return
	}

	// Bounded: reading terraform output shells out, and an unbounded wait here
	// would hold a probe worker on a wedged terraform state lock.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	addr, err := m.addrs.Address(ctx, qube.Name)
	if errors.Is(err, orchestrator.ErrExecutorBusy) {
		// An apply is running. Nothing is wrong and the next sweep will ask
		// again; logging it as a failure every interval would bury the real ones.
		return
	}
	if err != nil {
		log.Printf("agenthealth: qube %q has no recorded address and terraform could not supply one: %v",
			qube.Name, err)
		return
	}
	if addr == "" {
		return
	}

	qube.IPAddress = addr
	if err := m.qubes.UpdateIPAddress(ctx, qube.ID, addr); err != nil {
		// The probe can still proceed on the in-memory value; only the saving
		// failed. Reported so a persistently unsaved address is visible rather
		// than showing up as terraform being shelled out to on every sweep.
		log.Printf("agenthealth: qube %q address %s could not be recorded: %v", qube.Name, addr, err)
	}
}

// Shutdown stops probing and waits for the workers, up to grace.
//
// The base context is cancelled BEFORE waiting, unlike orchestrator.Runner
// which waits first. The asymmetry is intentional: an abandoned terraform apply
// strands real infrastructure, while an abandoned probe leaves nothing behind
// at all. A settle worker can be mid-sleep in a five-minute budget, and waiting
// that out would turn every restart into a five-minute outage.
//
// Both the cancel and the channel close are needed. Closing alone would leave a
// worker sleeping inside settle; cancelling alone would leave settleLoop parked
// on a receive that never completes — this codebase already had a shutdown
// deadlock from waiting on a worker that only exits on cancel.
func (m *AgentHealthMonitor) Shutdown(grace time.Duration) {
	if m == nil {
		return
	}
	m.stop.Do(func() {
		m.closeMu.Lock()
		m.closing = true
		close(m.queue)
		m.closeMu.Unlock()

		m.cancel()

		done := make(chan struct{})
		go func() {
			m.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(grace):
			// Never block shutdown outright. A probe worker that has not
			// returned is stuck in a network call that will time out on its own;
			// holding the process hostage for it would be a worse failure than
			// leaking the goroutine into a process that is exiting anyway.
			log.Printf("agenthealth: shutdown grace of %s elapsed with probes still in flight; continuing", grace)
		}
	})
}

// markSettling records whether a qube is inside its boot grace window.
func (m *AgentHealthMonitor) markSettling(qubeID string, settling bool) {
	if m == nil || qubeID == "" {
		return
	}
	m.settlingMu.Lock()
	defer m.settlingMu.Unlock()
	if m.settling == nil {
		m.settling = map[string]struct{}{}
	}
	if settling {
		m.settling[qubeID] = struct{}{}
		return
	}
	delete(m.settling, qubeID)
}

// IsSettling reports whether a qube is still inside its boot grace window.
//
// Exported so an on-demand check can report "still coming up" instead of
// declaring a healthy, still-booting qube unreachable.
func (m *AgentHealthMonitor) IsSettling(qubeID string) bool {
	if m == nil {
		return false
	}
	m.settlingMu.RLock()
	defer m.settlingMu.RUnlock()
	_, ok := m.settling[qubeID]
	return ok
}
