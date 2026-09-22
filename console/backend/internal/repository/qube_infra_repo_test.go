package repository

import (
	"context"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQubeInfraRepository_GetMissingIsNil(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()

	inf, err := NewQubeInfraRepository(db).Get(context.Background(), "does-not-exist")
	require.NoError(t, err)
	assert.Nil(t, inf)
}

func TestQubeInfraRepository_SaveAndGet(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewQubeInfraRepository(db)
	ctx := context.Background()

	observedAt := time.Now().UTC().Truncate(time.Second)
	want := &provider.Infra{
		QubeID:        "q1",
		Provider:      "proxmox",
		Node:          "infra-node1",
		StorageVMID:   100,
		ComputeVMID:   101,
		DataVolume:    "ceph-pve:vm-100-disk-0",
		IdentityVol:   "local:snippets/agent-q1.yaml",
		ObservedState: "running",
		Protected:     true,
		ObservedAt:    &observedAt,
	}
	require.NoError(t, repo.Save(ctx, want))

	got, err := repo.Get(ctx, "q1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "proxmox", got.Provider)
	assert.Equal(t, "infra-node1", got.Node)
	assert.Equal(t, 100, got.StorageVMID)
	assert.Equal(t, 101, got.ComputeVMID)
	assert.Equal(t, "ceph-pve:vm-100-disk-0", got.DataVolume)
	assert.Equal(t, "local:snippets/agent-q1.yaml", got.IdentityVol)
	assert.Equal(t, "running", got.ObservedState)
	assert.True(t, got.Protected)
	require.NotNil(t, got.ObservedAt)
	assert.WithinDuration(t, observedAt, *got.ObservedAt, 2*time.Second)
}

func TestQubeInfraRepository_SaveUpserts(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewQubeInfraRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Save(ctx, &provider.Infra{
		QubeID: "q1", Provider: "proxmox", StorageVMID: 100, ComputeVMID: 101, Protected: true,
	}))
	// A suspend clears compute and (if the operator opted out) protection.
	require.NoError(t, repo.Save(ctx, &provider.Infra{
		QubeID: "q1", Provider: "proxmox", StorageVMID: 100, ComputeVMID: 0,
		ObservedState: "suspended", Protected: false,
	}))

	got, err := repo.Get(ctx, "q1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Zero(t, got.ComputeVMID)
	assert.False(t, got.Protected)
	assert.Equal(t, "suspended", got.ObservedState)
}

func TestQubeInfraRepository_SaveRequiresQubeID(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()

	err := NewQubeInfraRepository(db).Save(context.Background(), &provider.Infra{Provider: "proxmox"})
	require.Error(t, err)
}

func TestQubeInfraRepository_DeleteRemovesRow(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewQubeInfraRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Save(ctx, &provider.Infra{QubeID: "q1", Provider: "proxmox"}))
	require.NoError(t, repo.Delete(ctx, "q1"))

	got, err := repo.Get(ctx, "q1")
	require.NoError(t, err)
	assert.Nil(t, got)
}
