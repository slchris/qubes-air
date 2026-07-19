package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newJob(id, qubeID, name string, action orchestrator.Action, at time.Time) *orchestrator.Job {
	return &orchestrator.Job{
		ID:         id,
		QubeID:     qubeID,
		QubeName:   name,
		Action:     action,
		State:      orchestrator.JobQueued,
		EnqueuedAt: at,
	}
}

func TestJobRepository_RoundTrip(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewJobRepository(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	j := newJob("job-1", "qube-1", "dev-work", orchestrator.ActionResume, now)
	require.NoError(t, repo.Insert(ctx, j))

	got, err := repo.GetByID(ctx, "job-1")
	require.NoError(t, err)
	assert.Equal(t, orchestrator.ActionResume, got.Action)
	assert.Equal(t, orchestrator.JobQueued, got.State)
	assert.Equal(t, "dev-work", got.QubeName)
	assert.Nil(t, got.StartedAt, "a queued job has not started")
	assert.Nil(t, got.FinishedAt)

	// Progress through the lifecycle.
	started := now.Add(time.Second)
	j.State = orchestrator.JobRunning
	j.StartedAt = &started
	require.NoError(t, repo.Update(ctx, j))

	finished := now.Add(5 * time.Minute)
	j.State = orchestrator.JobFailed
	j.Error = "terraform apply failed: prevent_destroy"
	j.FinishedAt = &finished
	require.NoError(t, repo.Update(ctx, j))

	got, err = repo.GetByID(ctx, "job-1")
	require.NoError(t, err)
	assert.Equal(t, orchestrator.JobFailed, got.State)
	assert.Contains(t, got.Error, "prevent_destroy", "the failure reason is the point of the audit record")
	require.NotNil(t, got.StartedAt)
	require.NotNil(t, got.FinishedAt)
	assert.Equal(t, 5*time.Minute, got.FinishedAt.Sub(*got.StartedAt).Round(time.Minute))
}

// TestJobRepository_UpdateDoesNotRewriteIdentity — what a job was asked to do is
// immutable once recorded. If Update could change qube_id or action, the table
// would not be trustworthy as an audit trail.
func TestJobRepository_UpdateDoesNotRewriteIdentity(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewJobRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	require.NoError(t, repo.Insert(ctx, newJob("job-x", "qube-a", "alpha", orchestrator.ActionProvision, now)))

	tampered := newJob("job-x", "qube-b", "beta", orchestrator.ActionDestroy, now)
	tampered.State = orchestrator.JobSucceeded
	require.NoError(t, repo.Update(ctx, tampered))

	got, err := repo.GetByID(ctx, "job-x")
	require.NoError(t, err)
	assert.Equal(t, "qube-a", got.QubeID, "qube_id must be immutable")
	assert.Equal(t, "alpha", got.QubeName, "qube_name must be immutable")
	assert.Equal(t, orchestrator.ActionProvision, got.Action, "action must be immutable")
	assert.Equal(t, orchestrator.JobSucceeded, got.State, "state is the mutable part")
}

func TestJobRepository_UpdateUnknownJob(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewJobRepository(db)

	err := repo.Update(context.Background(), newJob("nope", "q", "n", orchestrator.ActionResume, time.Now()))
	assert.ErrorIs(t, err, ErrJobNotFound)
}

func TestJobRepository_ListByQubeNewestFirst(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewJobRepository(db)
	ctx := context.Background()

	base := time.Now().UTC()
	require.NoError(t, repo.Insert(ctx, newJob("old", "q1", "n", orchestrator.ActionProvision, base)))
	require.NoError(t, repo.Insert(ctx, newJob("mid", "q1", "n", orchestrator.ActionSuspend, base.Add(time.Minute))))
	require.NoError(t, repo.Insert(ctx, newJob("new", "q1", "n", orchestrator.ActionResume, base.Add(2*time.Minute))))
	require.NoError(t, repo.Insert(ctx, newJob("other", "q2", "m", orchestrator.ActionResume, base.Add(3*time.Minute))))

	got, err := repo.ListByQube(ctx, "q1", 0)
	require.NoError(t, err)
	require.Len(t, got, 3, "must not leak another qube's jobs")
	assert.Equal(t, "new", got[0].ID)
	assert.Equal(t, "mid", got[1].ID)
	assert.Equal(t, "old", got[2].ID)
}

// TestJobRepository_HistorySurvivesQubeRelease — a released qube's history is
// exactly what an audit wants to read, so job rows must not be tied to the
// qube's lifetime.
func TestJobRepository_HistorySurvivesQubeRelease(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	jobRepo := NewJobRepository(db)
	qubeRepo := NewQubeRepository(db)
	zone := createTestZone(t, NewZoneRepository(db))
	ctx := context.Background()

	id := claimTestQube(t, qubeRepo, zone.ID, "doomed", "running")
	require.NoError(t, jobRepo.Insert(ctx, newJob("j1", id, "doomed", orchestrator.ActionProvision, time.Now().UTC())))

	require.NoError(t, qubeRepo.Delete(ctx, id))

	got, err := jobRepo.ListByQube(ctx, id, 0)
	require.NoError(t, err)
	assert.Len(t, got, 1, "job history must outlive the qube row")
}

// TestJobRepository_FailUnfinished — the queue is in memory, so anything left
// queued or running at startup belonged to a dead process. Leaving those rows
// would have the audit trail claim work is in flight that nobody is doing.
func TestJobRepository_FailUnfinished(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewJobRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	queued := newJob("q", "q1", "n", orchestrator.ActionResume, now)
	running := newJob("r", "q1", "n", orchestrator.ActionSuspend, now)
	running.State = orchestrator.JobRunning
	done := newJob("d", "q1", "n", orchestrator.ActionProvision, now)
	done.State = orchestrator.JobSucceeded

	for _, j := range []*orchestrator.Job{queued, running, done} {
		require.NoError(t, repo.Insert(ctx, j))
	}

	n, err := repo.FailUnfinished(ctx, "console restarted")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	for _, id := range []string{"q", "r"} {
		got, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, orchestrator.JobFailed, got.State)
		assert.Contains(t, got.Error, "restarted")
		assert.NotNil(t, got.FinishedAt, "a terminal job must have a finish time")
	}

	// A completed job must not be rewritten.
	got, err := repo.GetByID(ctx, "d")
	require.NoError(t, err)
	assert.Equal(t, orchestrator.JobSucceeded, got.State)
	assert.Empty(t, got.Error)
}

// TestJobRepository_SatisfiesStoreInterface pins the repository to the
// orchestrator's expectations at compile time.
func TestJobRepository_SatisfiesStoreInterface(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()

	var store orchestrator.JobStore = NewJobRepository(db)
	_, err := store.GetByID(context.Background(), "missing")
	assert.True(t, errors.Is(err, ErrJobNotFound))
}
