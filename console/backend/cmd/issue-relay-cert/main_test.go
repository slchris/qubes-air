package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
)

func makeCSR(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatalf("csr: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func testCA(t *testing.T) *pki.CA {
	t.Helper()
	ca, err := pki.NewCA("qubes-air-test-ca", 72*time.Hour)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	return ca
}

// A relay asking for its OWN identity is signed, and the certificate carries the
// relay- prefixed common name derived from the caller, not whatever the CSR said.
func TestIssueRelayCert_PinsToCaller(t *testing.T) {
	ca := testCA(t)
	// The relay correctly builds its CSR with the relay-<self> common name.
	csr := makeCSR(t, pki.RelayCommonName("sys-relay-pve"))

	signed, err := issueRelayCert(ca, "sys-relay-pve", csr, time.Hour)
	if err != nil {
		t.Fatalf("issueRelayCert: %v", err)
	}
	block, _ := pem.Decode([]byte(signed.CertPEM))
	if block == nil {
		t.Fatal("no certificate in output")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if got, want := cert.Subject.CommonName, "relay-sys-relay-pve"; got != want {
		t.Fatalf("CN = %q, want %q", got, want)
	}
}

// The escalation this design must stop: a caller whose CSR asks for a DIFFERENT
// qube's relay identity. The CN is pinned to the (unforgeable) caller name, so
// SignAgentCSR sees a mismatch and refuses — the caller cannot mint a cert for
// someone else even though dom0 let it reach the service.
func TestIssueRelayCert_RefusesForeignIdentity(t *testing.T) {
	ca := testCA(t)
	// Caller is sys-relay-pve but the CSR asks for another relay's name.
	csr := makeCSR(t, pki.RelayCommonName("sys-relay-aws"))

	if _, err := issueRelayCert(ca, "sys-relay-pve", csr, time.Hour); err == nil {
		t.Fatal("expected refusal for a CSR asking for another identity, got nil")
	}
}

// An agent-style CN must not be signable through the relay path either: the pin
// is relay-<caller>, so a CSR carrying agent-<caller> is a mismatch.
func TestIssueRelayCert_RefusesAgentIdentity(t *testing.T) {
	ca := testCA(t)
	csr := makeCSR(t, pki.AgentCommonName("sys-relay-pve"))
	if _, err := issueRelayCert(ca, "sys-relay-pve", csr, time.Hour); err == nil {
		t.Fatal("expected refusal for an agent-CN CSR, got nil")
	}
}

func TestIssueRelayCert_RejectsBadCaller(t *testing.T) {
	ca := testCA(t)
	csr := makeCSR(t, pki.RelayCommonName("x"))
	for _, bad := range []string{"", "  ", "-leading", "has space", "semi;colon", "slash/x"} {
		if _, err := issueRelayCert(ca, bad, csr, time.Hour); err == nil {
			t.Fatalf("caller %q should be rejected", bad)
		}
	}
}
