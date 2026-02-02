// Package repository provides data access for zones.
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

// ZoneRepository defines zone data access operations.
type ZoneRepository interface {
	Create(ctx context.Context, zone *models.Zone) error
	GetByID(ctx context.Context, id string) (*models.Zone, error)
	List(ctx context.Context, opts ZoneListOptions) ([]*models.Zone, error)
	Update(ctx context.Context, zone *models.Zone) error
	Delete(ctx context.Context, id string) error
	UpdateStatus(ctx context.Context, id, status string) error
}

// ZoneListOptions contains filtering options for listing zones.
type ZoneListOptions struct {
	Status string
	Type   string
	Limit  int
	Offset int
}

// DefaultZoneListOptions returns default list options.
func DefaultZoneListOptions() ZoneListOptions {
	return ZoneListOptions{
		Limit:  100,
		Offset: 0,
	}
}

// zoneRepository implements ZoneRepository.
type zoneRepository struct {
	db *database.DB
}

// NewZoneRepository creates a new ZoneRepository.
func NewZoneRepository(db *database.DB) ZoneRepository {
	return &zoneRepository{db: db}
}

// Create inserts a new zone.
func (r *zoneRepository) Create(ctx context.Context, zone *models.Zone) error {
	configJSON, err := json.Marshal(zone.Config)
	if err != nil {
		return err
	}

	query := `
		INSERT INTO zones (id, name, type, status, config, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`

	_, err = r.db.DB().ExecContext(ctx, query,
		zone.ID,
		zone.Name,
		zone.Type,
		zone.Status,
		configJSON,
		zone.CreatedAt,
		zone.UpdatedAt,
	)

	return err
}

// GetByID retrieves a zone by ID.
func (r *zoneRepository) GetByID(ctx context.Context, id string) (*models.Zone, error) {
	query := `
		SELECT id, name, type, status, config, created_at, updated_at
		FROM zones WHERE id = ?`

	zone := &models.Zone{}
	var configJSON []byte

	err := r.db.DB().QueryRowContext(ctx, query, id).Scan(
		&zone.ID,
		&zone.Name,
		&zone.Type,
		&zone.Status,
		&configJSON,
		&zone.CreatedAt,
		&zone.UpdatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("zone not found")
	}
	if err != nil {
		return nil, err
	}

	if len(configJSON) > 0 {
		if err := json.Unmarshal(configJSON, &zone.Config); err != nil {
			return nil, err
		}
	}

	return zone, nil
}

// List retrieves zones with filtering.
func (r *zoneRepository) List(ctx context.Context, opts ZoneListOptions) ([]*models.Zone, error) {
	query, args := buildZoneListQuery(opts)

	rows, err := r.db.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanZones(rows)
}

// buildZoneListQuery constructs the list query with filters.
func buildZoneListQuery(opts ZoneListOptions) (string, []interface{}) {
	query := `
		SELECT id, name, type, status, config, created_at, updated_at
		FROM zones WHERE 1=1`
	args := []interface{}{}

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

// scanZones scans rows into zone slice.
func scanZones(rows *sql.Rows) ([]*models.Zone, error) {
	var zones []*models.Zone

	for rows.Next() {
		zone := &models.Zone{}
		var configJSON []byte

		err := rows.Scan(
			&zone.ID,
			&zone.Name,
			&zone.Type,
			&zone.Status,
			&configJSON,
			&zone.CreatedAt,
			&zone.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if len(configJSON) > 0 {
			if err := json.Unmarshal(configJSON, &zone.Config); err != nil {
				return nil, err
			}
		}

		zones = append(zones, zone)
	}

	return zones, rows.Err()
}

// Update updates an existing zone.
func (r *zoneRepository) Update(ctx context.Context, zone *models.Zone) error {
	configJSON, err := json.Marshal(zone.Config)
	if err != nil {
		return err
	}

	query := `
		UPDATE zones
		SET name = ?, type = ?, status = ?, config = ?, updated_at = ?
		WHERE id = ?`

	_, err = r.db.DB().ExecContext(ctx, query,
		zone.Name,
		zone.Type,
		zone.Status,
		configJSON,
		time.Now(),
		zone.ID,
	)

	return err
}

// Delete removes a zone.
func (r *zoneRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM zones WHERE id = ?`
	_, err := r.db.DB().ExecContext(ctx, query, id)
	return err
}

// UpdateStatus updates zone status.
func (r *zoneRepository) UpdateStatus(ctx context.Context, id, status string) error {
	query := `UPDATE zones SET status = ?, updated_at = ? WHERE id = ?`
	_, err := r.db.DB().ExecContext(ctx, query, status, time.Now(), id)
	return err
}
