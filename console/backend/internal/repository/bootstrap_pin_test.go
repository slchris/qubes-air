package repository

import (
	"context"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/database/dbtest"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssuedTokenStoresOnlyItsPublicBootstrapPin(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()
	secret, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)

	got, err := repo.PendingPlaceholderSPKIFingerprint(ctx, "qube-1", "remote-dev", time.Now())
	require.NoError(t, err)
	want, err := pki.BootstrapPlaceholderSPKIFingerprint(secret, "remote-dev")
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Regexp(t, `^[0-9a-f]{64}$`, got)
	assert.NotContains(t, got, secret)

	list, err := repo.ListByQube(ctx, "qube-1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, want, list[0].PlaceholderSPKIFingerprint)
}

// Re-provisioning mints a new token; the pin must follow the newest one, or the
// console would pin a key the freshly booted agent no longer holds.
func TestPendingBootstrapPinFollowsTheNewestToken(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()
	_, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond) // created_at orders the two rows
	newer, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)

	got, err := repo.PendingPlaceholderSPKIFingerprint(ctx, "qube-1", "remote-dev", time.Now())
	require.NoError(t, err)
	want, err := pki.BootstrapPlaceholderSPKIFingerprint(newer, "remote-dev")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestPendingBootstrapPinFailsClosed(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()
	now := time.Now()
	secret, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Minute)
	require.NoError(t, err)

	_, err = repo.PendingPlaceholderSPKIFingerprint(ctx, "other-id", "remote-dev", now)
	require.ErrorContains(t, err, "no unredeemed, unexpired bootstrap token", "another qube's token must not pin this one")
	_, err = repo.PendingPlaceholderSPKIFingerprint(ctx, "qube-1", "remote-other", now)
	require.ErrorContains(t, err, "no unredeemed, unexpired bootstrap token", "a token is pinned to the name it was minted for")
	_, err = repo.PendingPlaceholderSPKIFingerprint(ctx, "qube-1", "remote-dev", now.Add(2*time.Minute))
	require.ErrorContains(t, err, "no unredeemed, unexpired bootstrap token", "an expired token pins nothing")

	_, err = repo.Redeem(ctx, secret, now)
	require.NoError(t, err)
	_, err = repo.PendingPlaceholderSPKIFingerprint(ctx, "qube-1", "remote-dev", now)
	require.ErrorContains(t, err, "no unredeemed, unexpired bootstrap token", "a spent token pins nothing")
	require.ErrorIs(t, err, ErrNoBootstrapPin)
}

func TestLegacyBootstrapTokenWithoutPinFailsClosed(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()
	_, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)
	_, err = repo.db.DB().ExecContext(ctx,
		"UPDATE bootstrap_tokens SET placeholder_spki_sha256 = '' WHERE qube_id = ?", "qube-1")
	require.NoError(t, err)

	_, err = repo.PendingPlaceholderSPKIFingerprint(ctx, "qube-1", "remote-dev", time.Now())
	require.ErrorContains(t, err, "predates peer pinning")
	require.ErrorIs(t, err, ErrNoBootstrapPin, "callers must be able to tell this from a failed lookup")
}

// A failed lookup is NOT ErrNoBootstrapPin: the caller must not tell the
// operator to re-provision a qube because the database hiccuped.
func TestPendingBootstrapPinLookupFailureIsNotNoPin(t *testing.T) {
	repo := tokenRepo(t)
	require.NoError(t, repo.db.Close())
	_, err := repo.PendingPlaceholderSPKIFingerprint(context.Background(), "qube-1", "remote-dev", time.Now())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNoBootstrapPin)
}

// openV2FixtureDB opens (and so upgrades) the frozen schema-v2 database the
// database package's upgrade tests use, via the shared dbtest fixture.
func openV2FixtureDB(t *testing.T) *database.DB {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = dbtest.WriteV2Fixture(t)
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	dbtest.AssertV2RowsPreserved(t, db.DB())
	return db
}

// A token that was outstanding when the console was upgraded to schema 3 has
// no pin. The console must refuse to dial for it rather than fall back to an
// unauthenticated handshake, and re-provisioning (a new token) must work.
func TestTokenOutstandingAcrossTheUpgradeFailsClosed(t *testing.T) {
	repo := NewBootstrapTokenRepository(openV2FixtureDB(t))
	ctx := context.Background()
	insideTTL := time.Date(2026, 9, 20, 10, 10, 0, 0, time.UTC)

	_, err := repo.PendingPlaceholderSPKIFingerprint(ctx, "qube-pending", "remote-pending", insideTTL)
	require.ErrorContains(t, err, "predates peer pinning")

	secret, err := repo.Issue(ctx, "qube-pending", "remote-pending", time.Hour)
	require.NoError(t, err)
	got, err := repo.PendingPlaceholderSPKIFingerprint(ctx, "qube-pending", "remote-pending", time.Now())
	require.NoError(t, err)
	want, err := pki.BootstrapPlaceholderSPKIFingerprint(secret, "remote-pending")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
