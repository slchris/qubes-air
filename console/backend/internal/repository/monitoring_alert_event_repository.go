package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
)

// MonitoringAlertEvent is one durable transition into or out of an alert state.
type MonitoringAlertEvent struct {
	ID         int64     `json:"id"`
	AlertID    string    `json:"alert_id"`
	ResourceID string    `json:"resource_id"`
	EventType  string    `json:"event_type"`
	Severity   string    `json:"severity"`
	Message    string    `json:"message"`
	Source     string    `json:"source"`
	OccurredAt time.Time `json:"occurred_at"`
}

// MonitoringAlertEventRepository reads transition events captured by SQLite triggers.
type MonitoringAlertEventRepository struct {
	db *database.DB
}

func NewMonitoringAlertEventRepository(db *database.DB) *MonitoringAlertEventRepository {
	return &MonitoringAlertEventRepository{db: db}
}

// List returns newest events first and the total count from one SQLite snapshot.
func (r *MonitoringAlertEventRepository) List(
	ctx context.Context, limit, offset int,
) ([]MonitoringAlertEvent, int, error) {
	tx, err := r.db.DB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM monitoring_alert_events`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, alert_id, resource_id, event_type, severity, message, source, occurred_at
		FROM monitoring_alert_events ORDER BY occurred_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	events := make([]MonitoringAlertEvent, 0)
	for rows.Next() {
		var event MonitoringAlertEvent
		if err := rows.Scan(&event.ID, &event.AlertID, &event.ResourceID, &event.EventType,
			&event.Severity, &event.Message, &event.Source, &event.OccurredAt); err != nil {
			return nil, 0, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return events, total, nil
}
