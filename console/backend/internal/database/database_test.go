package database

import (
	"context"
	"os"
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
