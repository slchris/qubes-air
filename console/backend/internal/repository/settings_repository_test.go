package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSettingsRepositoryReadFailure(t *testing.T) {
	for _, failure := range []string{"canceled", "deadline", "database", "json"} {
		t.Run(failure, func(t *testing.T) {
			db, cleanup := setupQubeTestDB(t)
			defer cleanup()
			repo := NewSettingsRepository(db)
			ctx := context.Background()
			switch failure {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			case "database":
				require.NoError(t, db.Close())
			case "json":
				_, err := db.DB().Exec(`INSERT INTO settings(key,value,updated_at) VALUES ('security','broken',CURRENT_TIMESTAMP)`)
				require.NoError(t, err)
			}

			settings, err := repo.Get(ctx)
			require.Error(t, err)
			require.Nil(t, settings)
		})
	}
}

func TestSettingsRepositoryReadUsesDefaultsForUnsetGroups(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()

	settings, err := NewSettingsRepository(db).Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "UTC", settings.General.Timezone)
	require.Equal(t, "en", settings.General.Language)
	require.Equal(t, "light", settings.General.Theme)
	require.True(t, settings.Notifications.Email)
	require.Equal(t, 30, settings.Security.SessionTimeout)
}

func TestSettingsRepositoryAtomicUpdate(t *testing.T) {
	for _, failure := range []string{"general", "notifications", "security"} {
		t.Run(failure, func(t *testing.T) {
			db, cleanup := setupQubeTestDB(t)
			defer cleanup()
			repo := NewSettingsRepository(db)
			ctx := context.Background()

			original, err := repo.Get(ctx)
			require.NoError(t, err)
			require.NoError(t, repo.Update(ctx, original))

			_, err = db.DB().Exec(
				`CREATE TRIGGER reject_setting BEFORE UPDATE ON settings
				 WHEN NEW.key = '` + failure + `'
				 BEGIN SELECT RAISE(FAIL, 'injected failure'); END`)
			require.NoError(t, err)

			changed := *original
			changed.General.Theme = "dark"
			changed.Notifications.Email = !original.Notifications.Email
			changed.Notifications.Webhook = true
			changed.Notifications.WebhookURL = "https://hooks.example.test"
			changed.Security.SessionTimeout = 5

			require.Error(t, repo.Update(ctx, &changed))

			after, err := repo.Get(ctx)
			require.NoError(t, err)
			require.Equal(t, *original, *after)
		})
	}
}

func TestSettingsRepositoryCanceledUpdateLeavesSettings(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewSettingsRepository(db)
	ctx := context.Background()

	original, err := repo.Get(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.Update(ctx, original))

	changed := *original
	changed.General.Theme = "dark"
	changed.Security.SessionTimeout = 5

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(t, repo.Update(canceled, &changed))

	after, err := repo.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, *original, *after)
}
