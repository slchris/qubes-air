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
	// ErrZoneCredentialNotFound refuses a zone config whose credential_id
	// names no operator credential. A console row gets the same answer, so a
	// zone cannot be pointed at the CA or a data key, and the refusal does
	// not say whether such a row exists.
	ErrZoneCredentialNotFound = errors.New("credential_id does not name a stored credential")
	// ErrZoneConnectionLocked refuses a zone-scoped caller changing where the
	// zone's credential is sent. Letting it repoint the endpoint (or the
	// credential, or the CA the endpoint is checked against) would let a zone
	// operator have the console hand a fleet-managed credential to a server
	// of its choosing.
	ErrZoneConnectionLocked = errors.New("changing a zone's endpoint, credential or trust anchor needs a fleet-wide credential")
)

// ZoneCredentialRefs resolves a zone's credential_id through the operator's
// view of the credential store. *CredentialService satisfies it: a missing ID
// and a console row both come back as ErrCredentialNotFound.
type ZoneCredentialRefs interface {
	GetByID(ctx context.Context, id string) (*models.Credential, error)
}

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
	// UpdateKeepingConnection is Update for a zone-scoped caller: it refuses,
	// with ErrZoneConnectionLocked, any change to the fields that decide where
	// the zone's credential is sent.
	UpdateKeepingConnection(ctx context.Context, id string, req *models.ZoneUpdateRequest) (*models.Zone, error)
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
	// credentialRefs checks every credential_id a create or update writes.
	// Nil admits none: a config that names a credential is refused.
	credentialRefs ZoneCredentialRefs
}

// ZoneServiceOption customizes a ZoneServiceImpl.
type ZoneServiceOption func(*ZoneServiceImpl)

// WithCredentialRefs lets zones reference credentials, checked against refs.
func WithCredentialRefs(refs ZoneCredentialRefs) ZoneServiceOption {
	return func(s *ZoneServiceImpl) { s.credentialRefs = refs }
}

// NewZoneService creates a new ZoneService.
//
// adapters is consulted on create only. A nil value fails closed — every
// create is refused — because the alternative, admitting every valid type, is
// exactly the silent acceptance the check exists to remove. Credential
// references fail closed the same way until WithCredentialRefs is given.
func NewZoneService(zoneRepo repository.ZoneRepository, qubeRepo repository.QubeRepository, adapters ZoneTypeSupport, opts ...ZoneServiceOption) ZoneService {
	s := &ZoneServiceImpl{
		zoneRepo: zoneRepo,
		qubeRepo: qubeRepo,
		adapters: adapters,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Create creates a new zone.
func (s *ZoneServiceImpl) Create(ctx context.Context, req *models.ZoneCreateRequest) (*models.Zone, error) {
	if err := validateZoneCreateRequest(req); err != nil {
		return nil, err
	}
	if err := s.requireAdapter(req.Type); err != nil {
		return nil, err
	}
	if err := s.checkCredentialRefs(ctx, req.Config); err != nil {
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
	return s.update(ctx, id, req, false)
}

// UpdateKeepingConnection updates a zone without letting the caller change
// where its credential is sent.
func (s *ZoneServiceImpl) UpdateKeepingConnection(ctx context.Context, id string, req *models.ZoneUpdateRequest) (*models.Zone, error) {
	return s.update(ctx, id, req, true)
}

// update applies req to the zone. The connection check runs before the
// credential check, so a zone-scoped caller cannot use the refusal to learn
// whether some other credential ID exists.
func (s *ZoneServiceImpl) update(ctx context.Context, id string, req *models.ZoneUpdateRequest, keepConnection bool) (*models.Zone, error) {
	zone, err := s.zoneRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrZoneNotFound
	}
	if req.Config != nil {
		if keepConnection && connectionOf(*req.Config) != connectionOf(zone.Config) {
			return nil, ErrZoneConnectionLocked
		}
		if err := s.checkCredentialRefs(ctx, *req.Config); err != nil {
			return nil, err
		}
	}

	applyZoneUpdates(zone, req)
	zone.UpdatedAt = time.Now()

	if err := s.zoneRepo.Update(ctx, zone); err != nil {
		return nil, err
	}

	return zone, nil
}

// zoneConnection is every config field that decides where a zone's
// credential goes or which server is trusted to receive it.
type zoneConnection struct {
	endpoint            string
	proxmoxCredentialID string
	proxmoxCAPEM        string
	gcpCredentialID     string
	gcpIdentityBucket   string
	gcpServiceAccount   string
}

func connectionOf(cfg models.ZoneConfig) zoneConnection {
	c := zoneConnection{endpoint: cfg.Endpoint}
	if cfg.Proxmox != nil {
		c.proxmoxCredentialID, c.proxmoxCAPEM = cfg.Proxmox.CredentialID, cfg.Proxmox.CAPEM
	}
	if cfg.GCP != nil {
		c.gcpCredentialID = cfg.GCP.CredentialID
		c.gcpIdentityBucket, c.gcpServiceAccount = cfg.GCP.IdentityBucket, cfg.GCP.ServiceAccountEmail
	}
	return c
}

// checkCredentialRefs refuses a config that names a credential the operator
// cannot see: a missing ID or a console row (both ErrZoneCredentialNotFound;
// a console row also matches ErrConsoleCredential, for the audit trail only).
func (s *ZoneServiceImpl) checkCredentialRefs(ctx context.Context, cfg models.ZoneConfig) error {
	c := connectionOf(cfg)
	for _, id := range []string{c.proxmoxCredentialID, c.gcpCredentialID} {
		if id == "" {
			continue
		}
		if s.credentialRefs == nil {
			return fmt.Errorf("%w: this console cannot check credential references", ErrZoneCredentialNotFound)
		}
		_, err := s.credentialRefs.GetByID(ctx, id)
		switch {
		case errors.Is(err, ErrConsoleCredential):
			return fmt.Errorf("%w: %w", ErrZoneCredentialNotFound, err)
		case errors.Is(err, ErrCredentialNotFound):
			return ErrZoneCredentialNotFound
		case err != nil:
			return fmt.Errorf("check credential_id: %w", err)
		}
	}
	return nil
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
