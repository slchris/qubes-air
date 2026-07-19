package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/slchris/qubes-air/console/internal/qrexec"
)

// vaultcerts.go — fetch mTLS material from the no-network vault-cloud via qrexec
// ask (qubesair.GetCredential+<name>), assembling a *tls.Config in memory.
//
// The certs/keys NEVER touch disk here: vault-cloud cats them to stdout over
// qrexec (dom0 policy: ask), we hold them in memory only. This replaces the
// file-path mTLS loading for the vault-backed path.

// VaultCertConfig names the credentials to fetch from vault-cloud.
type VaultCertConfig struct {
	// VaultQube is the credential vault qube (default "vault-cloud").
	VaultQube string
	// Credential names passed as qubesair.GetCredential+<name>. Cert/Key are
	// required; CA is optional (empty → system roots).
	CertName string
	KeyName  string
	CAName   string
	// ServerName for TLS SNI / verification of the remote (e.g. remote_name).
	ServerName string
}

// getCredentialService is the qrexec service that cats a named credential from
// vault-cloud to stdout (dom0 policy: ask).
const getCredentialService = "qubesair.GetCredential"

// FetchClientMTLS fetches client cert/key (and optional CA) from vault-cloud via
// qrexec and returns an in-memory *tls.Config for the outbound relay client.
//
// Uses the default real qrexec client (which triggers dom0 policy ask prompts).
func FetchClientMTLS(ctx context.Context, cfg VaultCertConfig) (*tls.Config, error) {
	return fetchClientMTLSWith(ctx, cfg, qrexec.NewClient())
}

// VaultTLSProvider returns a Client.TLSProvider that re-fetches the mTLS config
// from vault-cloud on EACH call. Passed as ClientConfig.TLSProvider, this makes
// certificate ROTATION take effect on reconnect: after vault rotates the relay
// cert, the next reconnect fetches the new one — no restart needed.
//
// Each call runs a qrexec GetCredential (dom0 policy: ask); on a networked relay
// this is only exercised on (re)connect, not per-request.
func VaultTLSProvider(cfg VaultCertConfig) func() (*tls.Config, error) {
	return func() (*tls.Config, error) {
		return FetchClientMTLS(context.Background(), cfg)
	}
}

// fetchClientMTLSWith is the test seam: inject a fake qrexec client.
func fetchClientMTLSWith(ctx context.Context, cfg VaultCertConfig, qc qrexecClient) (*tls.Config, error) {
	vault := cfg.VaultQube
	if vault == "" {
		vault = "vault-cloud"
	}
	if cfg.CertName == "" || cfg.KeyName == "" {
		return nil, fmt.Errorf("vault mTLS: cert_name and key_name are required")
	}

	certPEM, err := getCred(ctx, qc, vault, cfg.CertName)
	if err != nil {
		return nil, fmt.Errorf("fetch client cert: %w", err)
	}
	keyPEM, err := getCred(ctx, qc, vault, cfg.KeyName)
	if err != nil {
		return nil, fmt.Errorf("fetch client key: %w", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse client cert/key: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ServerName:   cfg.ServerName,
		MinVersion:   tls.VersionTLS12,
	}
	if cfg.CAName != "" {
		caPEM, err := getCred(ctx, qc, vault, cfg.CAName)
		if err != nil {
			return nil, fmt.Errorf("fetch CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("vault mTLS: CA credential %q has no valid certificates", cfg.CAName)
		}
		tlsCfg.RootCAs = pool
	}
	return tlsCfg, nil
}

// getCred asks vault for one named credential: qubesair.GetCredential+<name>.
// The '+<name>' is the qrexec service argument the vault handler reads.
func getCred(ctx context.Context, qc qrexecClient, vault, name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("empty credential name")
	}
	service := getCredentialService + "+" + name
	out, err := qc.Call(ctx, vault, service, nil)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("vault returned empty credential for %q", name)
	}
	return out, nil
}
