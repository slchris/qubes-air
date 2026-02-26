package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
)

// InfraRepository handles infrastructure data access.
type InfraRepository struct {
	db *database.DB
}

// NewInfraRepository creates a new InfraRepository.
func NewInfraRepository(db *database.DB) *InfraRepository {
	return &InfraRepository{db: db}
}

// List returns all infrastructure providers.
func (r *InfraRepository) List(ctx context.Context) ([]models.InfraProvider, error) {
	query := `SELECT id, name, type, status, region, config, resource_count, created_at, updated_at FROM infrastructure ORDER BY created_at DESC`

	rows, err := r.db.DB().QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []models.InfraProvider
	for rows.Next() {
		var p models.InfraProvider
		var configJSON string
		err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.Status, &p.Region, &configJSON, &p.ResourceCount, &p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(configJSON), &p.Config); err != nil {
			p.Config = models.InfraConfig{}
		}
		providers = append(providers, p)
	}

	return providers, rows.Err()
}

// GetByID returns an infrastructure provider by ID.
func (r *InfraRepository) GetByID(ctx context.Context, id string) (*models.InfraProvider, error) {
	query := `SELECT id, name, type, status, region, config, resource_count, created_at, updated_at FROM infrastructure WHERE id = ?`

	var p models.InfraProvider
	var configJSON string
	err := r.db.DB().QueryRowContext(ctx, query, id).Scan(&p.ID, &p.Name, &p.Type, &p.Status, &p.Region, &configJSON, &p.ResourceCount, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(configJSON), &p.Config); err != nil {
		p.Config = models.InfraConfig{}
	}

	return &p, nil
}

// Create creates a new infrastructure provider.
func (r *InfraRepository) Create(ctx context.Context, req *models.InfraCreateRequest) (*models.InfraProvider, error) {
	now := time.Now().UTC()
	p := &models.InfraProvider{
		ID:            uuid.New().String(),
		Name:          req.Name,
		Type:          req.Type,
		Status:        "disconnected",
		Region:        req.Region,
		Config:        req.Config,
		ResourceCount: 0,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	configJSON, err := json.Marshal(p.Config)
	if err != nil {
		return nil, err
	}

	query := `INSERT INTO infrastructure (id, name, type, status, region, config, resource_count, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = r.db.DB().ExecContext(ctx, query, p.ID, p.Name, p.Type, p.Status, p.Region, string(configJSON), p.ResourceCount, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return p, nil
}

// Update updates an infrastructure provider.
func (r *InfraRepository) Update(ctx context.Context, id string, req *models.InfraUpdateRequest) (*models.InfraProvider, error) {
	existing, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Region != nil {
		existing.Region = *req.Region
	}
	if req.Config != nil {
		existing.Config = *req.Config
	}
	existing.UpdatedAt = time.Now().UTC()

	configJSON, err := json.Marshal(existing.Config)
	if err != nil {
		return nil, err
	}

	query := `UPDATE infrastructure SET name = ?, region = ?, config = ?, updated_at = ? WHERE id = ?`
	_, err = r.db.DB().ExecContext(ctx, query, existing.Name, existing.Region, string(configJSON), existing.UpdatedAt, id)
	if err != nil {
		return nil, err
	}

	return existing, nil
}

// UpdateStatus updates the status of an infrastructure provider.
func (r *InfraRepository) UpdateStatus(ctx context.Context, id, status string) error {
	query := `UPDATE infrastructure SET status = ?, updated_at = ? WHERE id = ?`
	_, err := r.db.DB().ExecContext(ctx, query, status, time.Now().UTC(), id)
	return err
}

// Delete deletes an infrastructure provider.
func (r *InfraRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM infrastructure WHERE id = ?`
	_, err := r.db.DB().ExecContext(ctx, query, id)
	return err
}
