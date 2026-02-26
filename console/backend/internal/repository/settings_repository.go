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

	// Load general settings
	general, err := r.getSettingValue(ctx, "general")
	if err == nil && general != "" {
		if unmarshalErr := json.Unmarshal([]byte(general), &settings.General); unmarshalErr != nil {
			return nil, fmt.Errorf("failed to unmarshal general settings: %w", unmarshalErr)
		}
	}

	// Load notification settings
	notifications, err := r.getSettingValue(ctx, "notifications")
	if err == nil && notifications != "" {
		if unmarshalErr := json.Unmarshal([]byte(notifications), &settings.Notifications); unmarshalErr != nil {
			return nil, fmt.Errorf("failed to unmarshal notification settings: %w", unmarshalErr)
		}
	}

	// Load security settings
	security, err := r.getSettingValue(ctx, "security")
	if err == nil && security != "" {
		if unmarshalErr := json.Unmarshal([]byte(security), &settings.Security); unmarshalErr != nil {
			return nil, fmt.Errorf("failed to unmarshal security settings: %w", unmarshalErr)
		}
	}

	return settings, nil
}

// getSettingValue gets a single setting value by key.
func (r *SettingsRepository) getSettingValue(ctx context.Context, key string) (string, error) {
	query := `SELECT value FROM settings WHERE key = ?`
	var value string
	err := r.db.DB().QueryRowContext(ctx, query, key).Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return value, nil
}

// Update updates settings.
func (r *SettingsRepository) Update(ctx context.Context, settings *models.Settings) error {
	// Update general settings
	generalJSON, err := json.Marshal(settings.General)
	if err != nil {
		return err
	}
	err = r.upsertSetting(ctx, "general", string(generalJSON))
	if err != nil {
		return err
	}

	// Update notification settings
	notificationsJSON, err := json.Marshal(settings.Notifications)
	if err != nil {
		return err
	}
	err = r.upsertSetting(ctx, "notifications", string(notificationsJSON))
	if err != nil {
		return err
	}

	// Update security settings
	securityJSON, err := json.Marshal(settings.Security)
	if err != nil {
		return err
	}
	err = r.upsertSetting(ctx, "security", string(securityJSON))
	if err != nil {
		return err
	}

	return nil
}

// upsertSetting inserts or updates a setting.
func (r *SettingsRepository) upsertSetting(ctx context.Context, key, value string) error {
	query := `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, datetime('now')) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = datetime('now')`
	_, err := r.db.DB().ExecContext(ctx, query, key, value)
	return err
}
