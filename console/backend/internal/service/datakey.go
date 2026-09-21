package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
)

// Credential name+type for the data-disk master secret and per-qube keys. They
// sit in the same encrypted store as the CA key, so they are protected by the
// same keyring rather than by anything new.
const (
	// dataMasterCredentialName names the LEGACY master secret. It is only ever
	// READ, and only by the migration path: deriving from it to unlock a disk
	// that was formatted before per-qube keys existed, so the disk can be
	// rekeyed to its own DEK. The console never mints one — a fresh master
	// cannot open any existing disk.
	dataMasterCredentialName = "qubes-air-luks-master" //nolint:gosec // G101: a store key name, not a credential
	dataMasterCredentialType = "pki"
	// dataKeyCredentialPrefix names a qube's own data key. One credential per
	// qube, deletable on its own — which is what makes purge a crypto-shred.
	dataKeyCredentialPrefix = "qubes-air-luks-key-" //nolint:gosec // G101: a store key name prefix, not a credential
	// migrationMarkerPrefix names a NON-secret flag recording that a qube's
	// disk may still answer to the legacy master-derived key. It exists so a
	// rekey that added the DEK but failed to remove the old keyslot is retried
	// on the next unlock instead of leaving the old key valid forever.
	migrationMarkerPrefix = "qubes-air-luks-legacy-slot-"
)

// DataKeyManager owns the console's data-disk keys.
//
// A qube's disk is unlocked by its OWN random key — the DEK — stored in the
// console's encrypted credential store and never leaving the console; deleting
// that credential is the crypto-shred that makes purge irreversible. There is
// no silent fallback: a qube with no stored key is one whose disk predates
// per-qube keys, and the unlock path must migrate it (see LegacyKeyFor) before
// the disk can be opened.
type DataKeyManager struct {
	creds CredentialStore

	mu     sync.Mutex
	master string // cached legacy master (base64), loaded on first migration use
}

// NewDataKeyManager builds a manager over the credential store.
func NewDataKeyManager(creds CredentialStore) *DataKeyManager {
	return &DataKeyManager{creds: creds}
}

// KeyFor returns the qube's own stored data key. found is false when the qube
// has none, which means its disk was formatted under the legacy master-derived
// scheme (or that no disk exists yet). Callers must NOT substitute a derived
// key here: unlocking a legacy disk is a migration, and it goes through
// LegacyKeyFor plus an explicit rekey.
func (m *DataKeyManager) KeyFor(ctx context.Context, qubeID string) (key string, found bool, err error) {
	stored, err := m.storedKey(ctx, qubeID)
	if err != nil {
		return "", false, err
	}
	if stored == "" {
		return "", false, nil
	}
	return stored, true, nil
}

// LegacyKeyFor derives the old master-based key for an existing disk.
//
// It is the ONLY remaining use of the master secret, and it never mints one: a
// disk that was formatted under the legacy scheme was formatted with a key
// derived from THAT deployment's master, so a freshly generated master could
// only produce a key that opens nothing. An absent master is therefore an error
// that stops the migration — the disk stays closed, which is the safe outcome.
//
// Callers must rekey the disk to the stored DEK before using it normally; the
// derived key is a migration credential, not an unlock credential.
func (m *DataKeyManager) LegacyKeyFor(ctx context.Context, qubeID string) (string, error) {
	master, err := m.loadMaster(ctx)
	if err != nil {
		return "", err
	}
	key, err := pki.DeriveDataKey(master, qubeID)
	if err != nil {
		return "", err
	}
	return key, nil
}

// EnsureDataKey mints and stores a random per-qube key if the qube has none, and
// returns the key. Called before a disk is formatted so the container is created
// with a key only this qube's record holds, and by the migration path so a
// legacy disk gets a DEK to move to.
func (m *DataKeyManager) EnsureDataKey(ctx context.Context, qubeID string) (string, error) {
	if y, err := m.storedKey(ctx, qubeID); err != nil {
		return "", err
	} else if y != "" {
		return y, nil
	}
	key, err := pki.NewDataKey()
	if err != nil {
		return "", err
	}
	if _, err := m.creds.Create(ctx, models.CredentialCreateRequest{
		Name:        dataKeyCredentialPrefix + qubeID,
		Type:        dataMasterCredentialType,
		Description: "Per-qube LUKS data key. Deleting this is a crypto-shred: the disk's ciphertext becomes unrecoverable.",
		SecretValue: key,
	}); err != nil {
		return "", fmt.Errorf("store data key for qube %s: %w", qubeID, err)
	}
	log.Printf("pki: minted a per-qube data key for %s", qubeID)
	return key, nil
}

// DeleteDataKey removes a qube's stored key and any migration marker. Idempotent:
// deleting credentials that are already gone is not an error, so purge can be
// retried.
func (m *DataKeyManager) DeleteDataKey(ctx context.Context, qubeID string) error {
	list, err := m.creds.List(ctx)
	if err != nil {
		return fmt.Errorf("list credentials: %w", err)
	}
	keyName, markerName := dataKeyCredentialPrefix+qubeID, migrationMarkerPrefix+qubeID
	found := false
	for _, cred := range list {
		name := strings.ToLower(cred.Name)
		switch name {
		case strings.ToLower(keyName), strings.ToLower(markerName):
			if err := m.creds.Delete(ctx, cred.ID); err != nil {
				return fmt.Errorf("delete data key for qube %s: %w", qubeID, err)
			}
			found = found || name == strings.ToLower(keyName)
		}
	}
	if found {
		log.Printf("pki: deleted the per-qube data key for %s (crypto-shred)", qubeID)
	}
	return nil
}

// MigrationPending reports whether a qube's disk may still carry the legacy
// master-derived keyslot. The unlock path uses it to finish a rekey that added
// the DEK but did not remove the old key.
func (m *DataKeyManager) MigrationPending(ctx context.Context, qubeID string) (bool, error) {
	list, err := m.creds.List(ctx)
	if err != nil {
		return false, fmt.Errorf("list credentials: %w", err)
	}
	for _, cred := range list {
		if strings.EqualFold(cred.Name, migrationMarkerPrefix+qubeID) {
			return true, nil
		}
	}
	return false, nil
}

// MarkMigrationPending records that a qube's legacy keyslot may still be valid.
// Idempotent: marking an already-marked qube is not an error.
func (m *DataKeyManager) MarkMigrationPending(ctx context.Context, qubeID string) error {
	list, err := m.creds.List(ctx)
	if err != nil {
		return fmt.Errorf("list credentials: %w", err)
	}
	for _, cred := range list {
		if strings.EqualFold(cred.Name, migrationMarkerPrefix+qubeID) {
			return nil
		}
	}
	if _, err := m.creds.Create(ctx, models.CredentialCreateRequest{
		Name:        migrationMarkerPrefix + qubeID,
		Type:        dataMasterCredentialType,
		Description: "Non-secret marker: this qube's data disk may still answer to the legacy master-derived key; the next unlock retries removal.",
		SecretValue: "pending",
	}); err != nil {
		return fmt.Errorf("mark migration pending for qube %s: %w", qubeID, err)
	}
	return nil
}

// ClearMigrationPending removes the marker once the legacy keyslot is verified
// gone. Idempotent.
func (m *DataKeyManager) ClearMigrationPending(ctx context.Context, qubeID string) error {
	list, err := m.creds.List(ctx)
	if err != nil {
		return fmt.Errorf("list credentials: %w", err)
	}
	for _, cred := range list {
		if strings.EqualFold(cred.Name, migrationMarkerPrefix+qubeID) {
			if err := m.creds.Delete(ctx, cred.ID); err != nil {
				return fmt.Errorf("clear migration marker for qube %s: %w", qubeID, err)
			}
			return nil
		}
	}
	return nil
}

// storedKey returns a qube's own data key, or "" when it has none.
func (m *DataKeyManager) storedKey(ctx context.Context, qubeID string) (string, error) {
	name := dataKeyCredentialPrefix + qubeID
	list, err := m.creds.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list credentials: %w", err)
	}
	for _, cred := range list {
		if strings.EqualFold(cred.Name, name) {
			return m.creds.GetSecret(ctx, cred.ID)
		}
	}
	return "", nil
}

// loadMaster reads the legacy master secret, caching it for the process. It
// deliberately does not create one; see LegacyKeyFor.
func (m *DataKeyManager) loadMaster(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.master != "" {
		return m.master, nil
	}
	existing, err := lookupCredential(ctx, m.creds, dataMasterCredentialName)
	if err != nil {
		if errors.Is(err, errCredentialNotFound) {
			return "", fmt.Errorf("legacy data-disk master secret %q is not in the credential store; "+
				"restore the keyring that holds it before migrating legacy disks", dataMasterCredentialName)
		}
		return "", err
	}
	m.master = existing
	return existing, nil
}

// lookupCredential finds a credential's secret by name, returning
// errCredentialNotFound when absent. Shared with CertIssuer's own lookup so the
// "absent vs broken" distinction is made the same way everywhere.
func lookupCredential(ctx context.Context, creds CredentialStore, name string) (string, error) {
	list, err := creds.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list credentials: %w", err)
	}
	for _, cred := range list {
		if strings.EqualFold(cred.Name, name) {
			return creds.GetSecret(ctx, cred.ID)
		}
	}
	return "", errCredentialNotFound
}
