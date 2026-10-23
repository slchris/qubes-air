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
