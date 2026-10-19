package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMonitoringAlertEventsRecordStateTransitionsAndResourceDeletion(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	ctx := context.Background()
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	_, err := db.DB().ExecContext(ctx, `INSERT INTO zones (id, name, type, status, created_at, updated_at)
		VALUES ('z1', 'lab', 'proxmox', 'connected', ?, ?)`, base, base)
	require.NoError(t, err)
	_, err = db.DB().ExecContext(ctx, `UPDATE zones SET status = 'disconnected', updated_at = ? WHERE id = 'z1'`, base.Add(time.Minute))
	require.NoError(t, err)
	_, err = db.DB().ExecContext(ctx, `UPDATE zones SET name = 'lab-renamed', updated_at = ? WHERE id = 'z1'`, base.Add(90*time.Second))
	require.NoError(t, err, "non-status edits must not create another alert event")
	_, err = db.DB().ExecContext(ctx, `UPDATE zones SET status = 'connected', updated_at = ? WHERE id = 'z1'`, base.Add(2*time.Minute))
	require.NoError(t, err)

	_, err = db.DB().ExecContext(ctx, `INSERT INTO qubes (
		id, name, type, zone_id, status, spec, agent_health, agent_last_probed_at, created_at, updated_at
	) VALUES ('q1', 'work-vm', 'work', 'z1', 'running', '{}', 'unreachable', ?, ?, ?)`,
		base.Add(3*time.Minute), base, base)
	require.NoError(t, err)
	_, err = db.DB().ExecContext(ctx, `UPDATE qubes SET agent_health = 'starting', agent_last_probed_at = ? WHERE id = 'q1'`, base.Add(4*time.Minute))
	require.NoError(t, err)
	_, err = db.DB().ExecContext(ctx, `UPDATE qubes SET agent_health = 'unreachable', agent_last_probed_at = ? WHERE id = 'q1'`, base.Add(5*time.Minute))
	require.NoError(t, err)
	_, err = db.DB().ExecContext(ctx, `DELETE FROM qubes WHERE id = 'q1'`)
	require.NoError(t, err)

	repo := NewMonitoringAlertEventRepository(db)
	events, total, err := repo.List(ctx, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 6, total)
	require.Len(t, events, 6)
	require.Equal(t, "resolved", events[0].EventType)
	require.Equal(t, "agent-unreachable:q1", events[0].AlertID)
	require.Equal(t, "agent-health", events[0].Source)
	require.Equal(t, "raised", events[1].EventType)
	require.Equal(t, "resolved", events[2].EventType)
	require.Equal(t, "raised", events[3].EventType)
	require.Equal(t, "resolved", events[4].EventType)
	require.Equal(t, "raised", events[5].EventType)

	page, pageTotal, err := repo.List(ctx, 2, 1)
	require.NoError(t, err)
	require.Equal(t, total, pageTotal)
	require.Len(t, page, 2)
	require.Equal(t, events[1:3], page)
}

func TestMonitoringAlertWebhookOutboxQueuesOnlyWhenEnabled(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	ctx := context.Background()
	_, err := db.DB().Exec(`INSERT INTO settings (key, value, updated_at)
		VALUES ('notifications', '{"webhook":true,"webhookUrl":"https://hooks.example.test/alerts"}', CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.DB().Exec(`INSERT INTO zones (id,name,type,status,created_at,updated_at)
		VALUES ('z-enabled','zone','proxmox','disconnected',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	repo := NewMonitoringAlertWebhookRepository(db)
	deliveries, err := repo.ListDue(ctx, 10)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.Equal(t, "zone-disconnected:z-enabled", deliveries[0].Event.AlertID)

	_, err = db.DB().Exec(`UPDATE settings SET value = '{"webhook":false,"webhookUrl":"https://hooks.example.test/alerts"}' WHERE key='notifications'`)
	require.NoError(t, err)
	_, err = db.DB().Exec(`INSERT INTO zones (id,name,type,status,created_at,updated_at)
		VALUES ('z-disabled','zone','proxmox','disconnected',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	deliveries, err = repo.ListDue(ctx, 10)
	require.NoError(t, err)
	require.Len(t, deliveries, 1, "events raised while delivery is disabled must not be queued for later surprise delivery")
}

func TestMonitoringAlertWebhookOutboxPersistsRetryAndCompletion(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	ctx := context.Background()
	_, err := db.DB().Exec(`INSERT INTO settings (key, value, updated_at)
		VALUES ('notifications', '{"webhook":true}', CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.DB().Exec(`INSERT INTO zones (id,name,type,status,created_at,updated_at)
		VALUES ('z-retry','zone','proxmox','disconnected',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	repo := NewMonitoringAlertWebhookRepository(db)
	deliveries, err := repo.ListDue(ctx, 10)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	retryAt := time.Now().UTC().Add(time.Hour)
	require.NoError(t, repo.MarkFailed(ctx, deliveries[0].ID, retryAt, "endpoint returned HTTP 503"))
	deliveries, err = repo.ListDue(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, deliveries, "future retry must stay out of the due batch")
	_, err = db.DB().Exec(`UPDATE monitoring_alert_webhook_outbox SET next_attempt_at = datetime('now', '-1 second')`)
	require.NoError(t, err)
	deliveries, err = repo.ListDue(ctx, 10)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.Equal(t, 1, deliveries[0].Attempts)
	require.NoError(t, repo.MarkDelivered(ctx, deliveries[0].ID))
	deliveries, err = repo.ListDue(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, deliveries)
	_, err = db.DB().Exec(`UPDATE monitoring_alert_webhook_outbox SET delivered_at = datetime('now', '-100 days')`)
	require.NoError(t, err)
	removed, err := repo.PruneDelivered(ctx, time.Now().UTC().Add(-90*24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, removed)
}
