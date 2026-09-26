package repository

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
)

// ConsoleSecretStore is what reading a console secret needs.
// *CredentialRepository implements it; service tests supply an in-memory one.
type ConsoleSecretStore interface {
	List(ctx context.Context) ([]models.Credential, error)
	GetSecret(ctx context.Context, id string) (string, error)
}

// ConsoleSecret returns the secret of the console's own row named name. It is
// the one read path for every console secret — the CA, the data keys, the
// legacy master — in the console and in the CLI tools that read the CA, so
// "absent", "broken" and "ambiguous" are decided the same way everywhere:
//
//   - models.ErrConsoleRowNotFound when no row answers to the name;
//   - a models.ErrConsoleRowConflict error when rows the console did not write
//     answer to it (see models.SelectConsoleRow). No secret is read, and the
//     conflict is logged with the rows' IDs, once per name while it stands.
func ConsoleSecret(ctx context.Context, store ConsoleSecretStore, name string) (string, error) {
	list, err := store.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list credentials: %w", err)
	}
	row, err := models.SelectConsoleRow(list, name)
	if err != nil {
		if errors.Is(err, models.ErrConsoleRowConflict) {
			reportConflict(name, err)
		}
		return "", err
	}
	return store.GetSecret(ctx, row.ID)
}

// reportedConflicts holds, per console name, the conflict last logged for it.
var reportedConflicts sync.Map

// reportConflict logs a conflict once per name for as long as it stands. The
// lookups behind it run on every public revocation read and every lifecycle
// retry, so logging each call would let anyone who can reach
// GET /pki/revocations fill the journal with the same line. A changed
// conflict under the same name — a row added or removed — is logged again.
func reportConflict(name string, err error) {
	msg := err.Error()
	if prev, seen := reportedConflicts.Swap(name, msg); seen && prev == msg {
		return
	}
	log.Printf("SECURITY: pki: %s", msg)
}

// LoadConsoleCA reads the console's agent CA through ConsoleSecret. It never
// creates one: an absent half is models.ErrConsoleRowNotFound, and a half
// answered by rows the console did not write is models.ErrConsoleRowConflict.
func LoadConsoleCA(ctx context.Context, store ConsoleSecretStore) (*pki.CA, error) {
	certPEM, err := ConsoleSecret(ctx, store, models.ConsoleCACertName)
	if err != nil {
		return nil, fmt.Errorf("load CA certificate: %w", err)
	}
	keyPEM, err := ConsoleSecret(ctx, store, models.ConsoleCAKeyName)
	if err != nil {
		return nil, fmt.Errorf("load CA key: %w", err)
	}
	return pki.ParseCA(certPEM, keyPEM)
}
