package database

import (
	"path/filepath"
	"testing"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/require"
)

func openWebhookMigrationDB(t *testing.T) *Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "webhook.db")
	return cfg
}

func TestMigrateAlertWebhookCredentialMovesTheLegacyRow(t *testing.T) {
	cfg := openWebhookMigrationDB(t)
	db, err := New(cfg)
	require.NoError(t, err)
	_, err = db.DB().Exec(`INSERT INTO credentials
		(id, name, type, description, encrypted_data, key_version, created_at, updated_at)
		VALUES ('system:alert-webhook-target', 'Alert webhook endpoint', 'webhook',
		        'old description', 'ciphertext', 3, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = New(cfg)
	require.NoError(t, err)
	defer db.Close()

	var name, typ, description, encrypted string
	var version int
	require.NoError(t, db.DB().QueryRow(`SELECT name, type, description, encrypted_data, key_version
		FROM credentials WHERE id = ?`, models.ConsoleWebhookTargetID).
		Scan(&name, &typ, &description, &encrypted, &version))
	require.Equal(t, models.ConsoleWebhookTargetName, name)
	require.Equal(t, models.ConsoleRowType, typ)
	require.Equal(t, "old description", description)
	require.Equal(t, "ciphertext", encrypted)
	require.Equal(t, 3, version)

	var legacy int
	require.NoError(t, db.DB().QueryRow(
		`SELECT COUNT(*) FROM credentials WHERE id = 'system:alert-webhook-target'`).Scan(&legacy))
	require.Zero(t, legacy)
}

func TestMigrateAlertWebhookCredentialPrefersAnExistingNewRow(t *testing.T) {
	cfg := openWebhookMigrationDB(t)
	db, err := New(cfg)
	require.NoError(t, err)
	_, err = db.DB().Exec(`INSERT INTO credentials
		(id, name, type, description, encrypted_data, key_version, created_at, updated_at)
		VALUES ('qubes-air-alert-webhook-target', 'qubes-air-alert-webhook', 'pki',
		        'new description', 'new ciphertext', 4, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		       ('system:alert-webhook-target', 'Alert webhook endpoint', 'webhook',
		        'old description', 'old ciphertext', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = New(cfg)
	require.NoError(t, err)
	defer db.Close()

	var encrypted string
	require.NoError(t, db.DB().QueryRow(
		`SELECT encrypted_data FROM credentials WHERE id = ?`, models.ConsoleWebhookTargetID).Scan(&encrypted))
	require.Equal(t, "new ciphertext", encrypted)
	var legacy int
	require.NoError(t, db.DB().QueryRow(
		`SELECT COUNT(*) FROM credentials WHERE id = 'system:alert-webhook-target'`).Scan(&legacy))
	require.Zero(t, legacy)

	// Reopening again is a no-op.
	require.NoError(t, db.Close())
	db, err = New(cfg)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.DB().QueryRow(
		`SELECT encrypted_data FROM credentials WHERE id = ?`, models.ConsoleWebhookTargetID).Scan(&encrypted))
	require.Equal(t, "new ciphertext", encrypted)
}
