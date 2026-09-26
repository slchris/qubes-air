package service

import (
	"context"
	"fmt"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// ErrCredentialNotFound is the answer for an ID the operator cannot address:
// one that does not exist, and one that names a console row. It is the
// repository's own sentinel, so a row deleted between this view's check and
// its write is reported the same way.
var ErrCredentialNotFound = repository.ErrCredentialNotFound

// ErrConsoleCredential is returned instead of ErrCredentialNotFound when the ID
// exists but names a console row. It matches ErrCredentialNotFound under
// errors.Is, so a caller that only asks "can this be addressed?" gets the same
// answer for both. The API must answer both alike; the distinction exists only
// so the audit trail can record the refusal as denied.
var ErrConsoleCredential = fmt.Errorf("%w: reserved for the console", ErrCredentialNotFound)

// ErrReservedCredential refuses a create or rename that would put an operator
// row in the console's namespace: a row named like a console secret would be
// found by the console's name lookups in its place.
var ErrReservedCredential = fmt.Errorf(
	"credential names starting with %q and the type %q are reserved for the console's own secrets",
	models.ConsoleRowNamePrefix, models.ConsoleRowType)

// CredentialService is the operator's view of the credential store.
//
// The table also holds the console's own secrets (models.IsConsoleCredential):
// the agent CA, the legacy LUKS master, and each qube's data key and migration
// marker. This view never lists, reads, changes or deletes those rows, and
// never creates or renames a row into their namespace. The CA, data-key and
// purge paths use the repository directly and are unaffected.
//
// Whether an ID names a console row cannot change after this check: console
// rows are only ever inserted under a fresh ID and never renamed, a row's type
// cannot be changed at all, and this view refuses to rename a row into the
// namespace. Classifying an ID and then acting on it is therefore not a race.
type CredentialService struct {
	repo *repository.CredentialRepository
}

// NewCredentialService creates a new credential service.
func NewCredentialService(repo *repository.CredentialRepository) *CredentialService {
	return &CredentialService{repo: repo}
}

// List returns the operator's credentials, without secrets.
func (s *CredentialService) List(ctx context.Context) ([]models.Credential, error) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]models.Credential, 0, len(all))
	for _, c := range all {
		if !models.IsConsoleCredential(c.Name, c.Type) {
			visible = append(visible, c)
		}
	}
	return visible, nil
}

// GetByID returns one operator credential, without its secret. A missing ID
// returns ErrCredentialNotFound and a console row ErrConsoleCredential.
func (s *CredentialService) GetByID(ctx context.Context, id string) (*models.Credential, error) {
	c, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, ErrCredentialNotFound
	}
	if models.IsConsoleCredential(c.Name, c.Type) {
		return nil, ErrConsoleCredential
	}
	return c, nil
}

// Create stores a new operator credential. A name or type in the console's
// namespace returns ErrReservedCredential and stores nothing.
func (s *CredentialService) Create(ctx context.Context, req models.CredentialCreateRequest) (*models.Credential, error) {
	if models.IsConsoleCredential(req.Name, req.Type) {
		return nil, ErrReservedCredential
	}
	return s.repo.Create(ctx, req)
}

// Update changes an operator credential. A new name in the console's namespace
// returns ErrReservedCredential before the ID is looked at, so that answer
// does not depend on what the ID names. Otherwise a missing ID or a console
// row is refused as in GetByID and left untouched.
func (s *CredentialService) Update(ctx context.Context, id string, req models.CredentialUpdateRequest) (*models.Credential, error) {
	if req.Name != nil && models.IsConsoleCredential(*req.Name, "") {
		return nil, ErrReservedCredential
	}
	if _, err := s.GetByID(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.Update(ctx, id, req)
}

// Delete removes an operator credential. A missing ID or a console row is
// refused as in GetByID, so a console row is never deleted through this view;
// purge crypto-shreds a qube's data key through DataKeyManager instead.
func (s *CredentialService) Delete(ctx context.Context, id string) error {
	if _, err := s.GetByID(ctx, id); err != nil {
		return err
	}
	return s.repo.Delete(ctx, id)
}
