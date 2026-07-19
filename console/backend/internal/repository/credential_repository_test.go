package repository

import (
	"context"
	"testing"

	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	oldKey = "0123456789abcdef0123456789abcdef" // 32 bytes (version 1)
	newKey = "fedcba9876543210fedcba9876543210" // 32 bytes (version 2)
)

// keyVersionOf reads the raw key_version column for a credential row so tests
// can assert which key encrypted it.
func keyVersionOf(t *testing.T, repo *CredentialRepository, id string) int {
	t.Helper()
	var v int
	err := repo.db.DB().QueryRow(`SELECT key_version FROM credentials WHERE id = ?`, id).Scan(&v)
	require.NoError(t, err)
	return v
}

func TestCredential_CreateGetSecret_SingleKey(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	kr, err := keyring.NewSingle([]byte(oldKey))
	require.NoError(t, err)
	repo := NewCredentialRepository(db, kr)
	ctx := context.Background()

	cred, err := repo.Create(ctx, models.CredentialCreateRequest{
		Name: "cloud-api", Type: "api_key", SecretValue: "s3cr3t-token",
	})
	require.NoError(t, err)

	// New rows are stamped with the primary version (1).
	assert.Equal(t, 1, keyVersionOf(t, repo, cred.ID))

	got, err := repo.GetSecret(ctx, cred.ID)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t-token", got)
}

// TestCredential_RotationBackwardCompatible is the core acceptance test:
// after rotating to a new key, OLD credentials still decrypt AND NEW
// credentials are encrypted with the new key.
func TestCredential_RotationBackwardCompatible(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// --- Phase 1: single key (v1). Create a credential encrypted with v1. ---
	kr1, err := keyring.NewSingle([]byte(oldKey))
	require.NoError(t, err)
	repoV1 := NewCredentialRepository(db, kr1)

	old, err := repoV1.Create(ctx, models.CredentialCreateRequest{
		Name: "relay-ssh", Type: "ssh_key", SecretValue: "OLD-SECRET",
	})
	require.NoError(t, err)
	require.Equal(t, 1, keyVersionOf(t, repoV1, old.ID))

	// --- Phase 2: add v2 alongside v1 (rotation window). v2 is primary. ---
	kr2, err := keyring.ParseSpec("v1:" + oldKey + ",v2:" + newKey)
	require.NoError(t, err)
	require.Equal(t, 2, kr2.PrimaryVersion())
	repoV2 := NewCredentialRepository(db, kr2)

	// The pre-rotation v1 credential must STILL be decryptable (this is the
	// exact failure mode the versioning fixes).
	got, err := repoV2.GetSecret(ctx, old.ID)
	require.NoError(t, err)
	assert.Equal(t, "OLD-SECRET", got)

	// A NEW credential created now must use the new primary key (v2).
	fresh, err := repoV2.Create(ctx, models.CredentialCreateRequest{
		Name: "new-cred", Type: "api_key", SecretValue: "NEW-SECRET",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, keyVersionOf(t, repoV2, fresh.ID), "new credential uses new key")

	// --- Phase 3: run rotation. All rows move to v2, values preserved. ---
	stats, err := repoV2.RotateToPrimary(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, stats.Total)
	assert.Equal(t, 1, stats.Reencrypted, "only the v1 row is re-encrypted")
	assert.Equal(t, 1, stats.AlreadyCurrent, "the v2 row is left untouched")
	assert.Equal(t, 2, stats.TargetVersion)

	assert.Equal(t, 2, keyVersionOf(t, repoV2, old.ID), "old row now stamped v2")
	assert.Equal(t, 2, keyVersionOf(t, repoV2, fresh.ID))

	// Both secrets still decrypt after rotation.
	gotOld, err := repoV2.GetSecret(ctx, old.ID)
	require.NoError(t, err)
	assert.Equal(t, "OLD-SECRET", gotOld)
	gotNew, err := repoV2.GetSecret(ctx, fresh.ID)
	require.NoError(t, err)
	assert.Equal(t, "NEW-SECRET", gotNew)

	// --- Phase 4: drop the old key entirely. Everything still works. ---
	kr3, err := keyring.ParseSpec("v2:" + newKey)
	require.NoError(t, err)
	repoV3 := NewCredentialRepository(db, kr3)

	finalOld, err := repoV3.GetSecret(ctx, old.ID)
	require.NoError(t, err)
	assert.Equal(t, "OLD-SECRET", finalOld)
}

// TestCredential_RotationIsIdempotent verifies a second rotation pass is a
// no-op (resumable after a partial/aborted run).
func TestCredential_RotationIsIdempotent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	kr, err := keyring.ParseSpec("v1:" + oldKey + ",v2:" + newKey)
	require.NoError(t, err)
	repo := NewCredentialRepository(db, kr)

	_, err = repo.Create(ctx, models.CredentialCreateRequest{
		Name: "c", Type: "api_key", SecretValue: "x",
	})
	require.NoError(t, err)

	first, err := repo.RotateToPrimary(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, first.Reencrypted, "new row was already primary v2")

	second, err := repo.RotateToPrimary(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, second.Reencrypted)
	assert.Equal(t, 1, second.AlreadyCurrent)
}

// TestCredential_RotationFailClosedOnMissingKey verifies that if a row's key
// version is not in the keyring, rotation aborts WITHOUT partially mutating the
// table (atomicity).
func TestCredential_RotationFailClosedOnMissingKey(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create a v1 row.
	kr1, err := keyring.NewSingle([]byte(oldKey))
	require.NoError(t, err)
	repoV1 := NewCredentialRepository(db, kr1)
	cred, err := repoV1.Create(ctx, models.CredentialCreateRequest{
		Name: "c", Type: "api_key", SecretValue: "secret",
	})
	require.NoError(t, err)

	// Build a keyring that has ONLY v2 (missing the v1 key the row needs) and a
	// v2 primary, then attempt rotation — decryption of the v1 row must fail.
	krBad, err := keyring.New(map[int][]byte{2: []byte(newKey)}, 2)
	require.NoError(t, err)
	repoBad := NewCredentialRepository(db, krBad)

	_, err = repoBad.RotateToPrimary(ctx)
	require.Error(t, err, "rotation must fail when a key version is missing")

	// The row must be untouched: still v1, still the original ciphertext that
	// decrypts under the original key.
	assert.Equal(t, 1, keyVersionOf(t, repoV1, cred.ID))
	got, err := repoV1.GetSecret(ctx, cred.ID)
	require.NoError(t, err)
	assert.Equal(t, "secret", got)
}

// TestCredential_UpdateReencryptsWithPrimary verifies that updating a secret
// re-stamps the row with the current primary version.
func TestCredential_UpdateReencryptsWithPrimary(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create under v1.
	kr1, err := keyring.NewSingle([]byte(oldKey))
	require.NoError(t, err)
	repoV1 := NewCredentialRepository(db, kr1)
	cred, err := repoV1.Create(ctx, models.CredentialCreateRequest{
		Name: "c", Type: "api_key", SecretValue: "v1secret",
	})
	require.NoError(t, err)

	// Update the secret under a v2-primary keyring.
	kr2, err := keyring.ParseSpec("v1:" + oldKey + ",v2:" + newKey)
	require.NoError(t, err)
	repoV2 := NewCredentialRepository(db, kr2)

	newSecret := "v2secret"
	_, err = repoV2.Update(ctx, cred.ID, models.CredentialUpdateRequest{SecretValue: &newSecret})
	require.NoError(t, err)

	assert.Equal(t, 2, keyVersionOf(t, repoV2, cred.ID))
	got, err := repoV2.GetSecret(ctx, cred.ID)
	require.NoError(t, err)
	assert.Equal(t, "v2secret", got)
}
