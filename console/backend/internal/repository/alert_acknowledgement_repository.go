package repository

import (
	"context"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
)

// AlertAcknowledgement is the operator and time associated with an active alert acknowledgement.
type AlertAcknowledgement struct {
	AlertID        string    `json:"alert_id"`
	AcknowledgedBy string    `json:"acknowledged_by"`
	AcknowledgedAt time.Time `json:"acknowledged_at"`
}

// AlertAcknowledgementRepository stores acknowledgements for currently active alerts.
type AlertAcknowledgementRepository struct {
	db *database.DB
}

func NewAlertAcknowledgementRepository(db *database.DB) *AlertAcknowledgementRepository {
	return &AlertAcknowledgementRepository{db: db}
}

// SyncActive returns acknowledgements for this snapshot and removes records for resolved alerts.
func (r *AlertAcknowledgementRepository) SyncActive(ctx context.Context, activeIDs []string) (map[string]AlertAcknowledgement, error) {
	active := make(map[string]struct{}, len(activeIDs))
	for _, id := range activeIDs {
		active[id] = struct{}{}
	}
	tx, err := r.db.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT alert_id, acknowledged_by, acknowledged_at FROM alert_acknowledgements`)
	if err != nil {
		return nil, err
	}
	result := make(map[string]AlertAcknowledgement)
	var stale []string
	for rows.Next() {
		var record AlertAcknowledgement
		if err := rows.Scan(&record.AlertID, &record.AcknowledgedBy, &record.AcknowledgedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if _, ok := active[record.AlertID]; ok {
			result[record.AlertID] = record
		} else {
			stale = append(stale, record.AlertID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM alert_acknowledgements WHERE alert_id = ?`, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// Acknowledge idempotently records the current active alert state.
func (r *AlertAcknowledgementRepository) Acknowledge(ctx context.Context, alertID, subject string, at time.Time) error {
	_, err := r.db.DB().ExecContext(ctx, `
		INSERT INTO alert_acknowledgements (alert_id, acknowledged_by, acknowledged_at)
		VALUES (?, ?, ?)
		ON CONFLICT(alert_id) DO UPDATE SET acknowledged_by = excluded.acknowledged_by,
		acknowledged_at = excluded.acknowledged_at`, alertID, subject, at.UTC())
	return err
}
