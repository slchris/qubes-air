package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// Credential names under which the CA is stored.
//
// The CA private key is the single most valuable secret this console holds:
// whoever has it can mint any agent identity in the fleet. It lives in the
// encrypted credential store rather than on disk so it is protected by the same
// keyring, and can be rotated by the same machinery.
const (
	caCertCredentialName = "qubes-air-ca-cert"
	caKeyCredentialName  = "qubes-air-ca-key"
	caCredentialType     = "pki"
)

// CertIssuer issues and registers agent certificates.
//
// Issuance and registration are deliberately coupled: a certificate that is
// signed but not registered would be refused at connection time, and one that
// is registered without being issued cannot exist. Doing them together is what
// keeps the CA and the revocation registry describing the same reality.
type CertIssuer struct {
	creds CredentialStore
	certs *repository.AgentCertRepository

	// mu guards lazy CA initialization so two concurrent qube creations cannot
	// each mint a CA and race to store it — which would leave agents trusting
	// different roots.
	mu sync.Mutex
	ca *pki.CA
}

// CredentialStore is the subset of the credential repository this needs.
type CredentialStore interface {
	GetSecret(ctx context.Context, id string) (string, error)
	List(ctx context.Context) ([]models.Credential, error)
	Create(ctx context.Context, req models.CredentialCreateRequest) (*models.Credential, error)
}

// NewCertIssuer builds an issuer over the credential store and registry.
func NewCertIssuer(creds CredentialStore, certs *repository.AgentCertRepository) *CertIssuer {
	return &CertIssuer{creds: creds, certs: certs}
}

// IssueFor mints a client certificate for a qube's agent and registers it.
//
// The returned bundle is what cloud-init delivers to the remote. It contains
// the agent's key but never the CA's — see pki.Bundle.
func (c *CertIssuer) IssueFor(ctx context.Context, qube *models.Qube) (*pki.Bundle, error) {
	ca, err := c.loadOrCreateCA(ctx)
	if err != nil {
		return nil, fmt.Errorf("certificate authority: %w", err)
	}

	// The common name identifies the agent in logs and in the registry. It is
	// NOT an authorization input: the fingerprint is what the registry matches,
	// so a certificate cannot gain privilege by naming itself something else.
	cn := "agent-" + qube.Name
	bundle, err := ca.IssueAgentCert(cn, pki.DefaultAgentCertLifetime)
	if err != nil {
		return nil, fmt.Errorf("issue certificate for %q: %w", qube.Name, err)
	}

	notAfter := bundle.NotAfter
	if err := c.certs.Register(ctx, &repository.AgentCert{
		Fingerprint: bundle.Fingerprint,
		QubeID:      qube.ID,
		SubjectCN:   cn,
		IssuedAt:    time.Now().UTC(),
		ExpiresAt:   &notAfter,
	}); err != nil {
		// Registration failed, so this certificate can never be authorized.
		// Returning it would hand out a credential that silently does not work.
		return nil, fmt.Errorf("register certificate for %q: %w", qube.Name, err)
	}

	log.Printf("pki: issued certificate %s for qube %q (expires %s)",
		bundle.Fingerprint[:16], qube.Name, notAfter.Format(time.RFC3339))
	return bundle, nil
}

// RevokeFor revokes every certificate issued to a qube.
//
// Called when a qube is purged: a decommissioned machine must not keep a
// working credential.
func (c *CertIssuer) RevokeFor(ctx context.Context, qubeID, reason string) error {
	n, err := c.certs.RevokeByQube(ctx, qubeID, reason)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("pki: revoked %d certificate(s) for qube %s: %s", n, qubeID, reason)
	}
	return nil
}

// loadOrCreateCA returns the console's CA, creating and storing it on first use.
func (c *CertIssuer) loadOrCreateCA(ctx context.Context) (*pki.CA, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ca != nil {
		return c.ca, nil
	}

	certPEM, certErr := c.findCredential(ctx, caCertCredentialName)
	keyPEM, keyErr := c.findCredential(ctx, caKeyCredentialName)

	switch {
	case certErr == nil && keyErr == nil:
		ca, err := pki.ParseCA(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("stored CA is unusable: %w", err)
		}
		c.ca = ca
		return ca, nil

	case errors.Is(certErr, errCredentialNotFound) && errors.Is(keyErr, errCredentialNotFound):
		return c.createCA(ctx)

	default:
		// Exactly one half present means a partial write or a partial deletion.
		// Minting a replacement would silently invalidate every certificate the
		// missing half signed, so refuse and let a human look.
		return nil, fmt.Errorf(
			"CA is half-present (cert: %v, key: %v); refusing to mint a replacement, "+
				"which would invalidate every already-issued certificate", certErr, keyErr)
	}
}

// createCA mints and stores a new CA.
func (c *CertIssuer) createCA(ctx context.Context) (*pki.CA, error) {
	ca, err := pki.NewCA("qubes-air-console", pki.DefaultCALifetime)
	if err != nil {
		return nil, err
	}
	certPEM, keyPEM, err := ca.MarshalCA()
	if err != nil {
		return nil, err
	}

	// Store the certificate first. If the process dies between the two writes
	// the result is a half-present CA, which loadOrCreateCA refuses rather than
	// papering over — that is the intended behaviour, not an oversight.
	if _, err := c.creds.Create(ctx, models.CredentialCreateRequest{
		Name:        caCertCredentialName,
		Type:        caCredentialType,
		Description: "Qubes Air agent CA certificate (public)",
		SecretValue: certPEM,
	}); err != nil {
		return nil, fmt.Errorf("store CA certificate: %w", err)
	}
	if _, err := c.creds.Create(ctx, models.CredentialCreateRequest{
		Name:        caKeyCredentialName,
		Type:        caCredentialType,
		Description: "Qubes Air agent CA PRIVATE KEY — whoever holds this can mint any agent identity",
		SecretValue: keyPEM,
	}); err != nil {
		return nil, fmt.Errorf("store CA key: %w", err)
	}

	log.Printf("pki: created a new agent CA (valid until %s)", ca.Cert.NotAfter.Format(time.RFC3339))
	c.ca = ca
	return ca, nil
}

// errCredentialNotFound distinguishes "absent" from "broken" when loading.
var errCredentialNotFound = errors.New("credential not found")

// findCredential looks a credential up by name and returns its secret.
func (c *CertIssuer) findCredential(ctx context.Context, name string) (string, error) {
	list, err := c.creds.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list credentials: %w", err)
	}
	for _, cred := range list {
		if strings.EqualFold(cred.Name, name) {
			return c.creds.GetSecret(ctx, cred.ID)
		}
	}
	return "", errCredentialNotFound
}
