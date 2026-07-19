// Package repository provides data access for qubes.
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
)

// QubeRepository defines qube data access operations.
type QubeRepository interface {
	Create(ctx context.Context, qube *models.Qube) error
	GetByID(ctx context.Context, id string) (*models.Qube, error)
	List(ctx context.Context, opts QubeListOptions) ([]*models.Qube, error)
	Update(ctx context.Context, qube *models.Qube) error
	Delete(ctx context.Context, id string) error
	UpdateStatus(ctx context.Context, id string, status models.QubeStatus) error
	UpdateIPAddress(ctx context.Context, id, ipAddress string) error
	// ClaimTransition atomically moves a qube into a new status only if it is
	// currently in one of `from`, returning ErrTransitionConflict otherwise.
	// This is what serializes mutating operations on a single qube.
	ClaimTransition(ctx context.Context, id string, from []models.QubeStatus, to models.QubeStatus) error
	// ListByStatus returns every qube in one of the given statuses.
	ListByStatus(ctx context.Context, statuses []models.QubeStatus) ([]*models.Qube, error)
}

// QubeListOptions contains filtering options for listing qubes.
type QubeListOptions struct {
	ZoneID string
	Status string
	Type   string
	Limit  int
	Offset int
}

// DefaultQubeListOptions returns default list options.
func DefaultQubeListOptions() QubeListOptions {
	return QubeListOptions{
		Limit:  100,
		Offset: 0,
	}
}

// qubeRepository implements QubeRepository.
type qubeRepository struct {
	db *database.DB
}

// NewQubeRepository creates a new QubeRepository.
func NewQubeRepository(db *database.DB) QubeRepository {
	return &qubeRepository{db: db}
}

// Create inserts a new qube.
func (r *qubeRepository) Create(ctx context.Context, qube *models.Qube) error {
	specJSON, err := json.Marshal(qube.Spec)
	if err != nil {
		return err
	}

	query := `
		INSERT INTO qubes (id, name, type, zone_id, status, spec, ip_address, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err = r.db.DB().ExecContext(ctx, query,
		qube.ID,
		qube.Name,
		qube.Type,
		qube.ZoneID,
		qube.Status,
		specJSON,
		qube.IPAddress,
		qube.CreatedAt,
		qube.UpdatedAt,
	)

	return err
}

// GetByID retrieves a qube by ID.
func (r *qubeRepository) GetByID(ctx context.Context, id string) (*models.Qube, error) {
	query := `
		SELECT id, name, type, zone_id, status, spec, ip_address, created_at, updated_at
		FROM qubes WHERE id = ?`

	qube := &models.Qube{}
	var specJSON []byte

	err := r.db.DB().QueryRowContext(ctx, query, id).Scan(
		&qube.ID,
		&qube.Name,
		&qube.Type,
		&qube.ZoneID,
		&qube.Status,
		&specJSON,
		&qube.IPAddress,
		&qube.CreatedAt,
		&qube.UpdatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("qube not found")
	}
	if err != nil {
		return nil, err
	}

	if len(specJSON) > 0 {
		if err := json.Unmarshal(specJSON, &qube.Spec); err != nil {
			return nil, err
		}
	}

	return qube, nil
}

// List retrieves qubes with filtering.
func (r *qubeRepository) List(ctx context.Context, opts QubeListOptions) ([]*models.Qube, error) {
	query, args := buildQubeListQuery(opts)

	rows, err := r.db.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanQubes(rows)
}

// buildQubeListQuery constructs the list query with filters.
func buildQubeListQuery(opts QubeListOptions) (string, []interface{}) {
	query := `
		SELECT id, name, type, zone_id, status, spec, ip_address, created_at, updated_at
		FROM qubes WHERE 1=1`
	args := []interface{}{}

	if opts.ZoneID != "" {
		query += " AND zone_id = ?"
		args = append(args, opts.ZoneID)
	}

	if opts.Status != "" {
		query += " AND status = ?"
		args = append(args, opts.Status)
	}

	if opts.Type != "" {
		query += " AND type = ?"
		args = append(args, opts.Type)
	}

	query += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, opts.Limit, opts.Offset)

	return query, args
}

// scanQubes scans rows into qube slice.
func scanQubes(rows *sql.Rows) ([]*models.Qube, error) {
	var qubes []*models.Qube

	for rows.Next() {
		qube := &models.Qube{}
		var specJSON []byte

		err := rows.Scan(
			&qube.ID,
			&qube.Name,
			&qube.Type,
			&qube.ZoneID,
			&qube.Status,
			&specJSON,
			&qube.IPAddress,
			&qube.CreatedAt,
			&qube.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if len(specJSON) > 0 {
			if err := json.Unmarshal(specJSON, &qube.Spec); err != nil {
				return nil, err
			}
		}

		qubes = append(qubes, qube)
	}

	return qubes, rows.Err()
}

// Update updates an existing qube.
func (r *qubeRepository) Update(ctx context.Context, qube *models.Qube) error {
	specJSON, err := json.Marshal(qube.Spec)
	if err != nil {
		return err
	}

	query := `
		UPDATE qubes
		SET name = ?, type = ?, status = ?, spec = ?, ip_address = ?, updated_at = ?
		WHERE id = ?`

	_, err = r.db.DB().ExecContext(ctx, query,
		qube.Name,
		qube.Type,
		qube.Status,
		specJSON,
		qube.IPAddress,
		time.Now(),
		qube.ID,
	)

	return err
}

// Delete removes a qube.
func (r *qubeRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM qubes WHERE id = ?`
	_, err := r.db.DB().ExecContext(ctx, query, id)
	return err
}

// UpdateStatus updates qube status.
func (r *qubeRepository) UpdateStatus(ctx context.Context, id string, status models.QubeStatus) error {
	query := `UPDATE qubes SET status = ?, updated_at = ? WHERE id = ?`
	_, err := r.db.DB().ExecContext(ctx, query, status, time.Now(), id)
	return err
}

// ErrTransitionConflict means the qube was not in any of the expected source
// statuses. In practice that almost always means an operation is already in
// flight for it.
var ErrTransitionConflict = errors.New("qube is busy: another operation is in progress")

// ClaimTransition atomically moves a qube into a new status, but only from one
// of the statuses in from. It reports ErrTransitionConflict if the qube was in
// none of them.
//
// This is the concurrency guard for every mutating qube endpoint. Two
// simultaneous Start requests issue two UPDATEs; the first matches a row and
// the second affects zero, so exactly one job is ever enqueued. Expressing this
// as a read-then-write in Go would leave a window between the check and the
// write — and since a terraform apply takes minutes, that window is wide enough
// to matter in practice, not just in theory.
func (r *qubeRepository) ClaimTransition(
	ctx context.Context, id string, from []models.QubeStatus, to models.QubeStatus,
) error {
	if len(from) == 0 {
		return errors.New("ClaimTransition: no source statuses given")
	}

	placeholders := make([]string, len(from))
	args := make([]any, 0, len(from)+3)
	args = append(args, string(to), time.Now(), id)
	for i, s := range from {
		placeholders[i] = "?"
		args = append(args, string(s))
	}

	// #nosec G201 -- only "?" placeholders are interpolated into the SQL; every
	// value, including each source status, is passed as a bound argument.
	query := fmt.Sprintf(
		`UPDATE qubes SET status = ?, updated_at = ? WHERE id = ? AND status IN (%s)`,
		strings.Join(placeholders, ","),
	)

	res, err := r.db.DB().ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTransitionConflict
	}
	return nil
}

// ListByStatus returns every qube currently in one of the given statuses.
// Startup reconciliation uses it to find qubes stranded in a transient status
// by a process that died mid-operation.
func (r *qubeRepository) ListByStatus(ctx context.Context, statuses []models.QubeStatus) ([]*models.Qube, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, 0, len(statuses))
	for i, s := range statuses {
		placeholders[i] = "?"
		args = append(args, string(s))
	}
	// #nosec G201 -- placeholders only; statuses are bound arguments.
	query := fmt.Sprintf(`
		SELECT id, name, type, zone_id, status, spec, ip_address, created_at, updated_at
		FROM qubes WHERE status IN (%s)`, strings.Join(placeholders, ","))

	rows, err := r.db.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return scanQubes(rows)
}

// UpdateIPAddress updates qube IP address.
func (r *qubeRepository) UpdateIPAddress(ctx context.Context, id, ipAddress string) error {
	query := `UPDATE qubes SET ip_address = ?, updated_at = ? WHERE id = ?`
	_, err := r.db.DB().ExecContext(ctx, query, ipAddress, time.Now(), id)
	return err
}
