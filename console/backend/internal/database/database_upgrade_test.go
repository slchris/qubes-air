package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
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

// tableColumns returns a table's columns with their declared type and default,
// in declaration order.
func tableColumns(t *testing.T, db *DB, table string) []string {
	t.Helper()
	rows, err := db.DB().QueryContext(context.Background(),
		`SELECT name, type, "notnull", COALESCE(dflt_value, 'NULL') FROM pragma_table_info(?)`, table)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, typ, dflt string
		var notNull int
		require.NoError(t, rows.Scan(&name, &typ, &notNull, &dflt))
		out = append(out, name+" "+typ+" notnull="+strconv.Itoa(notNull)+" default="+dflt)
	}
	require.NoError(t, rows.Err())
	return out
}

// Schema 3: bootstrap tokens carry the public-key pin of the placeholder their
// token derives. Legacy rows must come out of the upgrade with the empty pin
// the repository refuses (they predate the token-derived placeholder), the
// purge guards on the table must still fire, and the upgrade must be a no-op
// the second time.
func TestUpgradeFromV2AddsBootstrapPin(t *testing.T) {
	db, path := openV2Fixture(t)
	ctx := context.Background()

	// Step 3 introduced the column; later steps keep it, so this test holds for
	// every SchemaVersion from 3 on without being edited.
	require.GreaterOrEqual(t, SchemaVersion, 3)
	got, err := db.UserVersion()
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, got)

	var pins []string
	rows, err := db.DB().QueryContext(ctx, `SELECT placeholder_spki_sha256 FROM bootstrap_tokens ORDER BY secret_hash`)
	require.NoError(t, err)
	for rows.Next() {
		var pin string
		require.NoError(t, rows.Scan(&pin))
		pins = append(pins, pin)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	assert.Equal(t, []string{"", ""}, pins, "legacy tokens have no pin and must not be given one")

	// An upgraded table and a freshly created one must be the same table.
	fresh := openPath(t, filepath.Join(t.TempDir(), "fresh.db"))
	assert.Equal(t, tableColumns(t, fresh, "bootstrap_tokens"), tableColumns(t, db, "bootstrap_tokens"))

	// The purge guards installed at v2 still cover the widened table: purging
	// withdraws the outstanding legacy token and refuses a new one.
	_, err = db.DB().ExecContext(ctx,
		`UPDATE qubes SET purge_requested = 1, updated_at = '2026-09-20 10:05:00+00:00' WHERE id = 'qube-pending'`)
	require.NoError(t, err)
	assert.Equal(t, 0, countRows(t, db,
		`SELECT COUNT(*) FROM bootstrap_tokens WHERE qube_id = 'qube-pending' AND redeemed_at IS NULL`))
	_, err = db.DB().ExecContext(ctx, `INSERT INTO bootstrap_tokens
		(secret_hash, qube_id, qube_name, created_at, not_after, placeholder_spki_sha256)
		VALUES ('4444', 'qube-pending', 'remote-pending', '2026-09-20 10:06:00+00:00', '2026-09-20 11:06:00+00:00', 'pin')`)
	require.ErrorContains(t, err, "purge requested")

	before := schemaObjects(t, db)
	require.NoError(t, db.Close())
	again := openPath(t, path)
	got, err = again.UserVersion()
	require.NoError(t, err)
	assert.Equal(t, SchemaVersion, got)
	assert.Equal(t, before, schemaObjects(t, again), "re-opening an upgraded database must not alter it")
}
