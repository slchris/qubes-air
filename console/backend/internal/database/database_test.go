package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	cfg := DefaultConfig()
	cfg.DSN = tmpFile.Name()

	db, err := New(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, db)

	err = db.Close()
	assert.NoError(t, err)
}

func TestHealthCheck(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	cfg := DefaultConfig()
	cfg.DSN = tmpFile.Name()

	db, err := New(cfg)
	require.NoError(t, err)
	defer db.Close()

	err = db.HealthCheck(context.Background())
	assert.NoError(t, err)
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.NotEmpty(t, cfg.DSN)
	assert.Greater(t, cfg.MaxOpenConns, 0)
	assert.Greater(t, cfg.MaxIdleConns, 0)
	assert.Greater(t, cfg.ConnMaxLifetime.Seconds(), float64(0))
}

// readProbeMarker returns the marker the last successful probe committed.
func readProbeMarker(t *testing.T, db *DB) string {
	t.Helper()
	var marker string
	require.NoError(t, db.DB().QueryRow("SELECT marker FROM _health_probe WHERE id = 1").Scan(&marker))
	return marker
}

// TestHealthCheckWritesAndReadsMarker — /health is a control (it decides whether
// the console is restarted or rolled back), so it has to prove something that
// can actually fail. PingContext cannot: with mattn/go-sqlite3 a Ping on a live
// connection returns nil without sending any SQL, so a full disk, a read-only
// filesystem or a vanished database file all reported healthy. This asserts the
// probe left a committed marker behind and replaced it on the next call.
func TestHealthCheckWritesAndReadsMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.db")
	cfg := DefaultConfig()
	cfg.DSN = path

	db, err := New(cfg)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.HealthCheck(context.Background()))
	first := readProbeMarker(t, db)
	assert.NotEmpty(t, first, "a probe that succeeded must have written a marker")

	require.NoError(t, db.HealthCheck(context.Background()))
	second := readProbeMarker(t, db)
	assert.NotEqual(t, first, second, "every probe must commit a fresh marker, not read back a stale one")
}

// TestHealthCheckDoesNotGrowTheDatabase — the probe runs on every liveness poll,
// so it must rewrite a single row. An appending probe would grow the file for
// as long as the console runs.
func TestHealthCheckDoesNotGrowTheDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded.db")
	cfg := DefaultConfig()
	cfg.DSN = path

	db, err := New(cfg)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.HealthCheck(context.Background()))
	var pagesAfterFirst int
	require.NoError(t, db.DB().QueryRow("PRAGMA page_count").Scan(&pagesAfterFirst))

	for i := 0; i < 200; i++ {
		require.NoError(t, db.HealthCheck(context.Background()))
	}

	var rows, pagesAfterMany int
	require.NoError(t, db.DB().QueryRow("SELECT COUNT(*) FROM _health_probe").Scan(&rows))
	require.NoError(t, db.DB().QueryRow("PRAGMA page_count").Scan(&pagesAfterMany))
	assert.Equal(t, 1, rows, "probes must reuse one row")
	assert.Equal(t, pagesAfterFirst, pagesAfterMany, "probes must not allocate new pages")
}

// TestHealthCheckIsSafeUnderConcurrentProbes — nothing serializes callers of
// /health, so probes must be correct in parallel, not merely race-free. The
// round-trip check is what would break if two probes shared the marker row: one
// would read back the other's value and report a failure that never happened.
func TestHealthCheckIsSafeUnderConcurrentProbes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	cfg := DefaultConfig()
	cfg.DSN = path

	db, err := New(cfg)
	require.NoError(t, err)
	defer db.Close()

	const probes = 16
	errs := make(chan error, probes)
	var wg sync.WaitGroup
	for i := 0; i < probes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := db.HealthCheck(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent probe failed: %v", err)
	}
}

// TestHealthCheckFailsOnReadOnlyDatabase is the failure path that matters most:
// the database is readable but not writable — a read-only filesystem, a revoked
// share, or a full disk that leaves the file intact.
//
// The connection is opened read-only rather than by chmod'ing the file: SQLite
// enforces the flag itself, so the test reproduces regardless of the user
// running it, while a permission bit proves nothing when tests run as root. The
// `file:` URI form is required — the driver ignores a bare `?mode=ro` and opens
// read-write, which is how this test first passed against a writable file.
func TestHealthCheckFailsOnReadOnlyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly.db")
	cfg := DefaultConfig()
	cfg.DSN = path

	writable, err := New(cfg)
	require.NoError(t, err)
	// Create the probe table while the file is still writable, so the CREATE
	// TABLE IF NOT EXISTS below is a no-op and the failure is the marker write.
	require.NoError(t, writable.HealthCheck(context.Background()))
	defer writable.Close()

	ro, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()

	err = (&DB{db: ro}).HealthCheck(context.Background())
	require.Error(t, err, "a database that cannot be written must not report healthy")
	assert.Contains(t, err.Error(), "readonly", "the probe must fail on the write, not on opening the file")
}

// TestHealthCheckFailsWhenDatabaseFileIsDeleted — POSIX keeps an unlinked file
// writable through the descriptor the process already holds, so the write half
// of the probe still succeeds after someone deletes the database. Only the path
// check can see it, and without it a console holding a database that no longer
// exists on disk would report healthy while every write landed in a file
// nothing can ever read again.
func TestHealthCheckFailsWhenDatabaseFileIsDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deleted.db")
	cfg := DefaultConfig()
	cfg.DSN = path

	db, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, db.HealthCheck(context.Background()))

	require.NoError(t, os.Remove(path))

	require.Error(t, db.HealthCheck(context.Background()),
		"a deleted database file must not report healthy")
	// The handle is still usable as far as the OS is concerned; closing it can
	// legitimately fail once its WAL is gone.
	_ = db.Close()
}

func TestBuildDSN(t *testing.T) {
	dsn := buildDSN("/tmp/test.db")

	assert.Contains(t, dsn, "/tmp/test.db")
	assert.Contains(t, dsn, "_journal_mode=WAL")
	assert.Contains(t, dsn, "_foreign_keys=on")
}

func TestSchemaVersionStamped(t *testing.T) {
	path := t.TempDir() + "/stamp.db"
	cfg := DefaultConfig()
	cfg.DSN = path

	db, err := New(cfg)
	require.NoError(t, err)
	defer db.Close()

	got, err := db.UserVersion()
	require.NoError(t, err)
	assert.Equal(t, SchemaVersion, got)
}

// TestRefusesNewerSchema is the downgrade guard: an older console must not open
// a database written by a newer one, or it would write rows the newer schema no
// longer expects.
func TestRefusesNewerSchema(t *testing.T) {
	path := t.TempDir() + "/newer.db"
	cfg := DefaultConfig()
	cfg.DSN = path

	db, err := New(cfg)
	require.NoError(t, err)
	_, err = db.DB().Exec("PRAGMA user_version = " + strconv.Itoa(SchemaVersion+1))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = New(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newer than this console supports")
}
