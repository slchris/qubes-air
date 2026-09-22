package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/lockfile"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStrandedQube opens a real database in a temp directory and plants the row
// the startup reconcile exists to rewrite: a qube left in a transient status.
// It is written through the repositories the server uses, so the status read
// back afterwards is the status reconcile would have overwritten.
func newStrandedQube(t *testing.T) (repository.QubeRepository, *models.Qube) {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "qubes-air.db")
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	require.NoError(t, repository.NewZoneRepository(db).Create(ctx, &models.Zone{
		ID: "zone-1", Name: "zone", Type: models.ZoneTypeProxmox, Status: "connected",
	}))

	qubeRepo := repository.NewQubeRepository(db)
	qube := &models.Qube{
		ID: "qube-1", Name: "in-flight", Type: models.QubeTypeApp,
		ZoneID: "zone-1", Status: models.QubeStatusCreating,
		Spec: models.QubeSpec{VCPU: 2, Memory: 2048},
	}
	require.NoError(t, qubeRepo.Create(ctx, qube))
	return qubeRepo, qube
}

// TestBootLockedRefusesToReconcileWhenTheLockIsHeld is the defect this lock
// exists for: a second console starting against a database a live console is
// serving. The lock is held exactly as the running instance holds it, and the
// boot step injected here is the REAL reconcileStrandedQubes over a real
// database, recording whether it was reached at all.
func TestBootLockedRefusesToReconcileWhenTheLockIsHeld(t *testing.T) {
	qubeRepo, qube := newStrandedQube(t)
	ctx := context.Background()

	lockPath := filepath.Join(t.TempDir(), "qubes-air.db.lock")
	live, err := lockfile.Acquire(lockPath)
	require.NoError(t, err)
	defer func() { _ = live.Release() }()

	reconciled := 0
	second, err := bootLocked(lockPath, func() error {
		reconciled++
		reconcileStrandedQubes(ctx, qubeRepo)
		return nil
	})

	require.Error(t, err, "a second console must refuse to start while the first holds the lock")
	assert.Nil(t, second, "a refused start must not hand back a lock")
	assert.Equal(t, 0, reconciled, "the startup reconcile must not run without the lock")
	assert.Contains(t, err.Error(), lockPath, "the refusal must name the lock file")
	assert.Contains(t, err.Error(), strconv.Itoa(os.Getpid()),
		"the refusal must name the holder pid")

	after, err := qubeRepo.GetByID(ctx, qube.ID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusCreating, after.Status,
		"the live instance's in-flight qube must be left untouched")
}

// TestReconcileStrandedQubesRewritesTheRow is the positive control for the test
// above: without it, "the qube is still creating" would hold just as well if
// reconcileStrandedQubes did nothing, and the refusal test would prove nothing
// about what it prevented.
func TestReconcileStrandedQubesRewritesTheRow(t *testing.T) {
	qubeRepo, qube := newStrandedQube(t)
	ctx := context.Background()

	reconcileStrandedQubes(ctx, qubeRepo)

	after, err := qubeRepo.GetByID(ctx, qube.ID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusError, after.Status,
		"reconcile must rewrite the transient status it finds")
}

// TestBootLockedHoldsTheLockAfterReturning pins the other half of the contract:
// the lock handed back by bootLocked is still held, because main keeps it for
// the whole process. Releasing it when boot returns would make single-instance
// protection last only as long as startup.
func TestBootLockedHoldsTheLockAfterReturning(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "qubes-air.db.lock")

	booted := false
	lock, err := bootLocked(lockPath, func() error { booted = true; return nil })
	require.NoError(t, err)
	require.True(t, booted)

	contender, err := lockfile.Acquire(lockPath)
	require.Error(t, err, "the lock must still be held after boot has returned")
	assert.Nil(t, contender)

	require.NoError(t, lock.Release())
	next, err := lockfile.Acquire(lockPath)
	require.NoError(t, err, "releasing must hand the lock back")
	require.NoError(t, next.Release())
}

// TestBootLockedWithoutLockPathStillBoots covers the one configuration with no
// lock: an in-memory database has no file another process could share, so the
// absence of a lock path must not stop the console from starting.
func TestBootLockedWithoutLockPathStillBoots(t *testing.T) {
	booted := false

	lock, err := bootLocked("", func() error { booted = true; return nil })

	require.NoError(t, err)
	assert.True(t, booted, "boot must still run when there is nothing to lock")
	assert.Nil(t, lock, "an unlocked start must report no lock")
}

// TestBootLockedReleasesTheLockWhenBootFails pins what happens on a failed
// start: main turns the error into a fatal log, and the lock must be handed
// back rather than left held by a process on its way out.
func TestBootLockedReleasesTheLockWhenBootFails(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "qubes-air.db.lock")
	bootErr := errors.New("dependency wiring failed")

	lock, err := bootLocked(lockPath, func() error { return bootErr })

	require.ErrorIs(t, err, bootErr)
	assert.Nil(t, lock)

	next, err := lockfile.Acquire(lockPath)
	require.NoError(t, err, "a failed boot must not leave the lock held")
	require.NoError(t, next.Release())
}
