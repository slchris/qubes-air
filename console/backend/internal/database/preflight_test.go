package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewRejectsFutureSchemaBeforeChangingTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec("CREATE TABLE future_only (value TEXT); INSERT INTO future_only VALUES ('keep')")
	require.NoError(t, err)
	_, err = db.Exec("PRAGMA user_version = " + strconv.Itoa(SchemaVersion+1))
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.DSN = path
	opened, err := New(cfg)
	if opened != nil {
		t.Cleanup(func() { require.NoError(t, opened.Close()) })
	}
	require.ErrorContains(t, err, "newer than this console supports")
	require.Nil(t, opened)
	var tables int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables))
	require.Equal(t, 1, tables, "a rejected database must not receive this version's tables or migrations")
	var value string
	require.NoError(t, db.QueryRow("SELECT value FROM future_only").Scan(&value))
	require.Equal(t, "keep", value)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "rejection must not even change the journal mode")
}

func TestCheckFileFailures(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.db")
	require.NoError(t, os.WriteFile(corrupt, []byte("not a sqlite database"), 0600))
	for _, path := range []string{filepath.Join(dir, "missing.db"), dir, corrupt} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			_, err := CheckFile(context.Background(), path)
			require.Error(t, err)
		})
	}
	require.NoFileExists(t, filepath.Join(dir, "missing.db"))
}

func TestCheckFileReadsLiveWALAndHonorsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec("PRAGMA journal_mode = WAL; PRAGMA wal_autocheckpoint = 0; PRAGMA user_version = " + strconv.Itoa(SchemaVersion+1))
	require.NoError(t, err)
	before, err := os.ReadFile(path + "-wal")
	require.NoError(t, err)
	version, err := CheckFile(context.Background(), path)
	require.ErrorContains(t, err, "newer than this console supports")
	require.Equal(t, SchemaVersion+1, version)
	after, err := os.ReadFile(path + "-wal")
	require.NoError(t, err)
	require.Equal(t, before, after)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = CheckFile(ctx, path)
	require.ErrorIs(t, err, context.Canceled)
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = CheckFile(ctx, path)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestReadOnlyConnectionDoesNotMigrateWriteOrInterpretPathOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read?literal.db")
	// Use a literal URL through the regular test fixture's default path first;
	// OpenReadOnly must not interpret the question mark as a URI option.
	plain := filepath.Join(t.TempDir(), "db")
	db, err := sql.Open("sqlite3", plain)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE proof (value TEXT); INSERT INTO proof VALUES ('kept')")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, os.Rename(plain, path))
	ro, err := OpenReadOnly(context.Background(), path)
	require.NoError(t, err)
	defer ro.Close()
	var count int
	require.NoError(t, ro.DB().QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&count))
	require.Equal(t, 1, count, "opening for publication must not run migrations")
	_, err = ro.DB().Exec("DELETE FROM proof")
	require.ErrorContains(t, err, "readonly")
	var value string
	require.NoError(t, ro.DB().QueryRow("SELECT value FROM proof").Scan(&value))
	require.Equal(t, "kept", value)
}
