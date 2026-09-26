package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/database"
)

// Persisted audit trail row caps (docs/runtime-defaults.md UD-24b). The caps
// are judgment values; the sizes are measured: a typical row takes about 265
// bytes on disk with its indexes, and the largest a request can make (an
// object of 128 invalid bytes, stored as 384 bytes of U+FFFD, from the longest
// IPv6 source) about 683. The full class therefore tops out near 53 MB, 137 MB
// at worst, and the sampled class under 14 MB. 200,000 full-class rows hold 90
// days (UD-24) of about 2,200 mutating requests a day; 20,000 sampled rows hold
// months of ordinary failed logins, or a little over two days of the sampled
// budget running flat out.
const (
	DefaultAuditFullRows    = 200_000
	DefaultAuditSampledRows = 20_000
)

// auditPruneBatch bounds one retention DELETE, so pruning a large backlog is a
// series of short write transactions rather than one that holds SQLite's
// single writer lock for as long as it takes.
const auditPruneBatch = 1000

// AuditCaps bounds the rows each persist class may hold.
type AuditCaps struct {
	Full    int
	Sampled int
}

// DefaultAuditCaps returns the production caps.
func DefaultAuditCaps() AuditCaps {
	return AuditCaps{Full: DefaultAuditFullRows, Sampled: DefaultAuditSampledRows}
}

func (c AuditCaps) of(class audit.Class) int {
	if class == audit.ClassFull {
		return c.Full
	}
	return c.Sampled
}

// AuditRepository stores the persisted audit trail (audit_events) and keeps it
// bounded.
//
// Each persist class has its own hard row cap, enforced in the same
// transaction as the insert that would exceed it by evicting that class's
// oldest rows. The classes never evict each other: an unauthenticated flood,
// or a credential flooding past its rate limit (every 429 is sampled, see
// audit.Event.Class), can at most churn the sampled class, which
// audit.Persister's budget also rate-limits, and can never push out a
// full-class event. What a credential holder can still add to the full class
// is what the API rate limit lets through (UD-1: 20 requests a second and a
// burst of 40 per subject), each row naming the credential; at that pace one
// token cycles the 200,000-row class in under three hours.
//
// Row counts are kept in memory so an insert does not count the table. They
// are loaded on first use and again after a prune, and an eviction recounts
// its class; only this repository writes the table, so they stay exact.
type AuditRepository struct {
	db   *database.DB
	caps AuditCaps

	mu      sync.Mutex
	counts  map[audit.Class]int
	counted bool
}

// NewAuditRepository builds the repository. A non-positive cap takes the
// default.
func NewAuditRepository(db *database.DB, caps AuditCaps) *AuditRepository {
	if caps.Full <= 0 {
		caps.Full = DefaultAuditFullRows
	}
	if caps.Sampled <= 0 {
		caps.Sampled = DefaultAuditSampledRows
	}
	return &AuditRepository{db: db, caps: caps}
}

const insertAuditEvent = `
INSERT INTO audit_events (occurred_at, request_id, authenticated, auth_disabled, subject, source,
	method, route, object, object_truncated, status, outcome, latency_ms, zone_scope,
	persist_class, suppressed, suppressed_since)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// AppendEvent stores one event exactly as it was logged, in its persist class.
func (r *AuditRepository) AppendEvent(ctx context.Context, ev audit.Event) error {
	class := ev.Class()
	return r.append(ctx, class,
		ev.Time.UnixNano(), ev.RequestID, ev.Authenticated, ev.AuthDisabled, ev.Subject, ev.Source,
		ev.Method, ev.Route, ev.Object, ev.ObjectTruncated, ev.Status, ev.Outcome, ev.LatencyMS, ev.ZoneScope,
		string(class), 0, 0)
}

// AppendSuppression stores a summary row for sampled events the budget kept
// out: outcome "suppressed", suppressed = the count, suppressed_since and
// occurred_at = the first and last of their times. It is not a request, so the
// request fields are empty; it belongs to, and is capped with, the sampled
// class.
func (r *AuditRepository) AppendSuppression(ctx context.Context, s audit.Suppression) error {
	return r.append(ctx, audit.ClassSampled,
		s.Last.UnixNano(), "", false, false, audit.AnonymousSubject, "",
		"", "", "", false, 0, audit.OutcomeSuppressed, 0, "none",
		string(audit.ClassSampled), s.Count, s.First.UnixNano())
}

// append inserts one row and, if that takes its class over the cap, evicts
// the class's oldest rows in the same transaction. It trims to 1% below the
// cap, so eviction runs once per hundredth of the cap rather than on every
// insert.
func (r *AuditRepository) append(ctx context.Context, class audit.Class, row ...any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.loadCounts(ctx); err != nil {
		return err
	}

	tx, err := r.db.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("audit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, insertAuditEvent, row...); err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	n := r.counts[class] + 1
	if limit := r.caps.of(class); n > limit {
		if n, err = trimAuditClass(ctx, tx, class, limit-limit/100); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("audit: commit: %w", err)
	}
	r.counts[class] = n
	return nil
}

// trimAuditClass deletes the oldest rows of class beyond keep and returns how
// many rows of the class remain.
func trimAuditClass(ctx context.Context, tx *sql.Tx, class audit.Class, keep int) (int, error) {
	var have int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE persist_class = ?`, string(class)).Scan(&have); err != nil {
		return 0, fmt.Errorf("audit: count %s rows: %w", class, err)
	}
	excess := have - keep
	if excess <= 0 {
		return have, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM audit_events WHERE id IN (
		SELECT id FROM audit_events WHERE persist_class = ? ORDER BY id LIMIT ?)`, string(class), excess); err != nil {
		return 0, fmt.Errorf("audit: evict %s rows: %w", class, err)
	}
	return keep, nil
}

// loadCounts reads the per-class row counts unless they are already known.
func (r *AuditRepository) loadCounts(ctx context.Context) error {
	if r.counted {
		return nil
	}
	rows, err := r.db.DB().QueryContext(ctx,
		`SELECT persist_class, COUNT(*) FROM audit_events GROUP BY persist_class`)
	if err != nil {
		return fmt.Errorf("audit: count rows: %w", err)
	}
	defer rows.Close()
	counts := map[audit.Class]int{}
	for rows.Next() {
		var class string
		var n int
		if err := rows.Scan(&class, &n); err != nil {
			return fmt.Errorf("audit: count rows: %w", err)
		}
		counts[audit.Class(class)] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("audit: count rows: %w", err)
	}
	r.counts, r.counted = counts, true
	return nil
}

// PruneBefore deletes every row that occurred strictly before cutoff (a row
// exactly at cutoff stays) and returns how many it deleted. It works in
// batches of auditPruneBatch rows, each its own short transaction, and stops
// at the first error, including ctx's.
func (r *AuditRepository) PruneBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := r.pruneBatch(ctx, cutoff.UnixNano())
		total += n
		if err != nil {
			return total, err
		}
		if n < auditPruneBatch {
			return total, nil
		}
	}
}

func (r *AuditRepository) pruneBatch(ctx context.Context, cutoff int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, err := r.db.DB().ExecContext(ctx, `DELETE FROM audit_events WHERE id IN (
		SELECT id FROM audit_events WHERE occurred_at < ? ORDER BY occurred_at LIMIT ?)`, cutoff, auditPruneBatch)
	if err != nil {
		return 0, fmt.Errorf("audit: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("audit: prune: %w", err)
	}
	if n > 0 {
		r.counted = false
	}
	return n, nil
}
