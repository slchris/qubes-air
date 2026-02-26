package service

import (
	"context"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// InfraService handles infrastructure business logic.
type InfraService struct {
	repo *repository.InfraRepository
}

// NewInfraService creates a new InfraService.
func NewInfraService(repo *repository.InfraRepository) *InfraService {
	return &InfraService{repo: repo}
}

// List returns all infrastructure providers.
func (s *InfraService) List(ctx context.Context) ([]models.InfraProvider, error) {
	return s.repo.List(ctx)
}

// Get returns an infrastructure provider by ID.
func (s *InfraService) Get(ctx context.Context, id string) (*models.InfraProvider, error) {
	return s.repo.GetByID(ctx, id)
}

// Create creates a new infrastructure provider.
func (s *InfraService) Create(ctx context.Context, req *models.InfraCreateRequest) (*models.InfraProvider, error) {
	return s.repo.Create(ctx, req)
}

// Update updates an infrastructure provider.
func (s *InfraService) Update(ctx context.Context, id string, req *models.InfraUpdateRequest) (*models.InfraProvider, error) {
	return s.repo.Update(ctx, id, req)
}

// Delete deletes an infrastructure provider.
func (s *InfraService) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// Connect connects to an infrastructure provider.
func (s *InfraService) Connect(ctx context.Context, id string) (*models.InfraProvider, error) {
	if err := s.repo.UpdateStatus(ctx, id, "connected"); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, id)
}

// Disconnect disconnects from an infrastructure provider.
func (s *InfraService) Disconnect(ctx context.Context, id string) (*models.InfraProvider, error) {
	if err := s.repo.UpdateStatus(ctx, id, "disconnected"); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, id)
}
