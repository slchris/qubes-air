// Package service provides business logic for qube management.
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

// Qube service errors.
var (
	ErrQubeNotFound     = errors.New("qube not found")
	ErrQubeNotStopped   = errors.New("qube must be stopped")
	ErrZoneDisconnected = errors.New("zone is disconnected")
	ErrInvalidQubeType  = errors.New("invalid qube type")
)

// QubeService defines qube business logic operations.
type QubeService interface {
	Create(ctx context.Context, req *models.QubeCreateRequest) (*models.Qube, error)
	GetByID(ctx context.Context, id string) (*models.Qube, error)
	List(ctx context.Context, opts repository.QubeListOptions) ([]*models.Qube, error)
	Update(ctx context.Context, id string, req *models.QubeUpdateRequest) (*models.Qube, error)
	Delete(ctx context.Context, id string) error
	Start(ctx context.Context, id string) (*models.Qube, error)
	Stop(ctx context.Context, id string) (*models.Qube, error)
}

// QubeServiceImpl implements QubeService.
type QubeServiceImpl struct {
	qubeRepo repository.QubeRepository
	zoneRepo repository.ZoneRepository
}

// NewQubeService creates a new QubeService.
func NewQubeService(qubeRepo repository.QubeRepository, zoneRepo repository.ZoneRepository) QubeService { //nolint:dupl
	return &QubeServiceImpl{
		qubeRepo: qubeRepo,
		zoneRepo: zoneRepo,
	}
}

// Create creates a new qube.
func (s *QubeServiceImpl) Create(ctx context.Context, req *models.QubeCreateRequest) (*models.Qube, error) {
	if err := s.validateQubeCreateRequest(ctx, req); err != nil {
		return nil, err
	}

	qube := buildNewQube(req)
	applyDefaultSpec(qube)

	if err := s.qubeRepo.Create(ctx, qube); err != nil {
		return nil, err
	}

	return qube, nil
}

// validateQubeCreateRequest validates qube creation request.
func (s *QubeServiceImpl) validateQubeCreateRequest(ctx context.Context, req *models.QubeCreateRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("qube name is required")
	}

	if !req.Type.IsValid() {
		return ErrInvalidQubeType
	}

	// Zone is optional - only validate if provided
	if req.ZoneID != "" {
		if _, err := s.zoneRepo.GetByID(ctx, req.ZoneID); err != nil {
			return ErrZoneNotFound
		}
	}

	return nil
}

// buildNewQube constructs a new Qube from the request.
func buildNewQube(req *models.QubeCreateRequest) *models.Qube {
	return &models.Qube{
		ID:        uuid.New().String(),
		Name:      strings.TrimSpace(req.Name),
		Type:      req.Type,
		ZoneID:    req.ZoneID,
		Status:    models.QubeStatusStopped,
		Spec:      req.Spec,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// applyDefaultSpec applies type-based default specifications.
func applyDefaultSpec(qube *models.Qube) {
	if qube.Spec.VCPU == 0 {
		qube.Spec.VCPU = getDefaultVCPU(qube.Type)
	}
	if qube.Spec.Memory == 0 {
		qube.Spec.Memory = getDefaultMemory(qube.Type)
	}
	if qube.Spec.Disk == 0 {
		qube.Spec.Disk = getDefaultDisk(qube.Type)
	}
}

// getDefaultVCPU returns default vCPU count by qube type.
func getDefaultVCPU(qubeType models.QubeType) int {
	switch qubeType {
	case models.QubeTypeApp:
		return 2
	case models.QubeTypeWork:
		return 4
	case models.QubeTypeGPU:
		return 8
	default:
		return 2
	}
}

// getDefaultMemory returns default memory in MB by qube type.
func getDefaultMemory(qubeType models.QubeType) int {
	switch qubeType {
	case models.QubeTypeApp:
		return 2048
	case models.QubeTypeWork:
		return 4096
	case models.QubeTypeGPU:
		return 16384
	default:
		return 2048
	}
}

// getDefaultDisk returns default disk in GB by qube type.
func getDefaultDisk(qubeType models.QubeType) int {
	switch qubeType {
	case models.QubeTypeApp:
		return 20
	case models.QubeTypeWork:
		return 50
	case models.QubeTypeGPU:
		return 100
	default:
		return 20
	}
}

// GetByID retrieves a qube by ID.
func (s *QubeServiceImpl) GetByID(ctx context.Context, id string) (*models.Qube, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrQubeNotFound
	}
	return qube, nil
}

// List retrieves all qubes with optional filtering.
func (s *QubeServiceImpl) List(ctx context.Context, opts repository.QubeListOptions) ([]*models.Qube, error) {
	return s.qubeRepo.List(ctx, opts)
}

// Update updates an existing qube.
func (s *QubeServiceImpl) Update(ctx context.Context, id string, req *models.QubeUpdateRequest) (*models.Qube, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrQubeNotFound
	}

	applyQubeUpdates(qube, req)
	qube.UpdatedAt = time.Now()

	if err := s.qubeRepo.Update(ctx, qube); err != nil {
		return nil, err
	}

	return qube, nil
}

// applyQubeUpdates applies update request fields to qube.
func applyQubeUpdates(qube *models.Qube, req *models.QubeUpdateRequest) {
	if req.Name != nil {
		qube.Name = strings.TrimSpace(*req.Name)
	}
	if req.Spec != nil {
		qube.Spec = *req.Spec
	}
}

// Delete removes a qube.
func (s *QubeServiceImpl) Delete(ctx context.Context, id string) error {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return ErrQubeNotFound
	}

	if qube.Status == models.QubeStatusRunning {
		return ErrQubeNotStopped
	}

	return s.qubeRepo.Delete(ctx, id)
}

// Start starts a qube.
func (s *QubeServiceImpl) Start(ctx context.Context, id string) (*models.Qube, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrQubeNotFound
	}

	if err := s.verifyZoneConnected(ctx, qube.ZoneID); err != nil {
		return nil, err
	}

	if err := s.qubeRepo.UpdateStatus(ctx, id, models.QubeStatusRunning); err != nil {
		return nil, err
	}

	return s.qubeRepo.GetByID(ctx, id)
}

// verifyZoneConnected checks if the zone is connected.
func (s *QubeServiceImpl) verifyZoneConnected(ctx context.Context, zoneID string) error {
	zone, err := s.zoneRepo.GetByID(ctx, zoneID)
	if err != nil {
		return ErrZoneNotFound
	}

	if zone.Status != "connected" {
		return ErrZoneDisconnected
	}

	return nil
}

// Stop stops a qube.
func (s *QubeServiceImpl) Stop(ctx context.Context, id string) (*models.Qube, error) {
	if _, err := s.qubeRepo.GetByID(ctx, id); err != nil {
		return nil, ErrQubeNotFound
	}

	if err := s.qubeRepo.UpdateStatus(ctx, id, models.QubeStatusStopped); err != nil {
		return nil, err
	}

	return s.qubeRepo.GetByID(ctx, id)
}
