package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// CheckFile checks an existing database's schema without migrating it. It uses
// a read-only connection, including the live WAL, so deployments can reject an
// incompatible candidate before replacing the running binary. It does not
// certify data integrity or the success of subsequent migrations.
func CheckFile(ctx context.Context, path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("database preflight: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("database preflight: expected a regular database file")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, fmt.Errorf("database preflight: resolve path: %w", err)
	}
	// Treat the supplied path literally; '?' and '#' must never become SQLite
	// URI options. Do not use immutable=1: that would ignore the running WAL.
	uri := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&_query_only=1&_busy_timeout=1000"}
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return 0, fmt.Errorf("database preflight: open: %w", err)
	}
	defer db.Close()
	version, err := checkSchema(ctx, db)
	if err != nil {
		return version, fmt.Errorf("database preflight: %w", err)
	}
	return version, nil
}

func checkSchema(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if version > SchemaVersion {
		return version, fmt.Errorf(
			"database schema version %d is newer than this console supports (%d); upgrade the console before opening this database",
			version, SchemaVersion)
	}
	return version, nil
}
