package repository

import (
	"context"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
)

// MonitoringAlertWebhookDelivery is one pending or completed notification.
type MonitoringAlertWebhookDelivery struct {
	ID       int64
	Event    MonitoringAlertEvent
	Attempts int
}

// MonitoringAlertWebhookRepository owns the durable webhook outbox.
type MonitoringAlertWebhookRepository struct {
	db *database.DB
}

func NewMonitoringAlertWebhookRepository(db *database.DB) *MonitoringAlertWebhookRepository {
	return &MonitoringAlertWebhookRepository{db: db}
}

// ListDue returns queued events whose retry time has arrived.
func (r *MonitoringAlertWebhookRepository) ListDue(ctx context.Context, limit int) ([]MonitoringAlertWebhookDelivery, error) {
	rows, err := r.db.DB().QueryContext(ctx, `
		SELECT d.id, d.attempts,
			e.id, e.alert_id, e.resource_id, e.event_type, e.severity, e.message, e.source, e.occurred_at
		FROM monitoring_alert_webhook_outbox d
		JOIN monitoring_alert_events e ON e.id = d.event_id
		WHERE d.delivered_at IS NULL AND d.next_attempt_at <= CURRENT_TIMESTAMP
		ORDER BY d.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := make([]MonitoringAlertWebhookDelivery, 0)
	for rows.Next() {
		var delivery MonitoringAlertWebhookDelivery
		e := &delivery.Event
		if err := rows.Scan(&delivery.ID, &delivery.Attempts,
			&e.ID, &e.AlertID, &e.ResourceID, &e.EventType, &e.Severity, &e.Message, &e.Source, &e.OccurredAt); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

// MarkDelivered completes an outbox item after a successful HTTP response.
func (r *MonitoringAlertWebhookRepository) MarkDelivered(ctx context.Context, id int64) error {
	_, err := r.db.DB().ExecContext(ctx, `UPDATE monitoring_alert_webhook_outbox
		SET delivered_at = CURRENT_TIMESTAMP, last_error = '' WHERE id = ? AND delivered_at IS NULL`, id)
	return err
}

// MarkFailed schedules a retry and stores a sanitized error description.
func (r *MonitoringAlertWebhookRepository) MarkFailed(ctx context.Context, id int64, next time.Time, description string) error {
	_, err := r.db.DB().ExecContext(ctx, `UPDATE monitoring_alert_webhook_outbox
		SET attempts = attempts + 1, next_attempt_at = ?, last_error = ?
		WHERE id = ? AND delivered_at IS NULL`, next.UTC(), description, id)
	return err
}

// PruneDelivered removes completed deliveries older than the retention window.
func (r *MonitoringAlertWebhookRepository) PruneDelivered(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.db.DB().ExecContext(ctx, `DELETE FROM monitoring_alert_webhook_outbox
		WHERE delivered_at IS NOT NULL AND delivered_at < ?`, before.UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
