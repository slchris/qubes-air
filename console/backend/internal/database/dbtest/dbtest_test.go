package dbtest_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/database/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture as written is a v2 database whose rows AssertV2RowsPreserved
// accepts before any migration has touched it.
func TestWriteV2FixtureIsAV2DatabaseWithItsRows(t *testing.T) {
	path := dbtest.WriteV2Fixture(t)
	raw, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer raw.Close()

	var v int
	require.NoError(t, raw.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v))
	assert.Equal(t, dbtest.V2FixtureVersion, v)
	dbtest.AssertV2RowsPreserved(t, raw)
}

// Two calls give two independent files, so tests can mutate one freely.
func TestWriteV2FixtureGivesEachCallItsOwnFile(t *testing.T) {
	first, second := dbtest.WriteV2Fixture(t), dbtest.WriteV2Fixture(t)
	assert.NotEqual(t, first, second)

	cfg := database.DefaultConfig()
	cfg.DSN = first
	db, err := database.New(cfg)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.DB().ExecContext(context.Background(), `DELETE FROM zones`)
	require.NoError(t, err)

	raw, err := sql.Open("sqlite3", second)
	require.NoError(t, err)
	defer raw.Close()
	dbtest.AssertV2RowsPreserved(t, raw)
}
