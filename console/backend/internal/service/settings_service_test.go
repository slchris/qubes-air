package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSettingsFixture opens a migrated database and returns the service plus a
// raw handle for writing rows the way an older release would have.
func newSettingsFixture(t *testing.T) (*SettingsService, *repository.SettingsRepository, *database.DB) {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "settings.db")
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := repository.NewSettingsRepository(db)
	return NewSettingsService(repo), repo, db
}

// storeRawSetting writes one settings row verbatim, bypassing validation, as a
// release that accepted any integer would have left it.
func storeRawSetting(t *testing.T, db *database.DB, key, value string) {
	t.Helper()
	_, err := db.DB().Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	require.NoError(t, err)
}

func validSettings(timeout int) *models.Settings {
	return &models.Settings{
		General:  models.GeneralSettings{Timezone: "UTC", Language: "en", Theme: "system"},
		Security: models.SecuritySettings{SessionTimeout: timeout},
	}
}

func TestValidateSessionTimeoutMinutesBounds(t *testing.T) {
	for _, minutes := range []int{MinSessionTimeoutMinutes, DefaultSessionTimeoutMinutes, MaxSessionTimeoutMinutes} {
		assert.NoError(t, ValidateSessionTimeoutMinutes(minutes), "%d minutes", minutes)
	}
	for _, minutes := range []int{-30, 0, MinSessionTimeoutMinutes - 1, MaxSessionTimeoutMinutes + 1, 1 << 30} {
		err := ValidateSessionTimeoutMinutes(minutes)
		assert.ErrorIs(t, err, ErrInvalidSessionTimeout, "%d minutes", minutes)
	}
}

// TestSettingsUpdateRefusesAnOutOfRangeTimeout — the refusal happens before
// anything is written, so the stored settings stay as they were.
func TestSettingsUpdateRefusesAnOutOfRangeTimeout(t *testing.T) {
	svc, repo, _ := newSettingsFixture(t)
	ctx := context.Background()
	require.NoError(t, svc.Update(ctx, validSettings(45)))

	for _, minutes := range []int{0, 4, 1441} {
		err := svc.Update(ctx, validSettings(minutes))
		require.ErrorIs(t, err, ErrInvalidSessionTimeout, "%d minutes", minutes)
	}
	stored, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, 45, stored.Security.SessionTimeout, "a refused save must not reach the database")
}

// TestSettingsUpdateRefusesUnimplementedSettings — email notifications and
// two-factor authentication do nothing, so they cannot be switched on.
func TestSettingsUpdateRefusesUnimplementedSettings(t *testing.T) {
	svc, repo, _ := newSettingsFixture(t)
	ctx := context.Background()

	email := validSettings(30)
	email.Notifications.Email = true
	assert.ErrorIs(t, svc.Update(ctx, email), ErrUnsupportedSetting)

	twoFactor := validSettings(30)
	twoFactor.Security.TwoFactorEnabled = true
	assert.ErrorIs(t, svc.Update(ctx, twoFactor), ErrUnsupportedSetting)

	stored, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.False(t, stored.Security.TwoFactorEnabled)
	assert.Equal(t, 30, stored.Security.SessionTimeout, "nothing was stored; the repository default remains")
}

// TestSettingsGetReportsTheEffectiveValues — values an older release stored,
// and which the console does not honor, read as what is actually in effect;
// the read writes nothing back.
func TestSettingsGetReportsTheEffectiveValues(t *testing.T) {
	svc, repo, db := newSettingsFixture(t)
	ctx := context.Background()
	storeRawSetting(t, db, "security", `{"sessionTimeout":100000,"twoFactorEnabled":true}`)
	storeRawSetting(t, db, "notifications", `{"email":true,"webhook":false,"webhookUrl":""}`)

	got, err := svc.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, DefaultSessionTimeoutMinutes, got.Security.SessionTimeout)
	assert.False(t, got.Security.TwoFactorEnabled)
	assert.False(t, got.Notifications.Email)

	raw, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, 100000, raw.Security.SessionTimeout, "Get must not rewrite the stored row")
}

func TestSettingsGetKeepsAValidTimeout(t *testing.T) {
	svc, _, _ := newSettingsFixture(t)
	ctx := context.Background()
	require.NoError(t, svc.Update(ctx, validSettings(MaxSessionTimeoutMinutes)))

	got, err := svc.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, MaxSessionTimeoutMinutes, got.Security.SessionTimeout)
}

func TestSettingsSessionTTL(t *testing.T) {
	ctx := context.Background()

	t.Run("nothing stored uses the default", func(t *testing.T) {
		svc, _, _ := newSettingsFixture(t)
		ttl, err := svc.SessionTTL(ctx)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(DefaultSessionTimeoutMinutes)*time.Minute, ttl)
	})

	t.Run("a saved timeout", func(t *testing.T) {
		svc, _, _ := newSettingsFixture(t)
		require.NoError(t, svc.Update(ctx, validSettings(45)))
		ttl, err := svc.SessionTTL(ctx)
		require.NoError(t, err)
		assert.Equal(t, 45*time.Minute, ttl)
	})

	for _, stored := range []string{`{"sessionTimeout":0}`, `{"sessionTimeout":1}`, `{"sessionTimeout":-5}`, `{"sessionTimeout":1441}`} {
		t.Run("out of range "+stored, func(t *testing.T) {
			svc, _, db := newSettingsFixture(t)
			storeRawSetting(t, db, "security", stored)
			ttl, err := svc.SessionTTL(ctx)
			require.ErrorIs(t, err, ErrInvalidSessionTimeout)
			assert.Zero(t, ttl)
		})
	}

	t.Run("row that does not parse", func(t *testing.T) {
		svc, _, db := newSettingsFixture(t)
		storeRawSetting(t, db, "security", `{"sessionTimeout":"thirty"}`)
		_, err := svc.SessionTTL(ctx)
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrInvalidSessionTimeout), "a row that does not parse is not a range violation: %v", err)
	})
}

// newWebhookSettingsFixture returns a service backed by a real keyring and an
// allowlisted destination, plus the raw handles needed to inject failures.
func newWebhookSettingsFixture(t *testing.T) (*SettingsService, *database.DB, *repository.CredentialRepository) {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "settings.db")
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	kr, err := keyring.NewSingle([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	credentials := repository.NewCredentialRepository(db, kr)
	svc := NewSettingsService(repository.NewSettingsRepository(db)).
		WithWebhookCredentials(credentials).
		WithWebhookAllowedHosts([]string{"hooks.example.test"})
	return svc, db, credentials
}

func TestValidateWebhookTargetRequiresConfiguredHTTPSHost(t *testing.T) {
	tests := []struct {
		name  string
		url   string
		allow []string
		valid bool
	}{
		{"configured host", "https://hooks.example.test/v1/events", []string{"hooks.example.test"}, true},
		{"default port", "https://hooks.example.test:443/events", []string{"hooks.example.test:443"}, true},
		{"explicit configured port", "https://hooks.example.test:8443/events", []string{"hooks.example.test:8443"}, true},
		{"not allowlisted", "https://other.example.test/events", []string{"hooks.example.test"}, false},
		{"http rejected", "http://hooks.example.test/events", []string{"hooks.example.test"}, false},
		{"userinfo rejected", "https://user:pass@hooks.example.test/events", []string{"hooks.example.test"}, false},
		{"nonstandard port rejected", "https://hooks.example.test:8443/events", []string{"hooks.example.test"}, false},
		{"fragment rejected", "https://hooks.example.test/events#frag", []string{"hooks.example.test"}, false},
		{"no hosts configured", "https://hooks.example.test/events", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateWebhookTarget(test.url, test.allow)
			if test.valid {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidWebhookTarget)
		})
	}
}

// TestSettingsUpdateRollbackIncludesWebhook injects a failure into the last
// write (settings for enable/replace, the credential delete for disable) and
// asserts that neither store changed.
func TestSettingsUpdateRollbackIncludesWebhook(t *testing.T) {
	for _, operation := range []string{"enable", "replace", "disable"} {
		t.Run(operation, func(t *testing.T) {
			svc, db, credentials := newWebhookSettingsFixture(t)
			ctx := context.Background()
			original := &models.Settings{Security: models.SecuritySettings{SessionTimeout: 30}}
			if operation != "enable" {
				original.Notifications.Webhook = true
				original.Notifications.WebhookURL = "https://hooks.example.test/original"
			}
			require.NoError(t, svc.Update(ctx, original))
			oldTarget, oldConfigured, err := credentials.GetAlertWebhookTarget(ctx)
			require.NoError(t, err)
			trigger := `CREATE TRIGGER fail_security BEFORE UPDATE ON settings WHEN NEW.key='security' BEGIN SELECT RAISE(FAIL,'injected'); END`
			if operation == "disable" {
				trigger = `CREATE TRIGGER fail_delete BEFORE DELETE ON credentials BEGIN SELECT RAISE(FAIL,'injected'); END`
			}
			_, err = db.DB().Exec(trigger)
			require.NoError(t, err)
			changed := *original
			changed.General.Theme = "dark"
			changed.Security.SessionTimeout = 5
			changed.Notifications.Webhook = operation != "disable"
			changed.Notifications.WebhookURL = "https://hooks.example.test/replacement"
			require.Error(t, svc.Update(ctx, &changed))
			after, err := svc.Get(ctx)
			require.NoError(t, err)
			require.Equal(t, original, after)
			target, configured, err := credentials.GetAlertWebhookTarget(ctx)
			require.NoError(t, err)
			require.Equal(t, oldTarget, target)
			require.Equal(t, oldConfigured, configured)
		})
	}
}

func TestSettingsRepeatedCanceledAndConcurrentUpdates(t *testing.T) {
	svc, _, credentials := newWebhookSettingsFixture(t)
	ctx := context.Background()
	initial := &models.Settings{Security: models.SecuritySettings{SessionTimeout: 30}, Notifications: models.NotificationSettings{Webhook: true, WebhookURL: "https://hooks.example.test/initial"}}
	require.NoError(t, svc.Update(ctx, initial))
	// Saving the redacted API response preserves the encrypted destination.
	require.NoError(t, svc.Update(ctx, initial))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	changed := *initial
	changed.Notifications.WebhookURL = "https://hooks.example.test/canceled"
	require.Error(t, svc.Update(canceled, &changed))
	target, _, err := credentials.GetAlertWebhookTarget(ctx)
	require.NoError(t, err)
	require.Equal(t, "https://hooks.example.test/initial", target)
	errors := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func(index int) {
			name := fmt.Sprintf("writer-%d", index)
			settings := &models.Settings{General: models.GeneralSettings{Theme: name}, Security: models.SecuritySettings{SessionTimeout: 30}, Notifications: models.NotificationSettings{Webhook: true, WebhookURL: "https://hooks.example.test/" + name}}
			errors <- svc.Update(ctx, settings)
		}(i)
	}
	for i := 0; i < 12; i++ {
		require.NoError(t, <-errors)
	}
	final, err := svc.Get(ctx)
	require.NoError(t, err)
	target, configured, err := svc.WebhookTarget(ctx)
	require.NoError(t, err)
	require.True(t, configured)
	require.Equal(t, "https://hooks.example.test/"+final.General.Theme, target)
}

func TestSettingsCredentialWriteFailureRollsBackSettings(t *testing.T) {
	for _, operation := range []string{"INSERT", "UPDATE"} {
		t.Run(operation, func(t *testing.T) {
			svc, db, _ := newWebhookSettingsFixture(t)
			ctx := context.Background()
			original := &models.Settings{Security: models.SecuritySettings{SessionTimeout: 30}}
			if operation == "UPDATE" {
				original.Notifications.Webhook = true
				original.Notifications.WebhookURL = "https://hooks.example.test/original"
			}
			require.NoError(t, svc.Update(ctx, original))
			_, err := db.DB().Exec(`CREATE TRIGGER reject_credential BEFORE ` + operation + ` ON credentials BEGIN SELECT RAISE(FAIL,'injected'); END`)
			require.NoError(t, err)
			changed := *original
			changed.Security.SessionTimeout = 5
			changed.Notifications.Webhook = true
			changed.Notifications.WebhookURL = "https://hooks.example.test/new"
			require.Error(t, svc.Update(ctx, &changed))
			actual, err := svc.Get(ctx)
			require.NoError(t, err)
			require.Equal(t, original, actual)
			if operation == "UPDATE" {
				target, enabled, err := svc.WebhookTarget(ctx)
				require.NoError(t, err)
				require.True(t, enabled)
				require.Equal(t, "https://hooks.example.test/original", target)
			}
		})
	}
}
