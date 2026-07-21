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

// Credential name+type for the data-disk master secret. It sits in the same
// encrypted store as the CA key, so it is protected by the same keyring rather
// than by anything new.
const (
	dataMasterCredentialName = "qubes-air-luks-master" //nolint:gosec // G101: a store key name, not a credential
	dataMasterCredentialType = "pki"
)

// DataKeyManager owns the single master secret from which every qube's LUKS
// passphrase is derived.
//
// The master lives in the console's encrypted credential store and NEVER leaves
// the console. Per-qube keys are derived on demand (pki.DeriveDataKey) and
// pushed to the agent over verified mTLS only when a disk needs unlocking, so
// nothing on the untrusted remote — not the disk, not cloud-init, not a backup —
// ever holds a key or the master. That is the entire security argument for
// encrypting the data disk on a remote you do not trust.
type DataKeyManager struct {
	creds CredentialStore

	mu     sync.Mutex
	master string // cached master (base64), minted on first use, guarded by mu
}

// NewDataKeyManager builds a manager over the credential store.
func NewDataKeyManager(creds CredentialStore) *DataKeyManager {
	return &DataKeyManager{creds: creds}
}

// DataKeyFor returns the LUKS passphrase for a qube, minting the console's
// master secret on first use. The same qube id always returns the same key, so
// a resumed qube unlocks the same container.
func (m *DataKeyManager) DataKeyFor(ctx context.Context, qubeID string) (string, error) {
	master, err := m.loadOrCreateMaster(ctx)
	if err != nil {
		return "", err
	}
	return pki.DeriveDataKey(master, qubeID)
}

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
		Description: "Qubes Air data-disk MASTER secret — every qube's LUKS key derives from " +
			"this; whoever holds it can decrypt every data disk",
		SecretValue: secret,
	}); err != nil {
		return "", fmt.Errorf("store data master secret: %w", err)
	}
	log.Printf("pki: created the data-disk master secret (every qube's LUKS key derives from it)")
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
