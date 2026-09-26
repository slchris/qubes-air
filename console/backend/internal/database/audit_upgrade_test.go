package database

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// v3PinnedTokenHash is the token a v3 console issued with a pin; writeV3State
// adds it so the v3 -> v4 upgrade has a v3-only value to carry over.
const v3PinnedTokenHash = "5555555555555555555555555555555555555555555555555555555555555555"

// writeV3State materializes the database a v3 console leaves on disk: the
// frozen v2 fixture, plus exactly what step 3 does to it (the pin column),
// plus one token issued with a pin, stamped user_version = 3. It is built from
// the v2 fixture rather than from this build so step 4 cannot leak into it.
func writeV3State(t *testing.T) string {
	t.Helper()
	path := writeV2Fixture(t)
	raw, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer raw.Close()

	ctx := context.Background()
	for _, stmt := range []string{
		`ALTER TABLE bootstrap_tokens ADD COLUMN placeholder_spki_sha256 TEXT NOT NULL DEFAULT ''`,
		`INSERT INTO bootstrap_tokens (secret_hash, qube_id, qube_name, created_at, not_after, placeholder_spki_sha256)
		 VALUES ('` + v3PinnedTokenHash + `', 'qube-healthy', 'remote-healthy',
		         '2026-09-21 09:00:00+00:00', '2026-09-21 10:00:00+00:00', '` + strings.Repeat("ab", 32) + `')`,
		`PRAGMA user_version = 3`,
	} {
		_, err := raw.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	return path
}

// assertAuditTrailSchema checks step 4's objects on db: the table has the same
// columns as one a fresh database gets, it starts empty, and its three indexes
// exist.
func assertAuditTrailSchema(t *testing.T, db *DB) {
	t.Helper()
	got, err := db.UserVersion()
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, got)

	fresh := openPath(t, filepath.Join(t.TempDir(), "fresh.db"))
	assert.Equal(t, tableColumns(t, fresh, "audit_events"), tableColumns(t, db, "audit_events"),
		"an upgraded audit_events must be the table a fresh database gets")
	assert.Equal(t, 0, countRows(t, db, `SELECT COUNT(*) FROM audit_events`),
		"an upgrade must not invent audit history")
	for _, index := range []string{
		"idx_audit_events_occurred_at", "idx_audit_events_request_id", "idx_audit_events_class",
	} {
		assert.Equal(t, 1, countRows(t, db,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ? AND tbl_name = 'audit_events'`, index),
			"index %s", index)
	}
}

// assertReopenIsNoOp closes db and opens path again: the schema must not
// change and the version must stay current.
func assertReopenIsNoOp(t *testing.T, db *DB, path string) *DB {
	t.Helper()
	before := schemaObjects(t, db)
	require.NoError(t, db.Close())
	again := openPath(t, path)
	got, err := again.UserVersion()
	require.NoError(t, err)
	assert.Equal(t, SchemaVersion, got)
	assert.Equal(t, before, schemaObjects(t, again), "re-opening an upgraded database must not alter it")
	return again
}

// Schema 4 from a v2 console: the audit trail appears empty, every v2 row
// survives, and a second open changes nothing.
func TestUpgradeFromV2AddsAuditTrail(t *testing.T) {
	require.GreaterOrEqual(t, SchemaVersion, 4)
	db, path := openV2Fixture(t)

	assertAuditTrailSchema(t, db)
	assertV2RowsPreserved(t, db)

	again := assertReopenIsNoOp(t, db, path)
	assertV2RowsPreserved(t, again)
}

// Schema 4 from a v3 console: the same, and the v3-only state (the pin column
// and a pinned token) comes through untouched.
func TestUpgradeFromV3AddsAuditTrail(t *testing.T) {
	require.GreaterOrEqual(t, SchemaVersion, 4)
	path := writeV3State(t)
	db := openPath(t, path)

	assertAuditTrailSchema(t, db)
	assertV2RowsPreserved(t, db)
	var pin string
	require.NoError(t, db.DB().QueryRowContext(context.Background(),
		`SELECT placeholder_spki_sha256 FROM bootstrap_tokens WHERE secret_hash = ?`, v3PinnedTokenHash).Scan(&pin))
	assert.Equal(t, strings.Repeat("ab", 32), pin, "a pin a v3 console stored must survive step 4")

	assertReopenIsNoOp(t, db, path)
}

// A database that already has an audit_events table this console did not
// create (an unreleased build's, without request_id) is refused by name, and
// is left as it was: the version is not stamped, so nothing reads it as v4.
func TestRefusesAuditEventsFromAnotherBuild(t *testing.T) {
	path := writeV3State(t)
	raw, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = raw.ExecContext(context.Background(), `CREATE TABLE audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT, occurred_at INTEGER NOT NULL, subject TEXT NOT NULL,
		source TEXT NOT NULL, method TEXT NOT NULL, route TEXT NOT NULL, object TEXT NOT NULL,
		status INTEGER NOT NULL, outcome TEXT NOT NULL, latency_ms INTEGER NOT NULL, zone_scope TEXT NOT NULL)`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	cfg := DefaultConfig()
	cfg.DSN = path
	db, err := New(cfg)
	if db != nil {
		_ = db.Close()
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit_events has columns")
	assert.Contains(t, err.Error(), "request_id")

	raw, err = sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer raw.Close()
	var v int
	require.NoError(t, raw.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v))
	assert.Equal(t, 3, v, "a refused database must keep the version it had")
}

// A database from a later schema that appended a column to audit_events is
// refused as newer than this build, with the message that says so, not
// mistaken for another build's audit table.
func TestNewerSchemaWithAWiderAuditTableIsRefusedAsNewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")
	db := openPath(t, path)
	ctx := context.Background()
	_, err := db.DB().ExecContext(ctx, `ALTER TABLE audit_events ADD COLUMN added_later TEXT NOT NULL DEFAULT ''`)
	require.NoError(t, err)
	// #nosec G202 -- a constant expression.
	_, err = db.DB().ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion+1))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	cfg := DefaultConfig()
	cfg.DSN = path
	again, err := New(cfg)
	if again != nil {
		_ = again.Close()
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newer than this console supports")
}

// The table's CHECK constraints are part of the contract: a boolean column
// cannot hold a third value and a row must name its persist class.
func TestAuditEventsRejectsOutOfRangeValues(t *testing.T) {
	db := openPath(t, filepath.Join(t.TempDir(), "checks.db"))
	insert := `INSERT INTO audit_events (occurred_at, request_id, authenticated, auth_disabled, subject, source,
		method, route, object, object_truncated, status, outcome, latency_ms, zone_scope, persist_class, suppressed)
		VALUES (1, 'r', ?, 0, 's', '192.0.2.1', 'POST', '/r', '', 0, 200, 'success', 0, 'fleet', ?, ?)`
	ctx := context.Background()

	_, err := db.DB().ExecContext(ctx, insert, 1, "full", 0)
	require.NoError(t, err, "a well-formed row is accepted")
	for name, args := range map[string][]any{
		"authenticated is not a boolean": {2, "full", 0},
		"unknown persist class":          {1, "some", 0},
		"missing persist class":          {1, nil, 0},
		"negative suppressed count":      {0, "sampled", -1},
	} {
		_, err := db.DB().ExecContext(ctx, insert, args...)
		assert.Error(t, err, name)
	}
	for _, method := range []string{"token", "", "Bearer"} {
		_, err := db.DB().ExecContext(ctx, `INSERT INTO audit_events (occurred_at, request_id, authenticated, auth_disabled,
			subject, source, method, route, object, object_truncated, status, outcome, latency_ms, zone_scope, persist_class,
			auth_method) VALUES (1, 'r', 1, 0, 's', '192.0.2.1', 'POST', '/r', '', 0, 200, 'success', 0, 'fleet', 'full', ?)`, method)
		assert.Error(t, err, "auth_method %q", method)
	}
}
