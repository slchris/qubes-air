package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/stretchr/testify/require"
)

func TestDatabasePreflightCLI(t *testing.T) {
	bin := buildConsoleBinary(t, "")
	for _, tc := range []struct {
		name    string
		version int
		wantErr string
	}{
		{name: "older", version: 2},
		{name: "current", version: database.SchemaVersion},
		{name: "future", version: database.SchemaVersion + 1, wantErr: "newer than this console supports"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "database with ? and #.db")
			db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "fixture.db"))
			require.NoError(t, err)
			_, err = db.Exec("PRAGMA user_version = " + strconv.Itoa(tc.version))
			require.NoError(t, err)
			require.NoError(t, db.Close())
			require.NoError(t, os.Rename(filepath.Join(dir, "fixture.db"), path))
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			cmd := exec.Command(bin, "--check-database", path, "--config", filepath.Join(dir, "missing.yaml"))
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if tc.wantErr == "" {
				require.NoError(t, err, "%s", out)
				require.Contains(t, string(out), "database_schema="+strconv.Itoa(tc.version))
				require.Contains(t, string(out), "supported_schema="+strconv.Itoa(database.SchemaVersion))
			} else {
				require.Error(t, err)
				require.Contains(t, string(out), tc.wantErr)
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after, "preflight must not migrate the database")
			require.NoFileExists(t, filepath.Join(dir, "qubes-air.db"))
		})
	}
	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.db")
		out, err := exec.Command(bin, "--check-database", path).CombinedOutput()
		require.Error(t, err)
		require.Contains(t, string(out), "database preflight")
		require.NoFileExists(t, path)
	})
	t.Run("empty argument", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command(bin, "--check-database=")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.Error(t, err)
		require.Contains(t, string(out), "database preflight: a database path is required")
		require.NoFileExists(t, filepath.Join(dir, "qubes-air.db"))
	})
}
