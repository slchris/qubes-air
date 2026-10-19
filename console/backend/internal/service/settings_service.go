package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
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
	// ErrInvalidWebhookTarget reports a webhook destination that is not an
	// allowlisted HTTPS endpoint.
	ErrInvalidWebhookTarget = errors.New("invalid webhook target")
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
	// Serializes destination read/modify/write and legacy migration within this
	// Console. The HTTP handler has its own lock for "store then apply"; this one
	// also covers the webhook dispatcher, which reads the destination while a
	// save may be replacing it.
	mu                  sync.Mutex
	repo                *repository.SettingsRepository
	credentials         *repository.CredentialRepository
	webhookAllowedHosts []string
}

// NewSettingsService creates a new settings service.
func NewSettingsService(repo *repository.SettingsRepository) *SettingsService {
	return &SettingsService{repo: repo}
}

// WithWebhookAllowedHosts limits outbound webhook delivery to configured hosts.
func (s *SettingsService) WithWebhookAllowedHosts(hosts []string) *SettingsService {
	s.webhookAllowedHosts = append([]string(nil), hosts...)
	return s
}

// WithWebhookCredentials encrypts the destination using the credential keyring.
func (s *SettingsService) WithWebhookCredentials(credentials *repository.CredentialRepository) *SettingsService {
	s.credentials = credentials
	return s
}

// Get returns the settings as they are in effect.
//
// A group that was never stored keeps its default. A storage or decode failure
// is returned to the caller rather than replaced with a default: a read must
// not silently report different settings than the ones the console is using.
//
// Two stored values can disagree with what the console does, and Get reports
// the effective one instead:
//   - a session timeout outside the accepted range, which releases before the
//     range was enforced saved without complaint, reads as the default the
//     session store falls back to (see SessionTTL);
//   - email notifications and two-factor authentication are not implemented,
//     so they read as off whatever an older release stored.
func (s *SettingsService) Get(ctx context.Context) (*models.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.repo.Get(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.migrateWebhook(ctx, settings); err != nil {
		return nil, err
	}
	if ValidateSessionTimeoutMinutes(settings.Security.SessionTimeout) != nil {
		settings.Security.SessionTimeout = DefaultSessionTimeoutMinutes
	}
	settings.Notifications.WebhookConfigured = false
	if s.credentials != nil && settings.Notifications.Webhook {
		_, configured, err := s.credentials.GetAlertWebhookTarget(ctx)
		if err != nil {
			return nil, err
		}
		settings.Notifications.WebhookConfigured = configured
	}
	settings.Notifications.Email = false
	settings.Security.TwoFactorEnabled = false
	return settings, nil
}

// migrateWebhook removes an old plaintext destination only when its encrypted
// replacement (or deliberate disable) commits successfully in the same
// transaction.
func (s *SettingsService) migrateWebhook(ctx context.Context, settings *models.Settings) error {
	target := settings.Notifications.WebhookURL
	if target == "" {
		return nil
	}
	if s.credentials == nil || ValidateWebhookTarget(target, s.webhookAllowedHosts) != nil {
		settings.Notifications.Webhook = false
		log.Printf("settings: disabled legacy webhook; encrypted storage or an allowed destination is unavailable")
	}
	if !settings.Notifications.Webhook {
		target = ""
	}
	settings.Notifications.WebhookURL = ""
	return s.save(ctx, settings, target)
}

// Update validates and stores settings. The session timeout must be inside the
// accepted range, and settings the console does not implement cannot be turned
// on; either refusal leaves the stored settings untouched.
//
// The encrypted webhook destination is written in the same transaction as the
// settings row that enables it. A failure in either store leaves both at their
// previous values.
func (s *SettingsService) Update(ctx context.Context, settings *models.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateSessionTimeoutMinutes(settings.Security.SessionTimeout); err != nil {
		return err
	}
	if settings.Notifications.Email {
		return fmt.Errorf("%w: email notifications", ErrUnsupportedSetting)
	}
	if settings.Security.TwoFactorEnabled {
		return fmt.Errorf("%w: two-factor authentication", ErrUnsupportedSetting)
	}
	target, err := s.resolveWebhookTarget(ctx, settings.Notifications)
	if err != nil {
		return err
	}
	if err := s.save(ctx, settings, target); err != nil {
		return err
	}
	settings.Notifications.WebhookConfigured = settings.Notifications.Webhook && target != ""
	settings.Notifications.WebhookURL = ""
	return nil
}

func (s *SettingsService) resolveWebhookTarget(ctx context.Context, notifications models.NotificationSettings) (string, error) {
	if !notifications.Webhook {
		return "", nil
	}
	if s.credentials == nil {
		return "", fmt.Errorf("%w: encrypted webhook credential storage is unavailable", ErrInvalidWebhookTarget)
	}
	target := notifications.WebhookURL
	if target == "" {
		var err error
		target, _, err = s.credentials.GetAlertWebhookTarget(ctx)
		if err != nil {
			return "", fmt.Errorf("load webhook destination: %w", err)
		}
	}
	if err := ValidateWebhookTarget(target, s.webhookAllowedHosts); err != nil {
		return "", err
	}
	return target, nil
}

func (s *SettingsService) save(ctx context.Context, settings *models.Settings, target string) error {
	if s.credentials == nil {
		return s.repo.Update(ctx, settings)
	}
	return s.repo.UpdateWithWebhook(ctx, settings, s.credentials, target)
}

// WebhookTarget returns the current endpoint only to the internal dispatcher;
// ordinary settings API responses expose configuration state, never the URL.
func (s *SettingsService) WebhookTarget(ctx context.Context) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.repo.Get(ctx)
	if err != nil {
		return "", false, err
	}
	if !settings.Notifications.Webhook {
		return "", false, nil
	}
	if s.credentials == nil {
		return "", false, fmt.Errorf("encrypted webhook credential storage is unavailable")
	}
	target, configured, err := s.credentials.GetAlertWebhookTarget(ctx)
	if err != nil {
		return "", false, err
	}
	if !configured {
		return "", false, fmt.Errorf("webhook is enabled but no encrypted endpoint is configured")
	}
	return target, true, nil
}

// SessionTTL returns the browser session lifetime stored in settings.
//
// An error wrapping ErrInvalidSessionTimeout means the stored value is out of
// range; any other error means the stored row does not parse. The caller
// decides the fallback, and the console's startup uses the default with a
// warning rather than refusing to start over a preference.
func (s *SettingsService) SessionTTL(ctx context.Context) (time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// ValidateWebhookTarget accepts HTTPS URLs only when their exact host is in the
// operator-managed allowlist; URL userinfo and redirects are not accepted.
func ValidateWebhookTarget(raw string, allowedHosts []string) error {
	if len(raw) == 0 || len(raw) > 2048 || strings.TrimSpace(raw) != raw || strings.Contains(raw, "#") {
		return fmt.Errorf("%w: URL is empty or too long", ErrInvalidWebhookTarget)
	}
	target, err := url.ParseRequestURI(raw)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil || target.Fragment != "" {
		return fmt.Errorf("%w: an HTTPS URL without userinfo or fragment is required", ErrInvalidWebhookTarget)
	}
	host := webhookAllowlistKey(target)
	if !slices.Contains(allowedHosts, host) {
		return fmt.Errorf("%w: host is not in notifications.webhook_allowed_hosts", ErrInvalidWebhookTarget)
	}
	return nil
}

func webhookAllowlistKey(target *url.URL) string {
	host := strings.ToLower(target.Hostname())
	if port := target.Port(); port != "" {
		return net.JoinHostPort(host, port)
	}
	return host
}
