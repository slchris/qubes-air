// Package database provides SQLite database connectivity.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Config holds database configuration.
type Config struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// DefaultConfig returns default database configuration.
func DefaultConfig() *Config {
	return &Config{
		DSN:             "./qubes-air.db",
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
	}
}

// DB wraps the SQL database connection.
type DB struct {
	db *sql.DB
}

// New creates a new database connection.
func New(cfg *Config) (*DB, error) {
	dsn := buildDSN(cfg.DSN)

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	configurePool(db, cfg)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	wrapper := &DB{db: db}

	if err := wrapper.migrate(); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return wrapper, nil
}

// buildDSN constructs the SQLite DSN with options.
func buildDSN(path string) string {
	return fmt.Sprintf("%s?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000", path)
}

// configurePool sets connection pool parameters.
func configurePool(db *sql.DB, cfg *Config) {
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
}

// DB returns the underlying sql.DB.
func (d *DB) DB() *sql.DB {
	return d.db
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

// HealthCheck verifies database connectivity.
func (d *DB) HealthCheck(ctx context.Context) error {
	return d.db.PingContext(ctx)
}

// migrate runs database migrations.
func (d *DB) migrate() error {
	migrations := []string{
		createZonesTable,
		createQubesTable,
	}

	for _, m := range migrations {
		if _, err := d.db.Exec(m); err != nil {
			return err
		}
	}

	return nil
}

const createZonesTable = `
CREATE TABLE IF NOT EXISTS zones (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'disconnected',
	config TEXT DEFAULT '{}',
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
)`

const createQubesTable = `
CREATE TABLE IF NOT EXISTS qubes (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	zone_id TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'stopped',
	spec TEXT DEFAULT '{}',
	ip_address TEXT DEFAULT '',
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	FOREIGN KEY (zone_id) REFERENCES zones(id)
)`
