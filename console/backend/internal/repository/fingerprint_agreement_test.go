package repository

import (
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/slchris/qubes-air/console/internal/pki"
)

// TestFingerprintImplementationsAgree pins the contract between issuance and
// verification.
//
// pki.FingerprintOf produces the registry key when a certificate is issued;
// repository.Fingerprint consumes it at TLS verification time. They are in
// different packages and could drift independently — and if they ever did,
// EVERY agent would be rejected as unregistered, which looks like a
// connectivity fault rather than a code change.
func TestFingerprintImplementationsAgree(t *testing.T) {
	ca, err := pki.NewCA("test-ca", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	bundle, err := ca.IssueAgentCert("agent-x", 0)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}

	block, _ := pem.Decode([]byte(bundle.CertPEM))
	if block == nil {
		t.Fatal("issued certificate is not valid PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// The value recorded at issuance must be the value computed at verification.
	if got := Fingerprint(cert); got != bundle.Fingerprint {
		t.Fatalf("fingerprint mismatch would reject every agent:\n  issuance:     %s\n  verification: %s",
			bundle.Fingerprint, got)
	}
}
