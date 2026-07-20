package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/pki"
)

// ErrBootstrapTokenRejected is the ONLY error a redemption failure produces for
// the caller.
//
// Unknown, expired and already-redeemed are deliberately indistinguishable: an
// attacker probing the endpoint learns nothing about which half of a guess was
// wrong, and "this token existed but is spent" is itself information worth
// withholding. The distinction is not lost — Redeem logs it (see explain), so
// the operator debugging a failed boot still gets a precise answer.
var ErrBootstrapTokenRejected = errors.New("bootstrap token not accepted")

// BootstrapToken is a minted, unredeemed credential as stored.
type BootstrapToken struct {
	SecretHash string
	QubeID     string
	QubeName   string
	CreatedAt  time.Time
	NotAfter   time.Time
	RedeemedAt *time.Time
}

// BootstrapTokenRepository stores one-shot bootstrap credentials.
type BootstrapTokenRepository struct {
	db *database.DB
}

// NewBootstrapTokenRepository builds the repository.
func NewBootstrapTokenRepository(db *database.DB) *BootstrapTokenRepository {
	return &BootstrapTokenRepository{db: db}
}

// Issue mints a token for a qube and stores its digest, returning the secret.
//
// The secret is returned exactly once and is not recoverable from the row. A
// caller that loses it must issue another; that is the property being bought.
func (r *BootstrapTokenRepository) Issue(
	ctx context.Context, qubeID, qubeName string, ttl time.Duration,
) (string, error) {
	if qubeID == "" {
		return "", errors.New("bootstrap token needs a qube id")
	}
	secret, rec, err := pki.NewBootstrapToken(qubeName, ttl)
	if err != nil {
		return "", err
	}

	const q = `
		INSERT INTO bootstrap_tokens (secret_hash, qube_id, qube_name, created_at, not_after)
		VALUES (?, ?, ?, ?, ?)`
	if _, err := r.db.DB().ExecContext(ctx, q,
		rec.SecretHash, qubeID, rec.QubeName, time.Now().UTC(), rec.NotAfter.UTC()); err != nil {
		return "", fmt.Errorf("store bootstrap token for %q: %w", qubeName, err)
	}
	return secret, nil
}

// Redeem consumes a token and returns the qube it authorizes.
//
// Single-use is the entire security value of a bootstrap token, and it is
// enforced by ONE statement. A Verify-then-Redeem pair in Go would be a
// check-then-act race: the database runs in WAL mode behind a real connection
// pool, so two agents presenting the same leaked token could both pass the check
// before either wrote, and both would get a certificate for the same name. The
// UPDATE below only matches an unredeemed, unexpired row, so exactly one caller
// can ever see RowsAffected == 1 — the database decides the winner, not a
// sequence of statements hoping isolation covers the gap.
//
// The expiry check lives in the same statement for the same reason: a token that
// expires between a separate SELECT and the UPDATE would otherwise be honored.
func (r *BootstrapTokenRepository) Redeem(
	ctx context.Context, secret string, now time.Time,
) (*BootstrapToken, error) {
	if secret == "" {
		return nil, ErrBootstrapTokenRejected
	}
	hash := pki.HashBootstrapSecret(secret)
	utc := now.UTC()

	const q = `
		UPDATE bootstrap_tokens
		   SET redeemed_at = ?
		 WHERE secret_hash = ?
		   AND redeemed_at IS NULL
		   AND not_after > ?
	 RETURNING qube_id, qube_name, created_at, not_after`

	var tok BootstrapToken
	err := r.db.DB().QueryRowContext(ctx, q, utc, hash, utc).
		Scan(&tok.QubeID, &tok.QubeName, &tok.CreatedAt, &tok.NotAfter)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Nothing was redeemed. Say why in the log, never in the response.
		return nil, r.explain(ctx, hash, utc)
	case err != nil:
		return nil, fmt.Errorf("redeem bootstrap token: %w", err)
	}
	tok.SecretHash = hash
	tok.RedeemedAt = &utc
	return &tok, nil
}

// explain turns a failed redemption into a specific operator-facing reason while
// returning the same opaque error to the caller.
//
// This runs only on the failure path, so the extra read costs nothing in the
// case that matters, and a boot that fails at 3am leaves behind "the token
// expired 20 minutes ago" rather than a flat refusal.
func (r *BootstrapTokenRepository) explain(ctx context.Context, hash string, now time.Time) error {
	const q = `
		SELECT qube_name, not_after, redeemed_at
		  FROM bootstrap_tokens WHERE secret_hash = ?`
	var (
		name     string
		notAfter time.Time
		redeemed sql.NullTime
	)
	err := r.db.DB().QueryRowContext(ctx, q, hash).Scan(&name, &notAfter, &redeemed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: no token matches the presented secret", ErrBootstrapTokenRejected)
	case err != nil:
		return fmt.Errorf("%w: %v", ErrBootstrapTokenRejected, err)
	case redeemed.Valid:
		return fmt.Errorf("%w: the token for %q was already redeemed at %s",
			ErrBootstrapTokenRejected, name, redeemed.Time.UTC().Format(time.RFC3339))
	case !notAfter.After(now):
		return fmt.Errorf("%w: the token for %q expired at %s",
			ErrBootstrapTokenRejected, name, notAfter.UTC().Format(time.RFC3339))
	default:
		// The atomic UPDATE matched nothing but the row looks usable, which
		// means another caller won the race between the two statements. That is
		// the mechanism working, not a fault.
		return fmt.Errorf("%w: the token for %q was redeemed concurrently",
			ErrBootstrapTokenRejected, name)
	}
}

// InvalidateForQube spends every outstanding token belonging to a qube.
//
// Called when a new one is minted, so a qube that is re-provisioned cannot be
// claimed with a token left over from an earlier attempt. Marking them redeemed
// rather than deleting keeps the audit trail: a row that exists and is spent
// says a token was issued, which a missing row does not.
func (r *BootstrapTokenRepository) InvalidateForQube(ctx context.Context, qubeID string, now time.Time) (int64, error) {
	const q = `
		UPDATE bootstrap_tokens SET redeemed_at = ?
		 WHERE qube_id = ? AND redeemed_at IS NULL`
	res, err := r.db.DB().ExecContext(ctx, q, now.UTC(), qubeID)
	if err != nil {
		return 0, fmt.Errorf("invalidate bootstrap tokens for %q: %w", qubeID, err)
	}
	return res.RowsAffected()
}

// DeleteSpent removes tokens that are redeemed or expired and older than before.
//
// Retention exists so the table does not grow without bound; the tokens it
// removes can no longer authorize anything, so nothing is lost but history.
func (r *BootstrapTokenRepository) DeleteSpent(ctx context.Context, before time.Time) (int64, error) {
	const q = `
		DELETE FROM bootstrap_tokens
		 WHERE created_at < ?
		   AND (redeemed_at IS NOT NULL OR not_after <= ?)`
	res, err := r.db.DB().ExecContext(ctx, q, before.UTC(), time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("delete spent bootstrap tokens: %w", err)
	}
	return res.RowsAffected()
}

// ListByQube returns a qube's tokens, newest first. For operator visibility —
// the digest is included, the secret is not recoverable.
func (r *BootstrapTokenRepository) ListByQube(ctx context.Context, qubeID string) ([]*BootstrapToken, error) {
	const q = `
		SELECT secret_hash, qube_id, qube_name, created_at, not_after, redeemed_at
		  FROM bootstrap_tokens WHERE qube_id = ? ORDER BY created_at DESC`
	rows, err := r.db.DB().QueryContext(ctx, q, qubeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*BootstrapToken
	for rows.Next() {
		var (
			t        BootstrapToken
			redeemed sql.NullTime
		)
		if err := rows.Scan(&t.SecretHash, &t.QubeID, &t.QubeName,
			&t.CreatedAt, &t.NotAfter, &redeemed); err != nil {
			return nil, err
		}
		if redeemed.Valid {
			at := redeemed.Time.UTC()
			t.RedeemedAt = &at
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}
