// Package repository provides data access for qubes.
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// UpdateIPAddress updates qube IP address.
func (r *qubeRepository) UpdateIPAddress(ctx context.Context, id, ipAddress string) error {
	query := `UPDATE qubes SET ip_address = ?, updated_at = ? WHERE id = ?`
	_, err := r.db.DB().ExecContext(ctx, query, ipAddress, time.Now(), id)
	return err
}
