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
	dataMasterCredentialName = "qubes-air-luks-master" //nolint:gosec // G101: a store key name, not a credential
	dataMasterCredentialType = "pki"
	// dataKeyCredentialPrefix names a qube's own data key. One credential per
	// qube, deletable on its own — which is what makes purge a crypto-shred.
	dataKeyCredentialPrefix = "qubes-air-luks-key-" //nolint:gosec // G101: a store key name prefix, not a credential
)

// DataKeyManager owns the console's data-disk keys.
//
// New qubes get a RANDOM per-qube key, stored in the console's encrypted
// credential store and never leaving the console; deleting that credential is
// the crypto-shred that makes purge irreversible. Qubes created before per-qube
// keys existed have no stored key, so DataKeyFor falls back to the legacy
// master-derived scheme (pki.DeriveDataKey) — those keep unlocking, but they
// cannot be crypto-shredded because their key is recomputable from the master.
type DataKeyManager struct {
	creds CredentialStore

	mu     sync.Mutex
	master string // cached legacy master (base64), minted on first legacy use
}

// NewDataKeyManager builds a manager over the credential store.
func NewDataKeyManager(creds CredentialStore) *DataKeyManager {
	return &DataKeyManager{creds: creds}
}

// DataKeyFor returns the LUKS passphrase for a qube.
//
// It prefers the qube's own stored key. A qube with none falls back to the
// legacy master-derived key, which is what keeps disks formatted before the
// per-qube scheme unlockable.
func (m *DataKeyManager) DataKeyFor(ctx context.Context, qubeID string) (string, error) {
	stored, err := m.storedKey(ctx, qubeID)
	if err != nil {
		return "", err
	}
	if stored != "" {
		return stored, nil
	}

	master, err := m.loadOrCreateMaster(ctx)
	if err != nil {
		return "", err
	}
	key, err := pki.DeriveDataKey(master, qubeID)
	if err != nil {
		return "", err
	}
	// Said once per call rather than silently: a legacy qube is one whose data
	// disk cannot be crypto-shredded on purge, and that is worth seeing while
	// there is still time to re-provision it.
	log.Printf("pki: qube %s has no per-qube data key; using the legacy master-derived key "+
		"(this qube cannot be crypto-shredded on purge)", qubeID)
	return key, nil
}

// EnsureDataKey mints and stores a random per-qube key if the qube has none, and
// returns the key. Called before a disk is formatted so the container is created
// with a key only this qube's record holds.
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

// DeleteDataKey removes a qube's stored key, if any. Idempotent: deleting a key
// that is already gone is not an error, so purge can be retried.
func (m *DataKeyManager) DeleteDataKey(ctx context.Context, qubeID string) error {
	name := dataKeyCredentialPrefix + qubeID
	list, err := m.creds.List(ctx)
	if err != nil {
		return fmt.Errorf("list credentials: %w", err)
	}
	found := false
	for _, cred := range list {
		if strings.EqualFold(cred.Name, name) {
			if err := m.creds.Delete(ctx, cred.ID); err != nil {
				return fmt.Errorf("delete data key for qube %s: %w", qubeID, err)
			}
			found = true
		}
	}
	if found {
		log.Printf("pki: deleted the per-qube data key for %s (crypto-shred)", qubeID)
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

// loadOrCreateMaster mints the LEGACY master secret on first use. It exists only
// for qubes that predate per-qube keys; new qubes never touch it.
func (m *DataKeyManager) loadOrCreateMaster(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.master != "" {
		return m.master, nil
	}

	existing, err := lookupCredential(ctx, m.creds, dataMasterCredentialName)
	switch {
	case err == nil:
		m.master = existing
		return existing, nil
	case errors.Is(err, errCredentialNotFound):
		// Fall through and mint one.
	default:
		return "", err
	}

	secret, err := pki.NewDataMasterSecret()
	if err != nil {
		return "", err
	}
	if _, err := m.creds.Create(ctx, models.CredentialCreateRequest{
		Name: dataMasterCredentialName,
		Type: dataMasterCredentialType,
		Description: "Qubes Air data-disk MASTER secret (LEGACY) — keys for qubes created before " +
			"per-qube keys derive from this; whoever holds it can decrypt those disks",
		SecretValue: secret,
	}); err != nil {
		return "", fmt.Errorf("store data master secret: %w", err)
	}
	log.Printf("pki: created the legacy data-disk master secret (only for qubes without per-qube keys)")
	m.master = secret
	return secret, nil
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
