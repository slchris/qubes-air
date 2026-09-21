package service

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDataKeyManagerPerQubeKeyWithoutFallback — a qube gets its own random key
// and KeyFor never derives one in its place: absence of a stored key means the
// disk needs migration, not that a legacy key should be used silently.
func TestDataKeyManagerPerQubeKeyWithoutFallback(t *testing.T) {
	store := newMemCredStore()
	ctx := context.Background()
	m := NewDataKeyManager(store)

	key, found, err := m.KeyFor(ctx, "q1")
	require.NoError(t, err)
	assert.False(t, found, "an unprovisioned qube must report no stored key")
	assert.Empty(t, key)

	k1, err := m.EnsureDataKey(ctx, "q1")
	require.NoError(t, err)
	require.NotEmpty(t, k1)

	got, found, err := m.KeyFor(ctx, "q1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, k1, got)

	// Another qube gets a different key.
	k2, err := m.EnsureDataKey(ctx, "q2")
	require.NoError(t, err)
	assert.NotEqual(t, k1, k2)

	// Ensure is idempotent.
	again, err := m.EnsureDataKey(ctx, "q1")
	require.NoError(t, err)
	assert.Equal(t, k1, again)
}

// TestDataKeyManagerShredRemovesKeyAndMarker — purge deletes the only copy of
// the key, and the migration marker for that qube with it.
func TestDataKeyManagerShredRemovesKeyAndMarker(t *testing.T) {
	store := newMemCredStore()
	ctx := context.Background()
	m := NewDataKeyManager(store)

	_, err := m.EnsureDataKey(ctx, "q1")
	require.NoError(t, err)
	require.NoError(t, m.MarkMigrationPending(ctx, "q1"))

	require.NoError(t, m.DeleteDataKey(ctx, "q1"))
	_, found, err := m.KeyFor(ctx, "q1")
	require.NoError(t, err)
	assert.False(t, found, "a shredded key must not come back")

	list, err := store.List(ctx)
	require.NoError(t, err)
	for _, cred := range list {
		assert.NotContains(t, cred.Name, "q1", "no credential for the shredded qube may survive")
	}
}

// TestLegacyKeyForDerivesFromExistingMaster — the migration path reads the
// master that created the disk and derives exactly the key it was formatted
// with, stably across managers.
func TestLegacyKeyForDerivesFromExistingMaster(t *testing.T) {
	store := newMemCredStore()
	ctx := context.Background()
	master := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("A", 32)))
	_, err := store.Create(ctx, models.CredentialCreateRequest{
		Name: dataMasterCredentialName, Type: dataMasterCredentialType, SecretValue: master,
	})
	require.NoError(t, err)

	m := NewDataKeyManager(store)
	key, err := m.LegacyKeyFor(ctx, "q1")
	require.NoError(t, err)
	want, err := pki.DeriveDataKey(master, "q1")
	require.NoError(t, err)
	assert.Equal(t, want, key)

	fresh, err := NewDataKeyManager(store).LegacyKeyFor(ctx, "q1")
	require.NoError(t, err)
	assert.Equal(t, key, fresh, "the master must be read, not re-minted")

	// Deriving a legacy key does not create a per-qube key.
	_, found, err := m.KeyFor(ctx, "q1")
	require.NoError(t, err)
	assert.False(t, found)
}

// TestLegacyKeyForNeverMintsMaster — a fresh master cannot open an existing
// disk, so an absent master must stop the migration, not quietly create one.
func TestLegacyKeyForNeverMintsMaster(t *testing.T) {
	store := newMemCredStore()
	m := NewDataKeyManager(store)

	_, err := m.LegacyKeyFor(context.Background(), "q1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), dataMasterCredentialName)

	list, err := store.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, list, "a missing master must not be replaced by a fresh one")
}

func TestMigrationMarkerLifecycle(t *testing.T) {
	store := newMemCredStore()
	ctx := context.Background()
	m := NewDataKeyManager(store)

	pending, err := m.MigrationPending(ctx, "q1")
	require.NoError(t, err)
	assert.False(t, pending)

	require.NoError(t, m.MarkMigrationPending(ctx, "q1"))
	require.NoError(t, m.MarkMigrationPending(ctx, "q1"), "marking twice must be a no-op")
	pending, err = m.MigrationPending(ctx, "q1")
	require.NoError(t, err)
	assert.True(t, pending)

	other, err := m.MigrationPending(ctx, "q2")
	require.NoError(t, err)
	assert.False(t, other)

	require.NoError(t, m.ClearMigrationPending(ctx, "q1"))
	require.NoError(t, m.ClearMigrationPending(ctx, "q1"), "clearing twice must be a no-op")
	pending, err = m.MigrationPending(ctx, "q1")
	require.NoError(t, err)
	assert.False(t, pending)
}
