package database

import (
	"context"
	"fmt"
	"strings"
)

// createAuditEventsTable is schema step 4: the persisted Console API audit
// trail. Each row is one JSON audit line (internal/audit), column for field,
// so a trail read from the database and one read from the log cannot disagree:
//
//   - occurred_at is the line's "time" as Unix nanoseconds (UTC), an INTEGER
//     rather than a DATETIME string so ordering and the retention cutoff are
//     numeric comparisons, not comparisons of whatever text format a driver
//     happened to write;
//   - the boolean fields are 0/1 with a CHECK, so a stray value cannot read as
//     "maybe authenticated".
//
// The other columns are not on the line; they record how the row got here,
// because the persisted trail is bounded and a reader has to be able to tell
// what was left out (see repository.AuditRepository):
//
//   - persist_class is "full" for an event that is always stored (an
//     authenticated request, or one that succeeded, unless it was throttled)
//     and "sampled" for one that went through the budget (every 429, and every
//     request that proved no credential and did not succeed: the kinds a
//     caller can produce faster than anything else bounds);
//   - suppressed and suppressed_since are non-zero only on a summary row: the
//     number of sampled events the budget kept out, and when the first of them
//     happened (occurred_at is the last). Every such event still has its log
//     line;
//   - suppressed_sources and suppressed_top_sources say where those events
//     came from, so a flood does not erase who was probing: the number of
//     distinct source prefixes (IPv4 /24, IPv6 /64; exact up to 1024), and the
//     busiest few with their counts ("198.51.100.0/24 4211; others 37").
//
// There is no foreign key: the trail must outlive the qubes and zones it
// names, exactly like the jobs table.
const createAuditEventsTable = `
CREATE TABLE IF NOT EXISTS audit_events (
	id               INTEGER PRIMARY KEY,
	occurred_at      INTEGER NOT NULL,
	request_id       TEXT NOT NULL,
	authenticated    INTEGER NOT NULL CHECK (authenticated IN (0, 1)),
	auth_disabled    INTEGER NOT NULL CHECK (auth_disabled IN (0, 1)),
	subject          TEXT NOT NULL,
	source           TEXT NOT NULL,
	method           TEXT NOT NULL,
	route            TEXT NOT NULL,
	object           TEXT NOT NULL,
	object_truncated INTEGER NOT NULL CHECK (object_truncated IN (0, 1)),
	status           INTEGER NOT NULL,
	outcome          TEXT NOT NULL,
	latency_ms       INTEGER NOT NULL,
	zone_scope       TEXT NOT NULL,
	persist_class    TEXT NOT NULL CHECK (persist_class IN ('full', 'sampled')),
	suppressed       INTEGER NOT NULL DEFAULT 0 CHECK (suppressed >= 0),
	suppressed_since INTEGER NOT NULL DEFAULT 0,
	suppressed_sources     INTEGER NOT NULL DEFAULT 0 CHECK (suppressed_sources >= 0),
	suppressed_top_sources TEXT NOT NULL DEFAULT ''
)`

// createAuditEventsIndexes serves the three ways the trail is read or trimmed:
// newest first, one request by the ID its caller was given, and the oldest
// rows of one persist class (the per-class row cap evicts from there).
const createAuditEventsIndexes = `
CREATE INDEX IF NOT EXISTS idx_audit_events_occurred_at ON audit_events(occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_request_id ON audit_events(request_id);
CREATE INDEX IF NOT EXISTS idx_audit_events_class ON audit_events(persist_class, id)`

// auditEventsColumns is every column step 4 creates, in order.
var auditEventsColumns = []string{
	"id", "occurred_at", "request_id", "authenticated", "auth_disabled",
	"subject", "source", "method", "route", "object", "object_truncated",
	"status", "outcome", "latency_ms", "zone_scope",
	"persist_class", "suppressed", "suppressed_since",
	"suppressed_sources", "suppressed_top_sources",
}

// migrateAudit is schema step 4. The table is new, so the step is additive:
// no existing row is touched.
//
// CREATE TABLE IF NOT EXISTS leaves a table of the same name alone whatever its
// columns, so the columns are checked before anything relies on them. An
// unreleased build once created an audit_events table without request_id; a
// database it touched must be refused with that said, rather than failing on an
// index with "no such column" or, worse, being written in two formats.
//
// Columns after step 4's are allowed: a later schema step may only add
// columns at the end, and a database that has them is newer than this build,
// which applySchemaVersion then refuses with the message that says so.
func (d *DB) migrateAudit() error {
	ctx := context.Background()
	if _, err := d.db.ExecContext(ctx, createAuditEventsTable); err != nil {
		return fmt.Errorf("creating audit_events: %w", err)
	}
	if err := d.checkAuditEventsColumns(ctx); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, createAuditEventsIndexes); err != nil {
		return fmt.Errorf("indexing audit_events: %w", err)
	}
	return nil
}

// checkAuditEventsColumns fails unless audit_events starts with exactly the
// columns step 4 creates, in the same order.
func (d *DB) checkAuditEventsColumns(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('audit_events')`)
	if err != nil {
		return fmt.Errorf("inspecting audit_events: %w", err)
	}
	defer rows.Close()
	var have []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("inspecting audit_events: %w", err)
		}
		have = append(have, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspecting audit_events: %w", err)
	}
	if len(have) < len(auditEventsColumns) ||
		strings.Join(have[:len(auditEventsColumns)], ",") != strings.Join(auditEventsColumns, ",") {
		return fmt.Errorf("audit_events has columns (%s), not the ones this console writes (%s): "+
			"a build with a different audit schema opened this database; restore a backup taken before it",
			strings.Join(have, ", "), strings.Join(auditEventsColumns, ", "))
	}
	return nil
}
