// Package service provides business logic for zone management.
package service

import (
	"context"
	"errors"
	"fmt"
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
	// ErrZoneTypeNotImplemented refuses a zone type the model names but no
	// registered provider adapter serves. Accepting such a zone would only
	// postpone the failure to the first provision job (provider.ErrNoAdapter),
	// after the operator had already built credentials and qubes around it.
	ErrZoneTypeNotImplemented = errors.New("provider not implemented")
)

// ZoneTypeSupport reports whether this console can provision into a zone
// type. *provider.Registry satisfies it; the interface keeps this package from
// depending on the provider package while still asking the one registry the
// executor dispatches through, rather than a second hard-coded list.
type ZoneTypeSupport interface {
	Has(zoneType models.ZoneType) bool
}

// ZoneService defines zone business logic operations.
type ZoneService interface {
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
	// adapters decides which zone types may be created. Nil admits none.
	adapters ZoneTypeSupport
}

// NewZoneService creates a new ZoneService.
//
// adapters is consulted on create only. A nil value fails closed — every
// create is refused — because the alternative, admitting every valid type, is
// exactly the silent acceptance the check exists to remove.
func NewZoneService(zoneRepo repository.ZoneRepository, qubeRepo repository.QubeRepository, adapters ZoneTypeSupport) ZoneService {
	return &ZoneServiceImpl{
		zoneRepo: zoneRepo,
		qubeRepo: qubeRepo,
		adapters: adapters,
	}
}

// Create creates a new zone.
func (s *ZoneServiceImpl) Create(ctx context.Context, req *models.ZoneCreateRequest) (*models.Zone, error) {
	if err := validateZoneCreateRequest(req); err != nil {
		return nil, err
	}
	if err := s.requireAdapter(req.Type); err != nil {
		return nil, err
	}

	zone := &models.Zone{
		ID:        uuid.New().String(),
		Name:      strings.TrimSpace(req.Name),
		Type:      req.Type,
		Status:    models.ZoneStatusDisconnected,
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

// requireAdapter refuses a zone type that no registered adapter serves.
//
// It runs on create only. Rows of such a type written before this check
// existed must stay readable, renamable and deletable — refusing to load them
// would strand them in the database — so reads, updates and deletes never
// consult it. A zone's type cannot change after creation (ZoneUpdateRequest
// has no type field), so create is the only API path by which a type enters
// the table.
func (s *ZoneServiceImpl) requireAdapter(zoneType models.ZoneType) error {
	if s.adapters == nil || !s.adapters.Has(zoneType) {
		return fmt.Errorf("%w: zone type %q has no provider adapter registered in this console, "+
			"so nothing could be provisioned into it", ErrZoneTypeNotImplemented, zoneType)
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

	if err := s.zoneRepo.UpdateStatus(ctx, id, models.ZoneStatusConnected); err != nil {
		return nil, err
	}

	return s.zoneRepo.GetByID(ctx, id)
}

// Disconnect closes connection to the zone.
func (s *ZoneServiceImpl) Disconnect(ctx context.Context, id string) (*models.Zone, error) {
	if _, err := s.zoneRepo.GetByID(ctx, id); err != nil {
		return nil, ErrZoneNotFound
	}

	if err := s.zoneRepo.UpdateStatus(ctx, id, models.ZoneStatusDisconnected); err != nil {
		return nil, err
	}

	return s.zoneRepo.GetByID(ctx, id)
}
