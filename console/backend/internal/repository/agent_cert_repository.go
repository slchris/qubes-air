package repository

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
)

// Agent certificate errors.
var (
	// ErrCertNotRegistered means the presented certificate is unknown. A
	// certificate signed by the CA but absent from this registry is refused:
	// possession of a CA signature is not, by itself, permission to connect.
	ErrCertNotRegistered = errors.New("client certificate is not registered")
	// ErrCertRevoked means the certificate was explicitly revoked.
	ErrCertRevoked = errors.New("client certificate has been revoked")
	// ErrCertExpired means the registry's own expiry has passed.
	ErrCertExpired = errors.New("client certificate has expired")
)

// AgentCert is one issued client certificate.
type AgentCert struct {
	Fingerprint   string     `json:"fingerprint"`
	QubeID        string     `json:"qube_id"`
	SubjectCN     string     `json:"subject_cn"`
	IssuedAt      time.Time  `json:"issued_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	RevokedReason string     `json:"revoked_reason,omitempty"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
}

// Revoked reports whether this certificate has been revoked.
func (c *AgentCert) Revoked() bool { return c.RevokedAt != nil }

// AgentCertRepository is the registry of client certificates permitted to
// connect, and the mechanism by which one is revoked.
//
// This is what makes mTLS operationally sound here. A CA signature alone grants
// permanent access — there is no way to take it back without a revocation
// mechanism, and a CRL that is published but never fetched provides none. Since
// the verifier, the issuer and this database are the same process, revocation
// is a row update that the next handshake reads directly, with no distribution
// step that can silently fail.
type AgentCertRepository struct {
	db *database.DB
}

// NewAgentCertRepository creates an AgentCertRepository.
func NewAgentCertRepository(db *database.DB) *AgentCertRepository {
	return &AgentCertRepository{db: db}
}

// Fingerprint returns the registry key for a certificate: the SHA-256 of its
// DER encoding, which is what the TLS stack provides at verification time.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// Register records a newly issued certificate as permitted to connect.
func (r *AgentCertRepository) Register(ctx context.Context, c *AgentCert) error {
	const q = `
		INSERT INTO agent_certs (fingerprint, qube_id, subject_cn, issued_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`
	_, err := r.db.DB().ExecContext(ctx, q,
		c.Fingerprint, c.QubeID, c.SubjectCN, c.IssuedAt, c.ExpiresAt)
	return err
}

// Authorize reports whether a certificate may connect right now.
//
// Called on every TLS handshake. Unknown, revoked and expired are distinct
// errors on purpose: "not registered" and "revoked" mean very different things
// to whoever is reading the logs, and collapsing them would hide an attacker
// presenting a forged-but-CA-signed certificate among ordinary revocations.
func (r *AgentCertRepository) Authorize(ctx context.Context, fingerprint string) (*AgentCert, error) {
	cert, err := r.GetByFingerprint(ctx, fingerprint)
	if err != nil {
		return nil, err
	}
	if cert.Revoked() {
		return cert, ErrCertRevoked
	}
	if cert.ExpiresAt != nil && time.Now().After(*cert.ExpiresAt) {
		return cert, ErrCertExpired
	}
	return cert, nil
}

// GetByFingerprint loads one registered certificate.
func (r *AgentCertRepository) GetByFingerprint(ctx context.Context, fingerprint string) (*AgentCert, error) {
	const q = `
		SELECT fingerprint, qube_id, subject_cn, issued_at, expires_at,
		       revoked_at, revoked_reason, last_seen_at
		FROM agent_certs WHERE fingerprint = ?`

	var (
		c          AgentCert
		expiresAt  sql.NullTime
		revokedAt  sql.NullTime
		lastSeenAt sql.NullTime
	)
	err := r.db.DB().QueryRowContext(ctx, q, fingerprint).Scan(
		&c.Fingerprint, &c.QubeID, &c.SubjectCN, &c.IssuedAt, &expiresAt,
		&revokedAt, &c.RevokedReason, &lastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCertNotRegistered
	}
	if err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		t := expiresAt.Time
		c.ExpiresAt = &t
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		c.RevokedAt = &t
	}
	if lastSeenAt.Valid {
		t := lastSeenAt.Time
		c.LastSeenAt = &t
	}
	return &c, nil
}

// Revoke marks a certificate as no longer permitted.
//
// Takes effect on the next handshake. It does NOT tear down a connection that
// is already established — a long-lived tunnel would otherwise survive its own
// revocation indefinitely. The transport re-authorizes periodically for exactly
// that reason; see the stream loop.
func (r *AgentCertRepository) Revoke(ctx context.Context, fingerprint, reason string) error {
	const q = `
		UPDATE agent_certs SET revoked_at = ?, revoked_reason = ?
		WHERE fingerprint = ? AND revoked_at IS NULL`
	res, err := r.db.DB().ExecContext(ctx, q, time.Now().UTC(), reason, fingerprint)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either unknown or already revoked. Both are fine outcomes for a
		// revoke request, so this is not an error — but distinguish them for
		// the caller's logs.
		if _, err := r.GetByFingerprint(ctx, fingerprint); err != nil {
			return err
		}
	}
	return nil
}

// RevokeByQube revokes every certificate issued to a qube, returning the count.
// Used when a qube is purged: its agent must lose access with it.
func (r *AgentCertRepository) RevokeByQube(ctx context.Context, qubeID, reason string) (int64, error) {
	const q = `
		UPDATE agent_certs SET revoked_at = ?, revoked_reason = ?
		WHERE qube_id = ? AND revoked_at IS NULL`
	res, err := r.db.DB().ExecContext(ctx, q, time.Now().UTC(), reason, qubeID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TouchLastSeen records that a certificate was used.
//
// Cheap operational visibility: a certificate that has not been seen for a long
// time is a candidate for revocation, and one seen from an unexpected time or
// place is worth investigating. Failure here is not fatal to the connection.
func (r *AgentCertRepository) TouchLastSeen(ctx context.Context, fingerprint string) error {
	const q = `UPDATE agent_certs SET last_seen_at = ? WHERE fingerprint = ?`
	_, err := r.db.DB().ExecContext(ctx, q, time.Now().UTC(), fingerprint)
	return err
}

// ListByQube returns every certificate issued to a qube, newest first.
func (r *AgentCertRepository) ListByQube(ctx context.Context, qubeID string) ([]*AgentCert, error) {
	const q = `
		SELECT fingerprint, qube_id, subject_cn, issued_at, expires_at,
		       revoked_at, revoked_reason, last_seen_at
		FROM agent_certs WHERE qube_id = ? ORDER BY issued_at DESC`
	rows, err := r.db.DB().QueryContext(ctx, q, qubeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*AgentCert
	for rows.Next() {
		var (
			c          AgentCert
			expiresAt  sql.NullTime
			revokedAt  sql.NullTime
			lastSeenAt sql.NullTime
		)
		if err := rows.Scan(
			&c.Fingerprint, &c.QubeID, &c.SubjectCN, &c.IssuedAt, &expiresAt,
			&revokedAt, &c.RevokedReason, &lastSeenAt,
		); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			t := expiresAt.Time
			c.ExpiresAt = &t
		}
		if revokedAt.Valid {
			t := revokedAt.Time
			c.RevokedAt = &t
		}
		if lastSeenAt.Valid {
			t := lastSeenAt.Time
			c.LastSeenAt = &t
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}
