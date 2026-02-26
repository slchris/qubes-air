// Package service provides business logic for zone management.
package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// Service errors.
var (
	ErrZoneNotFound    = errors.New("zone not found")
	ErrZoneInUse       = errors.New("zone is in use by qubes")
	ErrInvalidZoneType = errors.New("invalid zone type")
)

// ZoneService defines zone business logic operations.
type ZoneService interface { //nolint:dupl
	Create(ctx context.Context, req *models.ZoneCreateRequest) (*models.Zone, error)
	GetByID(ctx context.Context, id string) (*models.Zone, error)
	List(ctx context.Context, opts repository.ZoneListOptions) ([]*models.Zone, error)
	Update(ctx context.Context, id string, req *models.ZoneUpdateRequest) (*models.Zone, error)
	Delete(ctx context.Context, id string) error
	Connect(ctx context.Context, id string) (*models.Zone, error)
	Disconnect(ctx context.Context, id string) (*models.Zone, error)
}

// ZoneServiceImpl implements ZoneService.
type ZoneServiceImpl struct {
	zoneRepo repository.ZoneRepository
	qubeRepo repository.QubeRepository
}

// NewZoneService creates a new ZoneService.
func NewZoneService(zoneRepo repository.ZoneRepository, qubeRepo repository.QubeRepository) ZoneService {
	return &ZoneServiceImpl{
		zoneRepo: zoneRepo,
		qubeRepo: qubeRepo,
	}
}

// Create creates a new zone.
func (s *ZoneServiceImpl) Create(ctx context.Context, req *models.ZoneCreateRequest) (*models.Zone, error) {
	if err := validateZoneCreateRequest(req); err != nil {
		return nil, err
	}

	zone := &models.Zone{
		ID:        uuid.New().String(),
		Name:      strings.TrimSpace(req.Name),
		Type:      req.Type,
		Status:    "disconnected",
		Config:    req.Config,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := s.zoneRepo.Create(ctx, zone); err != nil {
		return nil, err
	}

	return zone, nil
}

// validateZoneCreateRequest validates zone creation request.
func validateZoneCreateRequest(req *models.ZoneCreateRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("zone name is required")
	}

	if !req.Type.IsValid() {
		return ErrInvalidZoneType
	}

	return nil
}

// GetByID retrieves a zone by ID.
func (s *ZoneServiceImpl) GetByID(ctx context.Context, id string) (*models.Zone, error) {
	zone, err := s.zoneRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrZoneNotFound
	}
	return zone, nil
}

// List retrieves all zones with optional filtering.
func (s *ZoneServiceImpl) List(ctx context.Context, opts repository.ZoneListOptions) ([]*models.Zone, error) {
	return s.zoneRepo.List(ctx, opts)
}

// Update updates an existing zone.
func (s *ZoneServiceImpl) Update(ctx context.Context, id string, req *models.ZoneUpdateRequest) (*models.Zone, error) {
	zone, err := s.zoneRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrZoneNotFound
	}

	applyZoneUpdates(zone, req)
	zone.UpdatedAt = time.Now()

	if err := s.zoneRepo.Update(ctx, zone); err != nil {
		return nil, err
	}

	return zone, nil
}

// applyZoneUpdates applies update request fields to zone.
func applyZoneUpdates(zone *models.Zone, req *models.ZoneUpdateRequest) {
	if req.Name != nil {
		zone.Name = strings.TrimSpace(*req.Name)
	}
	if req.Config != nil {
		zone.Config = *req.Config
	}
}

// Delete removes a zone if not in use.
func (s *ZoneServiceImpl) Delete(ctx context.Context, id string) error {
	if _, err := s.zoneRepo.GetByID(ctx, id); err != nil {
		return ErrZoneNotFound
	}

	if err := s.checkZoneInUse(ctx, id); err != nil {
		return err
	}

	return s.zoneRepo.Delete(ctx, id)
}

// checkZoneInUse verifies no qubes are using the zone.
func (s *ZoneServiceImpl) checkZoneInUse(ctx context.Context, zoneID string) error {
	opts := repository.DefaultQubeListOptions()
	opts.ZoneID = zoneID

	qubes, err := s.qubeRepo.List(ctx, opts)
	if err != nil {
		return err
	}

	if len(qubes) > 0 {
		return ErrZoneInUse
	}

	return nil
}

// Connect establishes connection to the zone.
func (s *ZoneServiceImpl) Connect(ctx context.Context, id string) (*models.Zone, error) {
	if _, err := s.zoneRepo.GetByID(ctx, id); err != nil {
		return nil, ErrZoneNotFound
	}

	if err := s.zoneRepo.UpdateStatus(ctx, id, "connected"); err != nil {
		return nil, err
	}

	return s.zoneRepo.GetByID(ctx, id)
}

// Disconnect closes connection to the zone.
func (s *ZoneServiceImpl) Disconnect(ctx context.Context, id string) (*models.Zone, error) {
	if _, err := s.zoneRepo.GetByID(ctx, id); err != nil {
		return nil, ErrZoneNotFound
	}

	if err := s.zoneRepo.UpdateStatus(ctx, id, "disconnected"); err != nil {
		return nil, err
	}

	return s.zoneRepo.GetByID(ctx, id)
}
