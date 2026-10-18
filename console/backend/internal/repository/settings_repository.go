package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
)

// SettingsRepository handles settings database operations.
type SettingsRepository struct {
	db *database.DB
}

// NewSettingsRepository creates a new settings repository.
func NewSettingsRepository(db *database.DB) *SettingsRepository {
	return &SettingsRepository{db: db}
}

// Get returns all settings.
//
// Every group is read in one statement so the result comes from a single
// SQLite snapshot. A group that was never stored keeps its default. A storage
// or decode failure is returned to the caller rather than replaced with a
// default: a read must not silently report different settings than the ones
// the console is using.
func (r *SettingsRepository) Get(ctx context.Context) (*models.Settings, error) {
	settings := &models.Settings{
		General: models.GeneralSettings{
			Timezone: "UTC",
			Language: "en",
			Theme:    "light",
		},
		Notifications: models.NotificationSettings{
			Email:      true,
			Webhook:    false,
			WebhookURL: "",
		},
		Security: models.SecuritySettings{
			SessionTimeout:   30,
			TwoFactorEnabled: false,
		},
	}

	rows, err := r.db.DB().QueryContext(ctx,
		`SELECT key, value FROM settings WHERE key IN ('general', 'notifications', 'security')`)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan settings: %w", err)
		}
		var target any
		switch key {
		case "general":
			target = &settings.General
		case "notifications":
			target = &settings.Notifications
		case "security":
			target = &settings.Security
		default:
			continue
		}
		if err := json.Unmarshal([]byte(value), target); err != nil {
			return nil, fmt.Errorf("decode %s settings: %w", key, err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}

	return settings, nil
}

// Update stores all setting groups in one transaction. A failure in any group
// leaves every group at its previous value; a partial write would make the
// console report settings the operator never saved.
func (r *SettingsRepository) Update(ctx context.Context, settings *models.Settings) error {
	return r.update(ctx, settings, nil)
}

// UpdateWithWebhook commits settings and their encrypted destination together.
// Both repositories must share the same database; no compensating writes are
// needed because the credential row is written in the settings transaction.
func (r *SettingsRepository) UpdateWithWebhook(ctx context.Context, settings *models.Settings, credentials *CredentialRepository, target string) error {
	if credentials == nil || credentials.db != r.db {
		return fmt.Errorf("settings and webhook must share a database")
	}
	return r.update(ctx, settings, func(tx *sql.Tx) error {
		return credentials.setAlertWebhookTarget(ctx, tx, target)
	})
}

func (r *SettingsRepository) update(ctx context.Context, settings *models.Settings, webhook func(*sql.Tx) error) error {
	tx, err := r.db.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin settings update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := writeSettings(ctx, tx, settings); err != nil {
		return err
	}
	if webhook != nil {
		if err := webhook(tx); err != nil {
			return fmt.Errorf("write webhook credential: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit settings: %w", err)
	}
	return nil
}

func writeSettings(ctx context.Context, tx *sql.Tx, settings *models.Settings) error {
	// The destination is stored as an encrypted credential. Keeping it (or the
	// derived "configured" flag) in the settings JSON would leak the URL through
	// a plaintext row and make the two stores disagree after a failed save.
	notifications := settings.Notifications
	notifications.WebhookURL = ""
	notifications.WebhookConfigured = false
	groups := []struct {
		key   string
		value any
	}{
		{"general", settings.General},
		{"notifications", notifications},
		{"security", settings.Security},
	}
	for _, group := range groups {
		encoded, err := json.Marshal(group.value)
		if err != nil {
			return fmt.Errorf("encode %s settings: %w", group.key, err)
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = datetime('now')`,
			group.key, string(encoded))
		if err != nil {
			return fmt.Errorf("write %s settings: %w", group.key, err)
		}
	}
	return nil
}
