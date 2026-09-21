package repository

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLifecycleIntentAndReservationSurviveReopen(t *testing.T) {
	cfg := database.DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "lifecycle.db")
	db, err := database.New(cfg)
	require.NoError(t, err)
	zone := createTestZone(t, NewZoneRepository(db))
	qubes := NewQubeRepository(db)
	id := claimTestQube(t, qubes, zone.ID, "remote-restart", models.QubeStatusError)
	ctx := context.Background()
	require.NoError(t, NewQubeInfraRepository(db).Save(ctx, &provider.Infra{
		QubeID: id, Provider: "proxmox", Node: "node1", StorageVMID: 105, ComputeVMID: 106,
		DataVolume: "ceph:vm-105-disk-0", Protected: true,
	}))
	require.NoError(t, qubes.ClaimPurge(ctx, id, []models.QubeStatus{models.QubeStatusError}))
	require.NoError(t, db.Close())
	db, err = database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	qubes = NewQubeRepository(db)
	q, err := qubes.GetByID(ctx, id)
	require.NoError(t, err)
	assert.True(t, q.PurgeRequested)
	in, err := NewQubeInfraRepository(db).Get(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, in)
	assert.Equal(t, 105, in.StorageVMID)
	assert.Equal(t, 106, in.ComputeVMID)
	require.NoError(t, qubes.UpdateStatus(ctx, id, models.QubeStatusError))
	require.ErrorIs(t, qubes.ClaimTransition(ctx, id, []models.QubeStatus{models.QubeStatusError}, models.QubeStatusResuming), ErrTransitionConflict)
	require.NoError(t, qubes.ClaimPurge(ctx, id, []models.QubeStatus{models.QubeStatusError}))
}
