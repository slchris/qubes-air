package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// Bounds on the browser session timeout, in minutes. Five minutes is the
// shortest lifetime an operator can work in; a day is the longest that still
// makes the setting a security control rather than a formality.
const (
	MinSessionTimeoutMinutes = 5
	MaxSessionTimeoutMinutes = 1440
	// DefaultSessionTimeoutMinutes is the lifetime used when nothing valid is
	// stored. It must equal middleware.DefaultSessionTTL (pinned by a test in
	// cmd/server, which imports both).
	DefaultSessionTimeoutMinutes = 30
)

var (
	// ErrInvalidSessionTimeout reports a session timeout outside
	// MinSessionTimeoutMinutes..MaxSessionTimeoutMinutes.
	ErrInvalidSessionTimeout = errors.New("invalid session timeout")
	// ErrUnsupportedSetting reports an attempt to turn on a setting the console
	// does not implement (email notifications, two-factor authentication).
	// Storing it would make the settings page claim a control that does nothing.
	ErrUnsupportedSetting = errors.New("setting is not implemented")
)

// ValidateSessionTimeoutMinutes rejects a timeout too short to work in or long
// enough to undermine the setting's purpose.
func ValidateSessionTimeoutMinutes(minutes int) error {
	if minutes < MinSessionTimeoutMinutes || minutes > MaxSessionTimeoutMinutes {
		return fmt.Errorf("%w: %d minutes is outside %d-%d", ErrInvalidSessionTimeout,
			minutes, MinSessionTimeoutMinutes, MaxSessionTimeoutMinutes)
	}
	return nil
}

// SettingsService handles settings business logic.
type SettingsService struct {
	repo *repository.SettingsRepository
}

// NewSettingsService creates a new settings service.
func NewSettingsService(repo *repository.SettingsRepository) *SettingsService {
	return &SettingsService{repo: repo}
}

// Get returns the settings as they are in effect.
//
// Two stored values can disagree with what the console does, and Get reports
// the effective one instead:
//   - a session timeout outside the accepted range, which releases before the
//     range was enforced saved without complaint, reads as the default the
//     session store falls back to (see SessionTTL);
//   - email notifications and two-factor authentication are not implemented,
//     so they read as off whatever an older release stored.
//
// Nothing is written back: a read must not change state. The stored value is
// replaced the next time an operator saves settings.
func (s *SettingsService) Get(ctx context.Context) (*models.Settings, error) {
	settings, err := s.repo.Get(ctx)
	if err != nil {
		return nil, err
	}
	if ValidateSessionTimeoutMinutes(settings.Security.SessionTimeout) != nil {
		settings.Security.SessionTimeout = DefaultSessionTimeoutMinutes
	}
	settings.Notifications.Email = false
	settings.Security.TwoFactorEnabled = false
	return settings, nil
}

// Update validates and stores settings. The session timeout must be inside the
// accepted range, and settings the console does not implement cannot be turned
// on; either refusal leaves the stored settings untouched.
func (s *SettingsService) Update(ctx context.Context, settings *models.Settings) error {
	if err := ValidateSessionTimeoutMinutes(settings.Security.SessionTimeout); err != nil {
		return err
	}
	if settings.Notifications.Email {
		return fmt.Errorf("%w: email notifications", ErrUnsupportedSetting)
	}
	if settings.Security.TwoFactorEnabled {
		return fmt.Errorf("%w: two-factor authentication", ErrUnsupportedSetting)
	}
	return s.repo.Update(ctx, settings)
}

// SessionTTL returns the browser session lifetime stored in settings.
//
// An error wrapping ErrInvalidSessionTimeout means the stored value is out of
// range; any other error means the stored row does not parse. (A database read
// error never reaches here: the settings repository treats it as "nothing
// stored" and returns its defaults, which include the 30-minute timeout.) Releases before the range was enforced accepted any integer, so an
// upgraded console can find one on disk; the caller decides the fallback, and
// the console's startup uses the default with a warning rather than refusing to
// start over a preference.
func (s *SettingsService) SessionTTL(ctx context.Context) (time.Duration, error) {
	settings, err := s.repo.Get(ctx)
	if err != nil {
		return 0, fmt.Errorf("read settings: %w", err)
	}
	minutes := settings.Security.SessionTimeout
	if err := ValidateSessionTimeoutMinutes(minutes); err != nil {
		return 0, fmt.Errorf("stored session timeout: %w", err)
	}
	return time.Duration(minutes) * time.Minute, nil
}
