package service

import (
	"context"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// SettingsService handles settings business logic.
type SettingsService struct {
	repo *repository.SettingsRepository
}

// NewSettingsService creates a new settings service.
func NewSettingsService(repo *repository.SettingsRepository) *SettingsService {
	return &SettingsService{repo: repo}
}

// Get returns all settings.
func (s *SettingsService) Get(ctx context.Context) (*models.Settings, error) {
	return s.repo.Get(ctx)
}

// Update updates settings.
func (s *SettingsService) Update(ctx context.Context, settings *models.Settings) error {
	return s.repo.Update(ctx, settings)
}
