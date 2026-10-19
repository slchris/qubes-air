package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
)

func openJobLogDB(t *testing.T) *database.DB {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "jobs-*.db")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	cfg := database.DefaultConfig()
	cfg.DSN = f.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertRetentionJob(t *testing.T, repo *repository.JobRepository, id string) {
	t.Helper()
	require.NoError(t, repo.Insert(context.Background(), &orchestrator.Job{
		ID: id, QubeID: "q1", QubeName: "remote-one",
		Action: orchestrator.ActionProvision, State: orchestrator.JobSucceeded,
		EnqueuedAt: time.Now().UTC(),
	}))
}

// The wired retention pass must remove files past the window, leave recent ones
// alone, and record the removal on the job row so the API can report "expired"
// instead of an unexplained empty log.
func TestJobLogRetentionPrunesOldFilesAndRecordsThem(t *testing.T) {
	db := openJobLogDB(t)
	repo := repository.NewJobRepository(db)
	dir := t.TempDir()
	logs, err := orchestrator.NewJobLogStore(dir)
	require.NoError(t, err)

	for _, id := range []string{"old", "new"} {
		insertRetentionJob(t, repo, id)
		f, err := logs.Create(id)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	old := time.Now().Add(-jobLogRetention - time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "old.log"), old, old))

	cancel, done := startJobLogRetention(logs, repo)
	require.NotNil(t, cancel)
	require.NotNil(t, done)
	defer func() {
		cancel()
		<-done
	}()

	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	var got *orchestrator.Job
	for {
		got, err = repo.GetByID(ctx, "old")
		require.NoError(t, err)
		if got.LogPrunedAt != nil {
			break
		}
		require.True(t, time.Now().Before(deadline), "the wired retention pass never marked the expired file")
		time.Sleep(5 * time.Millisecond)
	}

	_, err = os.Stat(filepath.Join(dir, "old.log"))
	assert.True(t, os.IsNotExist(err), "an expired log file survived the retention pass")
	_, err = os.Stat(filepath.Join(dir, "new.log"))
	assert.NoError(t, err, "a log inside the retention window was removed")

	fresh, err := repo.GetByID(ctx, "new")
	require.NoError(t, err)
	assert.Nil(t, fresh.LogPrunedAt, "a job whose log was kept must not be marked pruned")
}

// Orchestration or job history can be disabled; the wiring must then start
// nothing rather than dereference a nil store.
func TestJobLogRetentionIsNilSafe(t *testing.T) {
	cancel, done := startJobLogRetention(nil, nil)
	assert.Nil(t, cancel)
	assert.Nil(t, done)
}
