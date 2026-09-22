package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run Purge through a REAL orchestrator.Runner over a real database.
// That is the seam G-H3 lives on: the irreversible steps, the job row and the
// queue's refusal have to be observed together, and a fake submitter would
// decide the ordering itself instead of exercising it.

// purgeEventLog records what the provider and the stores did, in order.
type purgeEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *purgeEventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *purgeEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// recordingKeys is a DataKeyStore that records the crypto-shred and can be made
// to fail, standing in for an unreachable credential store.
type recordingKeys struct {
	log *purgeEventLog

	mu        sync.Mutex
	deleted   []string
	attempted []string
	deleteErr error
}

func (k *recordingKeys) EnsureDataKey(_ context.Context, qubeID string) (string, error) {
	return "k-" + qubeID, nil
}

func (k *recordingKeys) DeleteDataKey(_ context.Context, qubeID string) error {
	k.mu.Lock()
	err := k.deleteErr
	k.attempted = append(k.attempted, qubeID)
	if err == nil {
		k.deleted = append(k.deleted, qubeID)
	}
	k.mu.Unlock()
	if err != nil {
		return err
	}
	k.log.add("delete-key")
	return nil
}

func (k *recordingKeys) setDeleteErr(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.deleteErr = err
}

func (k *recordingKeys) destroyed() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.deleted...)
}

func (k *recordingKeys) attempts() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.attempted...)
}

// queueBlocker is the qube name whose resume call the test holds open.
const queueBlocker = "queue-blocker"

// purgeExecutor stands in for the provider adapter. Resume can be gated so the
// single worker is busy on demand; Destroy reads the stored protection latch the
// way the real adapter's guard does, so a destroy that ran before the
// preparation is not silently accepted — it fails, and the test can see it.
type purgeExecutor struct {
	orchestrator.NoopExecutor

	entered chan struct{}
	release chan struct{}

	infra  *repository.QubeInfraRepository
	log    *purgeEventLog
	qubeID string

	mu        sync.Mutex
	destroyed []string
}

func (e *purgeExecutor) Resume(_ context.Context, qubeName string) error {
	if qubeName != queueBlocker {
		return nil
	}
	select {
	case e.entered <- struct{}{}:
	default:
	}
	<-e.release
	return nil
}

func (e *purgeExecutor) Destroy(ctx context.Context, qubeName string) error {
	e.mu.Lock()
	e.destroyed = append(e.destroyed, qubeName)
	e.mu.Unlock()

	inf, err := e.infra.Get(ctx, e.qubeID)
	if err != nil {
		return err
	}
	protected := inf != nil && inf.Protected
	e.log.add(fmt.Sprintf("destroy:protected=%t", protected))
	if protected {
		return errors.New("refusing to destroy a data disk that is still protected")
	}
	return nil
}

func (e *purgeExecutor) destroyCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.destroyed...)
}

// purgeFixture wires the service, the runner and the stores Purge touches.
type purgeFixture struct {
	svc      QubeService
	qubeRepo repository.QubeRepository
	infra    *repository.QubeInfraRepository
	certs    *repository.AgentCertRepository
	jobs     *repository.JobRepository
	runner   *orchestrator.Runner
	exec     *purgeExecutor
	keys     *recordingKeys
	log      *purgeEventLog

	qubeID      string
	qubeName    string
	fingerprint string
}

// newPurgeFixture builds a suspended qube with a protected data disk, a
// registered agent certificate and a per-qube key — the state Purge accepts.
func newPurgeFixture(t *testing.T, queueSize int) *purgeFixture {
	t.Helper()
	ctx := context.Background()

	f, err := os.CreateTemp("", "purge-ordering-*.db")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	cfg := database.DefaultConfig()
	cfg.DSN = f.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	infra := repository.NewQubeInfraRepository(db)
	certs := repository.NewAgentCertRepository(db)
	jobs := repository.NewJobRepository(db)

	log := &purgeEventLog{}
	keys := &recordingKeys{log: log}
	exec := &purgeExecutor{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		infra:   infra,
		log:     log,
	}
	issuer := NewCertIssuer(newMemCredStore(), certs, t.TempDir(), "0.0.0.0:8443", testAgentPackage())

	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	// The qube is brought to "suspended" without a queue: this fixture is about
	// what Purge does, not about how a qube is parked.
	setupSvc := NewQubeService(qubeRepo, zoneRepo, WithExecutor(exec))
	zone := createConnectedZone(t, zoneSvc)
	const name = "goner"
	op, err := setupSvc.Create(ctx, &models.QubeCreateRequest{
		Name: name, Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)
	_, err = setupSvc.Stop(ctx, op.Qube.ID)
	require.NoError(t, err)
	require.NoError(t, infra.Save(ctx, &provider.Infra{
		QubeID: op.Qube.ID, Provider: "proxmox", StorageVMID: 42, Protected: true,
	}))

	// A live identity, so revocation is observable as a state change rather than
	// as a call count.
	expires := time.Now().Add(90 * 24 * time.Hour)
	fingerprint := "sha256:" + strings.Repeat("ab", 32)
	require.NoError(t, certs.Register(ctx, &repository.AgentCert{
		Fingerprint: fingerprint, QubeID: op.Qube.ID, SubjectCN: AgentCommonName(name),
		IssuedAt: time.Now().UTC(), ExpiresAt: &expires,
	}))

	exec.qubeID = op.Qube.ID
	runner := orchestrator.NewRunner(orchestrator.RunnerConfig{
		Executor:  exec,
		Store:     jobs,
		QueueSize: queueSize,
		Timeout:   10 * time.Second,
		// Production wires makeCompletionHook here (cmd/server). This keeps the
		// one mapping these tests read back: a finished destroy job settles its
		// qube, so a failed preparation cannot look like a purge.
		OnDone: func(ctx context.Context, j *orchestrator.Job) error {
			status := models.QubeStatusPurged
			if j.State != orchestrator.JobSucceeded {
				status = models.QubeStatusError
			}
			return qubeRepo.UpdateStatus(ctx, j.QubeID, status)
		},
	})
	runner.Start()

	svc := NewQubeService(qubeRepo, zoneRepo,
		WithExecutor(exec), WithInfraStore(infra), WithDataKeyStore(keys),
		WithCertIssuer(issuer), WithJobSubmitter(runner))

	t.Cleanup(func() {
		// Release a gated worker before shutting down, or Shutdown waits out its
		// grace period on a job the test deliberately froze.
		select {
		case <-exec.release:
		default:
			close(exec.release)
		}
		runner.Shutdown(5 * time.Second)
		_ = db.Close()
		_ = os.Remove(f.Name())
	})

	return &purgeFixture{
		svc: svc, qubeRepo: qubeRepo, infra: infra, certs: certs, jobs: jobs,
		runner: runner, exec: exec, keys: keys, log: log,
		qubeID: op.Qube.ID, qubeName: name, fingerprint: fingerprint,
	}
}

// holdWorker starts one provider call and leaves it open, so the single worker
// is busy and the queue can be filled behind it.
func (f *purgeFixture) holdWorker(t *testing.T) {
	t.Helper()
	_, err := f.runner.Submit(context.Background(), "other", queueBlocker, orchestrator.ActionResume)
	require.NoError(t, err)
	select {
	case <-f.exec.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never entered the blocking provider call")
	}
}

// terminalJob polls for the qube's most recent finished job. Purge returns as
// soon as the work is queued, so the outcome has to be read from the record —
// and a retry has more than one, so "the newest" is what it waits for.
func (f *purgeFixture) terminalJob(t *testing.T) *orchestrator.Job {
	t.Helper()
	due := time.Now().Add(10 * time.Second)
	for time.Now().Before(due) {
		jobs, err := f.jobs.ListByQube(context.Background(), f.qubeID, 10)
		require.NoError(t, err)
		newest := latestJob(jobs)
		if newest != nil && newest.State != orchestrator.JobQueued && newest.State != orchestrator.JobRunning {
			return newest
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the destroy job never reached a terminal state")
	return nil
}

// latestJob returns the most recently enqueued job, or nil when there is none.
func latestJob(jobs []*orchestrator.Job) *orchestrator.Job {
	var newest *orchestrator.Job
	for _, j := range jobs {
		if newest == nil || j.EnqueuedAt.After(newest.EnqueuedAt) {
			newest = j
		}
	}
	return newest
}

// TestPurge_RefusedEnqueueLeavesDataIntact is the G-H3 regression, run over the
// two refusal modes a real deployment hits: a saturated queue and a runner that
// is shutting down. The assertion is on what the refusal did NOT do — with the
// preparation running ahead of the enqueue, both modes find the disk
// unprotected and the per-qube key already gone.
func TestPurge_RefusedEnqueueLeavesDataIntact(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refuse  func(t *testing.T, f *purgeFixture)
		cause   string
		wantRow bool
	}{
		{
			name: "queue full",
			refuse: func(t *testing.T, f *purgeFixture) {
				// Single queue slot: hold the worker, then fill the slot behind
				// it. The next Submit is refused by the real ErrQueueFull branch,
				// not by a fake.
				f.holdWorker(t)
				_, err := f.runner.Submit(context.Background(), "other", "queued-behind", orchestrator.ActionResume)
				require.NoError(t, err,
					"the single queue slot must be occupied for the refusal to be the real branch")
			},
			cause: orchestrator.ErrQueueFull.Error(),
			// The runner records the job before it tries the queue, so this
			// refusal is itself on the record — with the reason.
			wantRow: true,
		},
		{
			name:   "runner shutting down",
			refuse: func(t *testing.T, f *purgeFixture) { f.runner.Shutdown(2 * time.Second) },
			cause:  orchestrator.ErrRunnerClosed.Error(),
			// Shutdown is checked before anything is recorded.
			wantRow: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPurgeFixture(t, 1)
			ctx := context.Background()
			tc.refuse(t, f)

			err := f.svc.Purge(ctx, f.qubeID, f.qubeName)
			require.Error(t, err, "a refused enqueue must be reported, not swallowed")
			assert.ErrorIs(t, err, ErrOrchestration)
			// The cause is flattened into the message rather than wrapped
			// (pre-existing: claimAndEnqueue renders it with %v), so it is
			// asserted as text.
			assert.Contains(t, err.Error(), tc.cause)

			// The error text an operator reads must state what was NOT destroyed:
			// "purge failed" alone would leave them guessing whether the data is
			// still recoverable.
			msg := err.Error()
			for _, want := range []string{
				"nothing was destroyed yet",
				"protection was not lifted",
				"data key was not deleted",
				"retry the purge to finish it",
			} {
				assert.Contains(t, msg, want, "the refusal must account for every irreversible effect")
			}
			assert.NotContains(t, msg, "identity was not revoked",
				"the claim's own transaction withdraws authorization; saying otherwise would be false")

			// ...and the state must agree with the text: lifting the protection and
			// shredding the key are what the job owns, and neither happened.
			inf, err := f.infra.Get(ctx, f.qubeID)
			require.NoError(t, err)
			require.NotNil(t, inf)
			assert.True(t, inf.Protected, "the data disk's protection latch was lifted before the job existed")
			assert.Empty(t, f.keys.destroyed(), "the per-qube key was deleted for a job that was never accepted")
			assert.Empty(t, f.exec.destroyCalls(), "destroy must not run for a refused job")

			// Authorization is the exception, and it is the CLAIM's doing rather
			// than the job's: purge_withdraw_identity revokes it in the same
			// transaction that records the intent. Asserted here so the note above
			// cannot drift from it.
			_, err = f.certs.Authorize(ctx, f.fingerprint)
			assert.ErrorIs(t, err, repository.ErrCertRevoked,
				"the purge claim withdraws authorization atomically; the refusal does not undo it")

			recorded, err := f.jobs.ListByQube(ctx, f.qubeID, 10)
			require.NoError(t, err)
			require.Len(t, recorded, boolToLen(tc.wantRow), "the refusal record must match how far the submit got")
			if tc.wantRow {
				assert.Equal(t, orchestrator.JobFailed, recorded[0].State)
				assert.Contains(t, recorded[0].Error, tc.cause)
			}

			// The claim is released so the qube is retryable, but the purge intent
			// stays recorded: purge is not something a later resume may undo.
			current, err := f.svc.GetByID(ctx, f.qubeID)
			require.NoError(t, err)
			assert.Equal(t, models.QubeStatusError, current.Status)
			assert.True(t, current.PurgeRequested)
		})
	}
}

// boolToLen maps an expectation onto testify's length argument: 1 row when the
// refusal got as far as recording the job, 0 when it did not.
func boolToLen(want bool) int {
	if want {
		return 1
	}
	return 0
}

// TestPurge_IrreversibleStepsRunInsideTheJobBeforeDestroy — the happy path, with
// the ordering made observable: destroy must see the disk already unprotected,
// and the key must already be gone.
func TestPurge_IrreversibleStepsRunInsideTheJobBeforeDestroy(t *testing.T) {
	f := newPurgeFixture(t, 8)
	ctx := context.Background()

	require.NoError(t, f.svc.Purge(ctx, f.qubeID, f.qubeName))
	job := f.terminalJob(t)
	require.Equal(t, orchestrator.JobSucceeded, job.State, "job error: %s", job.Error)
	assert.Equal(t, orchestrator.ActionDestroy, job.Action)

	assert.Equal(t, []string{"delete-key", "destroy:protected=false"}, f.log.snapshot(),
		"the key must be shredded before the provider is asked to destroy the disk, "+
			"and destroy must see the protection latch already lifted")

	assert.Equal(t, []string{f.qubeName}, f.exec.destroyCalls())
	assert.Equal(t, []string{f.qubeID}, f.keys.destroyed())
	_, err := f.certs.Authorize(ctx, f.fingerprint)
	assert.ErrorIs(t, err, repository.ErrCertRevoked, "a purged qube's identity must stop authenticating")

	current, err := f.svc.GetByID(ctx, f.qubeID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusPurged, current.Status)
}

// TestPurge_PartialPreparationIsReportedInTheJobRecord — the one case where
// part of the work does happen: a step fails half-way, so the job record has to
// say which irreversible steps completed. It is the same record the operator
// reads and the same one an audit reads.
func TestPurge_PartialPreparationIsReportedInTheJobRecord(t *testing.T) {
	f := newPurgeFixture(t, 8)
	ctx := context.Background()
	f.keys.setDeleteErr(errors.New("credential store offline"))

	require.NoError(t, f.svc.Purge(ctx, f.qubeID, f.qubeName),
		"the job is accepted; the preparation failure happens inside it")

	job := f.terminalJob(t)
	require.Equal(t, orchestrator.JobFailed, job.State)
	for _, want := range []string{
		"purge \"goner\"",
		"deleted the per-qube data key",
		"credential store offline",
		"already done and not undone: lifted the data disk's protection, revoked the agent identity",
		"the data disk was not destroyed",
	} {
		assert.Contains(t, job.Error, want, "the failed job must account for what it already destroyed")
	}
	assert.Empty(t, f.exec.destroyCalls(), "a half-prepared purge must not reach the provider")

	// The steps that did run are facts in the stores, not claims in a message.
	inf, err := f.infra.Get(ctx, f.qubeID)
	require.NoError(t, err)
	require.NotNil(t, inf)
	assert.False(t, inf.Protected)
	_, err = f.certs.Authorize(ctx, f.fingerprint)
	assert.ErrorIs(t, err, repository.ErrCertRevoked)
	assert.Empty(t, f.keys.destroyed())

	current, err := f.svc.GetByID(ctx, f.qubeID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusError, current.Status)
	assert.True(t, current.PurgeRequested)
}

// TestPurge_RetryAfterPartialPreparationIsIdempotent — after the failure above,
// the retry must run the same three steps without treating the already-cleared
// protection as uncleared or turning a second revocation into an error.
func TestPurge_RetryAfterPartialPreparationIsIdempotent(t *testing.T) {
	f := newPurgeFixture(t, 8)
	ctx := context.Background()
	f.keys.setDeleteErr(errors.New("credential store offline"))
	require.NoError(t, f.svc.Purge(ctx, f.qubeID, f.qubeName))
	require.Equal(t, orchestrator.JobFailed, f.terminalJob(t).State)

	// The operator fixes the store and retries; the qube is in "error", which
	// Purge accepts, and the purge intent is still recorded.
	f.keys.setDeleteErr(nil)
	require.NoError(t, f.svc.Purge(ctx, f.qubeID, f.qubeName))

	job := f.terminalJob(t)
	require.Equal(t, orchestrator.JobSucceeded, job.State, "retry job error: %s", job.Error)
	assert.Equal(t, 2, len(f.jobsRows(t)), "each attempt is its own job")

	// The retry re-ran all three steps: protection was already false (re-saved as
	// false, not mistaken for "still protected"), RevokeFor matched no unrevoked
	// row and reported 0 rather than an error, and the key deletion — the step
	// that failed — completed.
	assert.Equal(t, []string{f.qubeID, f.qubeID}, f.keys.attempts(),
		"the retry must attempt the step that never succeeded, not skip it")
	assert.Equal(t, []string{f.qubeID}, f.keys.destroyed())
	assert.Contains(t, f.log.snapshot(), "destroy:protected=false")

	_, err := f.certs.Authorize(ctx, f.fingerprint)
	assert.ErrorIs(t, err, repository.ErrCertRevoked, "the retry must not resurrect a revoked identity")

	current, err := f.svc.GetByID(ctx, f.qubeID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusPurged, current.Status)
}

// jobsRows reads the qube's job history, failing the test if it cannot.
func (f *purgeFixture) jobsRows(t *testing.T) []*orchestrator.Job {
	t.Helper()
	jobs, err := f.jobs.ListByQube(context.Background(), f.qubeID, 10)
	require.NoError(t, err)
	return jobs
}
