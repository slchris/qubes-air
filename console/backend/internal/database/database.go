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

	// Background: this runs at startup, before any request context exists.
	if err := db.PingContext(context.Background()); err != nil {
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
		createInfrastructureTable,
		createCredentialsTable,
		createSettingsTable,
		createJobsTable,
		createAgentCertsTable,
		createBootstrapTokensTable,
	}

	for _, m := range migrations {
		if _, err := d.db.ExecContext(context.Background(), m); err != nil {
			return err
		}
	}

	// Additive column migrations. These run after the CREATE TABLE IF NOT
	// EXISTS statements above so they also upgrade databases created by an
	// earlier schema version. Each is idempotent (skipped if the column
	// already exists), so existing rows and data are never destroyed.
	if err := d.addColumnIfMissing("credentials", "key_version", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}

	// Agent health: whether the agent inside a qube answers, which is a separate
	// fact from the VM's own status (see models.Qube). Existing rows backfill to
	// 'unknown' — the truthful value for a qube that has never been probed, and
	// specifically not 'healthy', which would reproduce the very bug these
	// columns exist to catch.
	for _, c := range []struct{ column, definition string }{
		{"agent_health", "TEXT NOT NULL DEFAULT 'unknown'"},
		{"agent_last_probed_at", "DATETIME"},
		{"agent_last_healthy_at", "DATETIME"},
		{"agent_last_error", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := d.addColumnIfMissing("qubes", c.column, c.definition); err != nil {
			return err
		}
	}

	return nil
}

// addColumnIfMissing adds a column to a table only if it does not already
// exist, making the migration safe to run repeatedly on existing databases.
//
// SQLite has no "ALTER TABLE ... ADD COLUMN IF NOT EXISTS", so the column set is
// inspected via PRAGMA table_info first. The ADD COLUMN definition MUST give
// pre-existing rows a deterministic value — for key_version that means a
// non-NULL DEFAULT backfilling legacy rows to version 1, which is exactly the
// key that originally encrypted them.
//
// Nullable columns are the one exception, and only where NULL is itself the
// correct answer for a legacy row: agent_last_probed_at has no default because
// "never probed" is the truth about a qube that predates probing, and a
// fabricated zero timestamp would assert an observation that never happened.
func (d *DB) addColumnIfMissing(table, column, definition string) error {
	// #nosec G202 -- table/column/definition are compile-time constants from
	// this package, never user input; PRAGMA cannot be parameterized.
	rows, err := d.db.QueryContext(context.Background(), fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("inspecting %s: %w", table, err)
	}
	defer rows.Close()

	exists := false
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notNull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &primaryKey); err != nil {
			return fmt.Errorf("scanning table_info for %s: %w", table, err)
		}
		if name == column {
			exists = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if exists {
		return nil
	}

	// #nosec G202 -- identifiers are constants from this package, not user input.
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)
	if _, err := d.db.ExecContext(context.Background(), stmt); err != nil {
		return fmt.Errorf("adding column %s.%s: %w", table, column, err)
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

// createQubesTable defines the schema for freshly created databases.
//
// status describes the compute instance; the agent_* columns describe whether
// the agent inside it answers. They are separate columns because they are
// separate facts — a running VM with a dead agent is a state the console has to
// be able to express (see models.Qube). Databases created before the agent_*
// columns existed are upgraded by addColumnIfMissing in migrate(), which
// backfills agent_health='unknown'.
const createQubesTable = `
CREATE TABLE IF NOT EXISTS qubes (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	zone_id TEXT DEFAULT '',
	status TEXT NOT NULL DEFAULT 'stopped',
	spec TEXT DEFAULT '{}',
	ip_address TEXT DEFAULT '',
	agent_health TEXT NOT NULL DEFAULT 'unknown',
	agent_last_probed_at DATETIME,
	agent_last_healthy_at DATETIME,
	agent_last_error TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
)`

const createInfrastructureTable = `
CREATE TABLE IF NOT EXISTS infrastructure (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'disconnected',
	region TEXT DEFAULT '',
	config TEXT DEFAULT '{}',
	resource_count INTEGER DEFAULT 0,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
)`

// createCredentialsTable defines the schema for freshly created databases.
// key_version records which encryption key version encrypted encrypted_data so
// the key can be rotated (see internal/keyring). Existing databases created
// before key_version existed are upgraded by addColumnIfMissing in migrate(),
// which backfills key_version=1 for legacy rows.
// createJobsTable records every orchestration job.
//
// Jobs are kept as an AUDIT TRAIL, not merely as poll targets: they are the
// record of who asked the system to change infrastructure and what terraform
// reported back. Rows are therefore never updated destructively beyond their
// own lifecycle, and never deleted when the qube they reference is released —
// hence no foreign key onto qubes, which would cascade or block.
const createJobsTable = `
CREATE TABLE IF NOT EXISTS jobs (
	id TEXT PRIMARY KEY,
	qube_id TEXT NOT NULL,
	qube_name TEXT NOT NULL,
	action TEXT NOT NULL,
	state TEXT NOT NULL,
	error TEXT DEFAULT '',
	enqueued_at DATETIME NOT NULL,
	started_at DATETIME,
	finished_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_jobs_qube_id ON jobs(qube_id);
CREATE INDEX IF NOT EXISTS idx_jobs_enqueued_at ON jobs(enqueued_at DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state)`

// createAgentCertsTable is the registry of client certificates allowed to
// connect, and the mechanism by which one is revoked.
//
// Why a fingerprint allowlist rather than a CRL or OCSP: the party verifying the
// certificate IS the party that issued it and IS the party that owns this
// database. A CRL would mean publishing a list, distributing it, and hoping the
// verifier fetched a fresh copy — and a CRL nobody actually checks provides zero
// security. Here revocation is a row update the verifier reads on the next
// handshake, with no distribution step that can silently fail.
//
// fingerprint is the SHA-256 of the certificate DER, which is what the TLS stack
// hands us at verification time.
const createAgentCertsTable = `
CREATE TABLE IF NOT EXISTS agent_certs (
	fingerprint TEXT PRIMARY KEY,
	qube_id     TEXT NOT NULL,
	subject_cn  TEXT NOT NULL,
	issued_at   DATETIME NOT NULL,
	expires_at  DATETIME,
	revoked_at  DATETIME,
	revoked_reason TEXT DEFAULT '',
	last_seen_at   DATETIME
);
CREATE INDEX IF NOT EXISTS idx_agent_certs_qube_id ON agent_certs(qube_id);
CREATE INDEX IF NOT EXISTS idx_agent_certs_revoked ON agent_certs(revoked_at)`

// createBootstrapTokensTable holds the one-shot credentials that let an agent
// obtain its first certificate without the console shipping it a private key.
//
// The row stores the token's HASH, never the token: a leaked database yields
// nothing usable, the same reason a password store keeps digests. secret_hash is
// the primary key because that is the lookup path — an agent presents a secret
// and nothing else, so the digest is what identifies the row.
//
// qube_id is carried alongside qube_name because the two answer different
// questions: the name derives the certificate's common name, the id is what the
// issued certificate is registered against. Storing both means redemption needs
// no second lookup, and the pair recorded at mint time is the pair used at
// redemption — a rename between the two cannot retarget the certificate.
//
// redeemed_at is what makes the token single-use, and it is set by the same
// statement that authorizes the redemption. See BootstrapTokenRepository.Redeem
// for why that has to be one statement.
const createBootstrapTokensTable = `
CREATE TABLE IF NOT EXISTS bootstrap_tokens (
	secret_hash TEXT PRIMARY KEY,
	qube_id     TEXT NOT NULL,
	qube_name   TEXT NOT NULL,
	created_at  DATETIME NOT NULL,
	not_after   DATETIME NOT NULL,
	redeemed_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_bootstrap_tokens_qube_id ON bootstrap_tokens(qube_id);
CREATE INDEX IF NOT EXISTS idx_bootstrap_tokens_not_after ON bootstrap_tokens(not_after)`

const createCredentialsTable = `
CREATE TABLE IF NOT EXISTS credentials (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	description TEXT DEFAULT '',
	encrypted_data TEXT NOT NULL,
	key_version INTEGER NOT NULL DEFAULT 1,
	last_used DATETIME,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
)`

const createSettingsTable = `
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at DATETIME NOT NULL
)`
