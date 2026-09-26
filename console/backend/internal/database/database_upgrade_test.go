package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// v2FixtureVersion is the user_version testdata/schema_v2.sql represents. It is
// deliberately a literal, not SchemaVersion: the fixture is frozen at the schema
// a deployed v2 console wrote, and every later schema step is tested against it.
const v2FixtureVersion = 2

// writeV2Fixture materializes testdata/schema_v2.sql into a fresh database file
// stamped user_version = 2 and returns its path. Nothing in this package's
// migration code runs on it, so the file is exactly what a v2 console left on
// disk.
func writeV2Fixture(t *testing.T) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("testdata", "schema_v2.sql"))
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "v2.db")
	raw, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer raw.Close()

	ctx := context.Background()
	_, err = raw.ExecContext(ctx, string(script))
	require.NoError(t, err, "the v2 fixture must load into a blank database")
	_, err = raw.ExecContext(ctx, "PRAGMA user_version = 2")
	require.NoError(t, err)
	return path
}

// openV2Fixture writes the v2 fixture and opens it with New, which is the
// upgrade: New runs every migration step between v2 and SchemaVersion.
func openV2Fixture(t *testing.T) (*DB, string) {
	t.Helper()
	path := writeV2Fixture(t)
	return openPath(t, path), path
}

// openPath opens (and therefore migrates) the database file at path and
// closes it when the test ends.
func openPath(t *testing.T, path string) *DB {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DSN = path
	db, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// schemaObjects returns every schema object's stored DDL, keyed by type and
// name. Comparing two snapshots is how a test proves a re-open changed nothing.
func schemaObjects(t *testing.T, db *DB) map[string]string {
	t.Helper()
	rows, err := db.DB().QueryContext(context.Background(),
		`SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var typ, name, ddl string
		require.NoError(t, rows.Scan(&typ, &name, &ddl))
		out[typ+" "+name] = ddl
	}
	require.NoError(t, rows.Err())
	return out
}

func countRows(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.DB().QueryRowContext(context.Background(), query, args...).Scan(&n))
	return n
}

// assertV2RowsPreserved checks that every row the fixture holds survived the
// upgrade with the values a v2 console wrote. Later schema steps call it after
// their own assertions, so an upgrade that rewrites or drops fleet state fails
// here rather than on a real console.
func assertV2RowsPreserved(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()

	assert.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM zones WHERE id = 'zone-pve-lab' AND status = 'disconnected'`))

	var health, lastErr string
	var failingSince sql.NullString
	require.NoError(t, db.DB().QueryRowContext(ctx,
		`SELECT agent_health, agent_last_error, agent_failing_since FROM qubes WHERE id = 'qube-pending'`).
		Scan(&health, &lastErr, &failingSince))
	assert.Equal(t, "unreachable", health)
	assert.Contains(t, lastErr, "connection refused")
	assert.True(t, failingSince.Valid, "a failure streak recorded before the upgrade must survive it")
	assert.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM qubes WHERE id = 'qube-healthy' AND agent_health = 'healthy'`))

	assert.Equal(t, 1, countRows(t, db,
		`SELECT COUNT(*) FROM bootstrap_tokens WHERE qube_id = 'qube-healthy' AND redeemed_at IS NOT NULL`))
	assert.Equal(t, 1, countRows(t, db,
		`SELECT COUNT(*) FROM bootstrap_tokens WHERE qube_id = 'qube-pending' AND redeemed_at IS NULL`))
	assert.Equal(t, 1, countRows(t, db,
		`SELECT COUNT(*) FROM agent_certs WHERE qube_id = 'qube-healthy' AND revoked_at IS NULL`))

	var notifications string
	require.NoError(t, db.DB().QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'notifications'`).Scan(&notifications))
	assert.Contains(t, notifications, `"webhookUrl":"https://hooks.example.invalid/qubes-air"`)
}

// Opening a database a v2 console wrote must bring it to this build's schema
// without losing or rewriting what the fleet already holds, and opening it a
// second time must change nothing: every console restart re-runs migrate().
func TestOpeningAV2DatabaseKeepsItsRowsAndIsIdempotent(t *testing.T) {
	db, path := openV2Fixture(t)

	got, err := db.UserVersion()
	require.NoError(t, err)
	assert.Equal(t, SchemaVersion, got)
	assertV2RowsPreserved(t, db)

	before := schemaObjects(t, db)
	require.NoError(t, db.Close())

	again := openPath(t, path)
	got, err = again.UserVersion()
	require.NoError(t, err)
	assert.Equal(t, SchemaVersion, got)
	assert.Equal(t, before, schemaObjects(t, again), "a second open must not alter the schema")
	assertV2RowsPreserved(t, again)
}

// The fixture is only useful while it really is v2. This fails if someone
// "refreshes" it from a newer build, which would silently turn every upgrade
// test into a no-op.
func TestV2FixtureIsFrozenAtVersion2(t *testing.T) {
	path := writeV2Fixture(t)
	raw, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer raw.Close()

	var v int
	require.NoError(t, raw.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v))
	assert.Equal(t, v2FixtureVersion, v)

	var cols int
	require.NoError(t, raw.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('bootstrap_tokens')`).Scan(&cols))
	assert.Equal(t, 6, cols, "the v2 bootstrap_tokens table has exactly six columns")
}
