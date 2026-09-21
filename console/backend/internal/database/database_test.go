package database

import (
	"context"
	"os"
	"strconv"
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
