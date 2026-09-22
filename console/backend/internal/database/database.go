// Package database provides SQLite database connectivity.
package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
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

	// probeMu serializes health probes. A probe proves a write reached the file
	// by writing a marker and reading it back, which only means anything if no
	// other probe can overwrite that marker in between.
	probeMu sync.Mutex
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

// HealthCheck verifies that the console's database can be written to and read
// back.
//
// It is deliberately NOT a Ping. With mattn/go-sqlite3 a Ping on an established
// connection returns nil without sending any SQL, so a full disk, a read-only
// filesystem or a database file deleted underneath the process all reported
// "healthy" while every real write failed. /health is the docker-compose
// liveness probe and the criterion docs/disaster-recovery.md uses to declare a
// restore good, so a green check that cannot go red is worse than no check.
//
// What it proves: the database file is still at the path SQLite has open, a
// write transaction against it commits, and the committed row is visible to a
// later read. What it does NOT prove: that pages the probe never touches are
// uncorrupted (that is PRAGMA integrity_check, too expensive for a check that
// runs every few seconds), that a provider or an agent is reachable, or that
// orchestration is running at all — the dispatcher reports that separately
// (internal/orchestrator).
func (d *DB) HealthCheck(ctx context.Context) error {
	// Held across the whole write-then-read pair so two concurrent probes cannot
	// read each other's marker and report a failure that never happened.
	d.probeMu.Lock()
	defer d.probeMu.Unlock()

	if err := d.probeDatabaseFile(ctx); err != nil {
		return err
	}

	if _, err := d.db.ExecContext(ctx, createHealthProbeTable); err != nil {
		return fmt.Errorf("health probe: create probe table: %w", err)
	}

	marker, err := newProbeMarker()
	if err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, writeHealthProbeMarker, marker, time.Now().UTC()); err != nil {
		return fmt.Errorf("health probe: write marker: %w", err)
	}

	var readBack string
	if err := d.db.QueryRowContext(ctx, readHealthProbeMarker).Scan(&readBack); err != nil {
		return fmt.Errorf("health probe: read marker: %w", err)
	}
	if readBack != marker {
		return fmt.Errorf("health probe: marker did not round-trip: wrote %q, read %q", marker, readBack)
	}
	return nil
}

// probeDatabaseFile checks that the main database file is still where SQLite has
// it open.
//
// The write half of the probe cannot see a deletion on its own: POSIX keeps an
// unlinked file writable through the descriptor the process already holds, so
// every write would keep succeeding against an inode nothing can open again —
// the console would report healthy while all its data was unreachable. PRAGMA
// database_list reports the path SQLite itself resolved (relative DSNs
// included), so this needs no second copy of the configured path. An in-memory
// database reports no file and has nothing to lose, so it is skipped, not
// failed.
func (d *DB) probeDatabaseFile(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx, listDatabases)
	if err != nil {
		return fmt.Errorf("health probe: list databases: %w", err)
	}
	defer rows.Close()

	var file string
	for rows.Next() {
		var (
			seq  int
			name string
			path string
		)
		if err := rows.Scan(&seq, &name, &path); err != nil {
			return fmt.Errorf("health probe: read database_list: %w", err)
		}
		if name == "main" {
			file = path
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("health probe: read database_list: %w", err)
	}
	if file == "" {
		return nil
	}
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("health probe: database file: %w", err)
	}
	return nil
}

// newProbeMarker returns a fresh random marker.
//
// It has to differ on every call: a constant would let the read satisfy itself
// from a value an earlier probe wrote, which is exactly the "the write went
// nowhere" failure this probe exists to catch.
func newProbeMarker() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("health probe: marker: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// The probe table holds exactly ONE row (id is pinned to 1 by the CHECK). The
// probe runs on every liveness poll, so it rewrites that row rather than
// appending: the database file cannot grow for as long as the console runs, and
// the write-ahead log these writes pass through is bounded by SQLite's own
// auto-checkpoint.
//
// It is created here rather than in migrate() because it is not application
// schema: a database restored from a backup taken before this probe existed must
// still pass its first check, and the IF NOT EXISTS form is what makes that work
// without a schema-version step.
const createHealthProbeTable = `
CREATE TABLE IF NOT EXISTS _health_probe (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	marker     TEXT NOT NULL,
	checked_at DATETIME NOT NULL
)`

const writeHealthProbeMarker = `
INSERT INTO _health_probe (id, marker, checked_at) VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET marker = excluded.marker, checked_at = excluded.checked_at`

const readHealthProbeMarker = `SELECT marker FROM _health_probe WHERE id = 1`

const listDatabases = `PRAGMA database_list`

// SchemaVersion is the schema this build creates and expects.
//
// It is stored in SQLite's own PRAGMA user_version so it travels inside the
// database file (and therefore inside any backup of it) without a table of its
// own. A database whose version is HIGHER than this build's is refused at open:
// running an older console against a newer schema could silently write rows the
// newer code no longer understands, and the failure would surface as corrupted
// data rather than an error. Restoring a backup into an older console is the
// same hazard, which is why backup carries the version too.
const SchemaVersion = 2

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
		createQubeInfraTable,
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
		{"purge_requested", "INTEGER NOT NULL DEFAULT 0"},
		{"agent_health", "TEXT NOT NULL DEFAULT 'unknown'"},
		{"agent_last_probed_at", "DATETIME"},
		{"agent_last_healthy_at", "DATETIME"},
		{"agent_last_error", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := d.addColumnIfMissing("qubes", c.column, c.definition); err != nil {
			return err
		}
	}

	if err := d.migrateLifecycle(); err != nil {
		return err
	}
	return d.applySchemaVersion()
}

// applySchemaVersion reads the file's user_version and stamps the current one.
//
// A file with no version (0) is either brand new or predates versioning; both
// are upgraded to SchemaVersion. A file with a HIGHER version is refused: see
// SchemaVersion.
func (d *DB) applySchemaVersion() error {
	have, err := d.UserVersion()
	if err != nil {
		return err
	}
	if have > SchemaVersion {
		return fmt.Errorf(
			"database schema version %d is newer than this console supports (%d); upgrade the console before opening this database",
			have, SchemaVersion)
	}
	if have == SchemaVersion {
		return nil
	}
	// #nosec G202 -- SchemaVersion is a compile-time constant.
	if _, err := d.db.ExecContext(context.Background(), fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("stamping schema version: %w", err)
	}
	return nil
}

// UserVersion returns the schema version recorded in the database file.
func (d *DB) UserVersion() (int, error) {
	var v int
	if err := d.db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
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

// createQubeInfraTable records the provider-side identity of a qube's
// infrastructure. It is the single source of truth that replaces a terraform
// state file.
//
// There is deliberately no foreign key onto qubes: a job that created a data
// disk must keep the row even if the qube row is later released, because the
// disk still exists and must be adoptable. Deleting the qube row does not
// delete infrastructure; only DestroyStorage does.
//
// protected is the explicit replacement for terraform's lifecycle.prevent_destroy:
// DestroyStorage refuses while it is set. Existing rows default to 1 so a fleet
// created before the flag existed is protected, not silently unprotected — the
// safe direction for a column guarding irreversible data loss.
const createQubeInfraTable = `
CREATE TABLE IF NOT EXISTS qube_infra (
	qube_id        TEXT PRIMARY KEY,
	provider       TEXT NOT NULL DEFAULT '',
	node           TEXT NOT NULL DEFAULT '',
	storage_vmid   INTEGER NOT NULL DEFAULT 0,
	compute_vmid   INTEGER NOT NULL DEFAULT 0,
	data_volume    TEXT NOT NULL DEFAULT '',
	identity_vol   TEXT NOT NULL DEFAULT '',
	observed_state TEXT NOT NULL DEFAULT '',
	protected      INTEGER NOT NULL DEFAULT 1,
	observed_at    DATETIME,
	created_at     DATETIME NOT NULL,
	updated_at     DATETIME NOT NULL
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
// record of who asked the system to change infrastructure and what the provider
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
