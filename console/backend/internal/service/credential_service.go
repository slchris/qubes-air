package service

import (
	"context"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// CredentialService handles credential business logic.
type CredentialService struct {
	repo *repository.CredentialRepository
}

// NewCredentialService creates a new credential service.
func NewCredentialService(repo *repository.CredentialRepository) *CredentialService {
	return &CredentialService{repo: repo}
}

// List returns all credentials.
func (s *CredentialService) List(ctx context.Context) ([]models.Credential, error) {
	return s.repo.List(ctx)
}

// GetByID returns a credential by ID.
func (s *CredentialService) GetByID(ctx context.Context, id string) (*models.Credential, error) {
	return s.repo.GetByID(ctx, id)
}

// GetSecret returns the decrypted secret for a credential.
func (s *CredentialService) GetSecret(ctx context.Context, id string) (string, error) {
	return s.repo.GetSecret(ctx, id)
}

// Create creates a new credential.
func (s *CredentialService) Create(ctx context.Context, req models.CredentialCreateRequest) (*models.Credential, error) {
	return s.repo.Create(ctx, req)
}

// Update updates a credential.
func (s *CredentialService) Update(ctx context.Context, id string, req models.CredentialUpdateRequest) (*models.Credential, error) {
	return s.repo.Update(ctx, id, req)
}

// Delete deletes a credential.
func (s *CredentialService) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}
