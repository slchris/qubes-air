package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tokenRepo(t *testing.T) *BootstrapTokenRepository {
	t.Helper()
	db, cleanup := setupQubeTestDB(t)
	t.Cleanup(cleanup)
	return NewBootstrapTokenRepository(db)
}

func TestIssuedTokenRedeemsToItsQube(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()

	secret, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, secret)

	tok, err := repo.Redeem(ctx, secret, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "qube-1", tok.QubeID)
	assert.Equal(t, "remote-dev", tok.QubeName)
	require.NotNil(t, tok.RedeemedAt)
}

// The stored row must not contain anything replayable — the same reason a
// password store keeps digests.
func TestStoredRowDoesNotContainTheSecret(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()

	secret, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)

	list, err := repo.ListByQube(ctx, "qube-1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.NotEqual(t, secret, list[0].SecretHash)
	assert.NotContains(t, list[0].SecretHash, secret)
}

func TestSecondRedemptionIsRefused(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()

	secret, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)

	_, err = repo.Redeem(ctx, secret, time.Now())
	require.NoError(t, err)

	_, err = repo.Redeem(ctx, secret, time.Now())
	require.ErrorIs(t, err, ErrBootstrapTokenRejected)
	assert.Contains(t, err.Error(), "already redeemed",
		"the operator-facing reason should name what actually happened")
}

func TestExpiredTokenIsRefused(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()

	secret, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Minute)
	require.NoError(t, err)

	_, err = repo.Redeem(ctx, secret, time.Now().Add(2*time.Minute))
	require.ErrorIs(t, err, ErrBootstrapTokenRejected)
	assert.Contains(t, err.Error(), "expired")
}

func TestUnknownSecretIsRefused(t *testing.T) {
	repo := tokenRepo(t)

	_, err := repo.Redeem(context.Background(), "not-a-real-token", time.Now())
	require.ErrorIs(t, err, ErrBootstrapTokenRejected)
}

// TestConcurrentRedemptionHasExactlyOneWinner checks that parallel redemptions
// of one token produce one certificate holder, not several.
//
// Read this before trusting it. It is a SMOKE TEST, not a guard, and the
// difference was measured rather than assumed: swapping Redeem for a naive
// SELECT-then-UPDATE — the exact bug this is nominally aimed at — still passes
// here, at 12 racers and at 300. The natural window between those two
// statements is narrower than the scheduler's granularity, so the interleaving
// never happens. Widening it with a 5ms sleep makes the naive version fail with
// all 12 winning, which confirms the test's shape is right and its sensitivity
// is not.
//
// So what actually holds the property? The single UPDATE in Redeem, structurally
// — there is no window to lose, rather than a window we hope stays shut. The
// conditional logic in that statement (unredeemed, unexpired) is covered
// deterministically by TestSecondRedemptionIsRefused and TestExpiredTokenIsRefused.
// This test stays because it is nearly free and would catch a gross regression
// under load, but a change to Redeem must be reviewed against the statement, not
// against a green run here.
func TestConcurrentRedemptionHasExactlyOneWinner(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()

	secret, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)

	const racers = 12
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		wins    int
		refused int
		other   []error
		start   = make(chan struct{})
	)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together to make the window as wide as it gets
			_, err := repo.Redeem(ctx, secret, time.Now())
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrBootstrapTokenRejected):
				refused++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Empty(t, other, "redemption failed for a reason other than refusal")
	assert.Equal(t, 1, wins, "exactly one caller may redeem a single-use token")
	assert.Equal(t, racers-1, refused)
}

// A qube re-provisioned after a failed attempt must not be claimable with the
// token left over from that attempt.
func TestInvalidateForQubeSpendsOutstandingTokens(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()

	stale, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)

	n, err := repo.InvalidateForQube(ctx, "qube-1", time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	_, err = repo.Redeem(ctx, stale, time.Now())
	require.ErrorIs(t, err, ErrBootstrapTokenRejected)

	// A token minted afterwards still works — invalidation is not a lockout.
	fresh, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)
	_, err = repo.Redeem(ctx, fresh, time.Now())
	require.NoError(t, err)
}

// Invalidating one qube's tokens must not touch another's.
func TestInvalidateIsScopedToOneQube(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()

	other, err := repo.Issue(ctx, "qube-2", "remote-prod", time.Hour)
	require.NoError(t, err)
	_, err = repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)

	_, err = repo.InvalidateForQube(ctx, "qube-1", time.Now())
	require.NoError(t, err)

	tok, err := repo.Redeem(ctx, other, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "qube-2", tok.QubeID)
}

func TestDeleteSpentKeepsLiveTokens(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()

	spent, err := repo.Issue(ctx, "qube-1", "remote-dev", time.Hour)
	require.NoError(t, err)
	_, err = repo.Redeem(ctx, spent, time.Now())
	require.NoError(t, err)

	live, err := repo.Issue(ctx, "qube-2", "remote-prod", time.Hour)
	require.NoError(t, err)

	n, err := repo.DeleteSpent(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "only the redeemed token should go")

	_, err = repo.Redeem(ctx, live, time.Now())
	assert.NoError(t, err, "an unredeemed, unexpired token must survive retention")
}

func TestIssueRejectsUnusableArguments(t *testing.T) {
	repo := tokenRepo(t)
	ctx := context.Background()

	_, err := repo.Issue(ctx, "", "remote-dev", time.Hour)
	assert.Error(t, err, "accepted an empty qube id")

	_, err = repo.Issue(ctx, "qube-1", "", time.Hour)
	assert.Error(t, err, "accepted an empty qube name")

	_, err = repo.Issue(ctx, "qube-1", "remote-dev", 0)
	assert.Error(t, err, "accepted a zero lifetime")
}
