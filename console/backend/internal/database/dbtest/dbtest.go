// Package dbtest holds database fixtures that tests in several packages share,
// so an upgrade test in database, repository or service loads exactly the same
// frozen database and checks exactly the same rows.
//
// It is imported only by tests. It must not import the database package: the
// database package's own tests import it.
package dbtest

import (
	"context"
	"database/sql"
	_ "embed" // the frozen v2 schema is embedded
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3" // the fixture is written with the same driver the console uses
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaV2 is the DDL a console built from 2d409fd created, plus the rows a
// running v2 fleet holds. It is frozen: regenerating it from a newer build
// would turn every upgrade test into a no-op.
//
//go:embed schema_v2.sql
var schemaV2 string

// V2FixtureVersion is the user_version the fixture represents. A literal, not
// the current SchemaVersion, for the reason schemaV2 is frozen.
const V2FixtureVersion = 2

// WriteV2Fixture materializes the frozen v2 database into a fresh file under
// t.TempDir(), stamps user_version = 2 and returns its path. No migration code
// runs on it, so the file is exactly what a v2 console left on disk; opening it
// with database.New is the upgrade under test.
func WriteV2Fixture(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v2.db")
	raw, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer raw.Close()

	ctx := context.Background()
	_, err = raw.ExecContext(ctx, schemaV2)
	require.NoError(t, err, "the v2 fixture must load into a blank database")
	_, err = raw.ExecContext(ctx, "PRAGMA user_version = 2")
	require.NoError(t, err)
	return path
}

func countRows(t testing.TB, db *sql.DB, query string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(), query).Scan(&n))
	return n
}

// AssertV2RowsPreserved checks that every row the fixture holds survived an
// upgrade with the values a v2 console wrote. Each schema step's upgrade test
// calls it after its own assertions, so a migration that rewrites or drops
// fleet state fails in a test rather than on a real console.
func AssertV2RowsPreserved(t testing.TB, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	assert.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM zones WHERE id = 'zone-pve-lab' AND status = 'disconnected'`))

	var health, lastErr string
	var failingSince sql.NullString
	require.NoError(t, db.QueryRowContext(ctx,
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
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'notifications'`).Scan(&notifications))
	assert.Contains(t, notifications, `"webhookUrl":"https://hooks.example.invalid/qubes-air"`)
}
