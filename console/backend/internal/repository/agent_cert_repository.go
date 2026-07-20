package repository

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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
	// ErrRenewalIdentityMismatch means a renewal tried to register a certificate
	// belonging to a different qube or agent than the one it renews. Distinct
	// from the others because it means the renewal machinery is confused about
	// which agent it is talking to, not that a credential went bad.
	ErrRenewalIdentityMismatch = errors.New("renewed certificate does not match the one it replaces")
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

// insertAgentCert is shared by Register and RecordRenewal, so a renewed
// certificate lands in the registry with exactly the columns a freshly issued
// one does. A renewal that produced a row the verifier reads differently would
// be a very confusing way to lose a fleet.
const insertAgentCert = `
	INSERT INTO agent_certs (fingerprint, qube_id, subject_cn, issued_at, expires_at)
	VALUES (?, ?, ?, ?, ?)`

// agentCertColumns is the read projection, in the order scanAgentCertRows and
// GetByFingerprint expect.
const agentCertColumns = `fingerprint, qube_id, subject_cn, issued_at, expires_at,
	       revoked_at, revoked_reason, last_seen_at`

// Register records a newly issued certificate as permitted to connect.
func (r *AgentCertRepository) Register(ctx context.Context, c *AgentCert) error {
	_, err := r.db.DB().ExecContext(ctx, insertAgentCert,
		c.Fingerprint, c.QubeID, c.SubjectCN, c.IssuedAt.UTC(), utcOrNil(c.ExpiresAt))
	return err
}

// utcOrNil normalizes a nullable timestamp on its way into the database.
//
// The driver stores a time.Time as a formatted string carrying whatever zone
// offset it had, and SQLite compares those strings lexically. Two identical
// instants written in different zones therefore compare as different, and
// ORDER BY expires_at interleaves them wrongly — which for
// ListRenewalCandidates means picking the wrong certificate as a qube's best
// one, and either renewing a qube that does not need it or skipping one that
// does. Normalizing on write makes the stored ordering agree with real time
// regardless of what the caller handed us.
func utcOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
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
	const q = `SELECT ` + agentCertColumns + ` FROM agent_certs WHERE fingerprint = ?`

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
//
// More than one live certificate per qube is NORMAL, not a fault. Renewal
// deliberately leaves the outgoing certificate to expire on its own — see
// RecordRenewal — so a qube inside a renewal window legitimately holds two.
func (r *AgentCertRepository) ListByQube(ctx context.Context, qubeID string) ([]*AgentCert, error) {
	const q = `SELECT ` + agentCertColumns + `
		FROM agent_certs WHERE qube_id = ? ORDER BY issued_at DESC`
	rows, err := r.db.DB().QueryContext(ctx, q, qubeID)
	if err != nil {
		return nil, err
	}
	return scanAgentCertRows(rows)
}

// ListRenewalCandidates returns, for each qube, the one certificate that decides
// whether it needs renewing — its longest-lived unrevoked one — but only when
// that certificate expires at or before cutoff. Soonest expiry first, so a sweep
// that runs out of time has done the most urgent work.
//
// Keying on the LONGEST-LIVED certificate per qube, rather than on every row
// approaching expiry, is what stops renewal running away with itself. Renewal
// does not revoke the certificate it replaces (revoking would kill the agent's
// in-flight connections and leave it briefly holding nothing valid), so during a
// renewal window a qube has an old certificate near expiry AND a fresh one. A
// query returning every expiring row would keep seeing that old one and renew
// again on every sweep, minting a certificate per sweep forever. Asking instead
// "what is the best credential this qube holds, and is THAT running out" makes a
// qube stop being a candidate the moment its renewal succeeds.
//
// Certificates that have ALREADY expired are included on purpose. Renewal will
// probably fail for them, since the agent can no longer authenticate to be
// dialed — but they are precisely the qubes that went dark, and a query that
// dropped them would hide the failure this mechanism exists to surface.
func (r *AgentCertRepository) ListRenewalCandidates(ctx context.Context, cutoff time.Time) ([]*AgentCert, error) {
	// The inner query picks one fingerprint rather than comparing against
	// MAX(expires_at): two certificates for a qube sharing an expiry to the
	// stored precision would otherwise both come back, and one qube would be
	// renewed twice in a single sweep.
	const q = `SELECT ` + agentCertColumns + `
		FROM agent_certs c
		WHERE c.revoked_at IS NULL
		  AND c.expires_at IS NOT NULL
		  AND c.expires_at <= ?
		  AND c.fingerprint = (
		        SELECT c2.fingerprint FROM agent_certs c2
		        WHERE c2.qube_id = c.qube_id
		          AND c2.revoked_at IS NULL
		          AND c2.expires_at IS NOT NULL
		        ORDER BY c2.expires_at DESC, c2.fingerprint DESC
		        LIMIT 1)
		ORDER BY c.expires_at ASC`
	rows, err := r.db.DB().QueryContext(ctx, q, cutoff.UTC())
	if err != nil {
		return nil, err
	}
	return scanAgentCertRows(rows)
}

// RecordRenewal registers a renewed certificate against the one it replaces.
//
// The previous certificate is deliberately NOT revoked here. Revoking it would
// tear down the connections the agent is holding open at this instant, and would
// leave it with no valid identity for the gap between this row landing and the
// agent installing what it was sent. It is left to expire on its own; the
// overlap is the safety margin.
//
// What this adds over Register is one check, in the same transaction as the
// insert, that the certificate being renewed is still live and still belongs to
// the same qube. Two failures it exists to prevent:
//
//   - A qube purged mid-renewal. RevokeByQube has already taken that qube's
//     access away; inserting a fresh unrevoked row afterwards would hand a
//     decommissioned machine a working credential straight back, and nothing
//     downstream would ever flag it, because a registered unrevoked certificate
//     is exactly what a legitimate agent looks like.
//   - A renewal recorded against the wrong qube, which would let a certificate
//     issued for one agent be authorized as another. The CA already refuses to
//     sign across identities; this is the same rule enforced where the
//     authorization decision actually reads from.
func (r *AgentCertRepository) RecordRenewal(ctx context.Context, previousFingerprint string, renewed *AgentCert) error {
	// The guard lives INSIDE the insert rather than in a preceding SELECT, so
	// there is no window between checking and writing. A read-then-write pair
	// would be racing RevokeByQube: the database runs in WAL mode with a real
	// connection pool, so a purge committing between the two statements would
	// leave this insert acting on a fact that stopped being true, and the purged
	// qube would get its credential back. Making it one statement removes the
	// window rather than relying on isolation to close it.
	const q = `
		INSERT INTO agent_certs (fingerprint, qube_id, subject_cn, issued_at, expires_at)
		SELECT ?, ?, ?, ?, ?
		WHERE EXISTS (
			SELECT 1 FROM agent_certs
			WHERE fingerprint = ? AND revoked_at IS NULL
			  AND qube_id = ? AND subject_cn = ?)`
	res, err := r.db.DB().ExecContext(ctx, q,
		renewed.Fingerprint, renewed.QubeID, renewed.SubjectCN,
		renewed.IssuedAt.UTC(), utcOrNil(renewed.ExpiresAt),
		previousFingerprint, renewed.QubeID, renewed.SubjectCN)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	// Nothing was written, so the guard failed. Re-read only to say WHY: a
	// scheduler that reports "renewal did not apply" without distinguishing a
	// purged qube from a confused caller sends whoever reads it looking in the
	// wrong place.
	prev, err := r.GetByFingerprint(ctx, previousFingerprint)
	if err != nil {
		return err
	}
	if prev.Revoked() {
		return fmt.Errorf("%w: certificate %s was revoked while it was being renewed",
			ErrCertRevoked, previousFingerprint)
	}
	return fmt.Errorf("%w: %s belongs to qube %s as %q, but the renewal names qube %s as %q",
		ErrRenewalIdentityMismatch, previousFingerprint, prev.QubeID, prev.SubjectCN,
		renewed.QubeID, renewed.SubjectCN)
}

// scanAgentCertRows drains a query using agentCertColumns.
//
// Shared so the three nullable columns are read the same way everywhere: a
// caller that skipped the Valid check would silently turn a NULL expiry into a
// zero time, which Authorize would read as "expired in year one" and reject
// every agent it touched.
func scanAgentCertRows(rows *sql.Rows) ([]*AgentCert, error) {
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
