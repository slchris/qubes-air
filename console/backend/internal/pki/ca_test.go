package pki

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func mustCA(t *testing.T) *CA {
	t.Helper()
	ca, err := NewCA("qubes-air-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	return ca
}

func parseLeaf(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatal("certificate is not valid PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}

// TestIssuedCertVerifiesAgainstCA — the point of the exercise. An agent
// certificate that does not chain to the CA cannot authenticate.
func TestIssuedCertVerifiesAgainstCA(t *testing.T) {
	ca := mustCA(t)
	b, err := ca.IssueAgentCert("agent-dev-work", 0)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(b.CAPEM)) {
		t.Fatal("bundle CA is not usable as a trust root")
	}
	leaf := parseLeaf(t, b.CertPEM)

	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("issued certificate must verify for client auth: %v", err)
	}
}

// TestBundleNeverCarriesCAKey is the one that would be catastrophic to get
// wrong. The bundle goes to a host assumed to be compromisable; the CA private
// key in it would let whoever takes that host mint any identity in the fleet.
func TestBundleNeverCarriesCAKey(t *testing.T) {
	ca := mustCA(t)
	b, err := ca.IssueAgentCert("agent-dev-work", 0)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}

	_, caKeyPEM, err := ca.MarshalCA()
	if err != nil {
		t.Fatalf("MarshalCA: %v", err)
	}
	// Compare the key body, since PEM headers are identical for any EC key.
	caKeyBody := strings.ReplaceAll(caKeyPEM, "\n", "")
	caKeyBody = strings.TrimPrefix(caKeyBody, "-----BEGIN EC PRIVATE KEY-----")
	caKeyBody = strings.TrimSuffix(caKeyBody, "-----END EC PRIVATE KEY-----")

	for name, field := range map[string]string{
		"CertPEM": b.CertPEM, "KeyPEM": b.KeyPEM, "CAPEM": b.CAPEM,
	} {
		if strings.Contains(strings.ReplaceAll(field, "\n", ""), caKeyBody) {
			t.Errorf("the CA private key must never appear in the bundle, found in %s", name)
		}
	}
	if strings.Contains(b.CAPEM, "PRIVATE KEY") {
		t.Error("CAPEM must be the certificate only, never a key")
	}
}

// TestIssuedCertCannotSign — an agent certificate must not be usable to issue
// further certificates. A leaked agent key should compromise one agent, not the
// ability to create new ones.
func TestIssuedCertCannotSign(t *testing.T) {
	ca := mustCA(t)
	b, err := ca.IssueAgentCert("agent", 0)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}
	leaf := parseLeaf(t, b.CertPEM)

	if leaf.IsCA {
		t.Error("an agent certificate must not be a CA")
	}
	if leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Error("an agent certificate must not carry CertSign")
	}
}

// TestIssuedCertIsClientAuthOnly — an agent certificate must not let its holder
// impersonate the server side of the tunnel.
func TestIssuedCertIsClientAuthOnly(t *testing.T) {
	ca := mustCA(t)
	b, err := ca.IssueAgentCert("agent", 0)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}
	leaf := parseLeaf(t, b.CertPEM)

	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			t.Error("an agent certificate must not be valid for server auth")
		}
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Errorf("want client auth only, got %v", leaf.ExtKeyUsage)
	}
}

// TestCACannotIssueIntermediates — path length zero stops a signed certificate
// being used to sign others.
func TestCACannotIssueIntermediates(t *testing.T) {
	ca := mustCA(t)
	if !ca.Cert.MaxPathLenZero || ca.Cert.MaxPathLen != 0 {
		t.Error("the CA must be constrained to leaf certificates only")
	}
}

// TestIssuedCertNeverOutlivesCA — a certificate valid past its issuer's expiry
// cannot be verified, and would fail at the least convenient moment.
func TestIssuedCertNeverOutlivesCA(t *testing.T) {
	shortCA, err := NewCA("short-lived", time.Hour)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	// Ask for far longer than the CA has left.
	b, err := shortCA.IssueAgentCert("agent", 365*24*time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}
	leaf := parseLeaf(t, b.CertPEM)

	if leaf.NotAfter.After(shortCA.Cert.NotAfter) {
		t.Errorf("certificate expiry %s outlives its CA's %s", leaf.NotAfter, shortCA.Cert.NotAfter)
	}
}

// TestEachIssuanceIsUnique — two agents must not share an identity, and serials
// must not be guessable or sequential.
func TestEachIssuanceIsUnique(t *testing.T) {
	ca := mustCA(t)
	a, err := ca.IssueAgentCert("agent-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ca.IssueAgentCert("agent-b", 0)
	if err != nil {
		t.Fatal(err)
	}

	if a.Fingerprint == b.Fingerprint {
		t.Error("two issued certificates must have distinct fingerprints")
	}
	if a.KeyPEM == b.KeyPEM {
		t.Error("two agents must never share a private key")
	}
	if parseLeaf(t, a.CertPEM).SerialNumber.Cmp(parseLeaf(t, b.CertPEM).SerialNumber) == 0 {
		t.Error("serial numbers must be unique")
	}
}

// TestCARoundTrip — the CA survives storage and reload, and can still issue
// certificates that verify. Without this, a console restart would invalidate
// every agent.
func TestCARoundTrip(t *testing.T) {
	original := mustCA(t)
	certPEM, keyPEM, err := original.MarshalCA()
	if err != nil {
		t.Fatalf("MarshalCA: %v", err)
	}

	restored, err := ParseCA(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("ParseCA: %v", err)
	}

	b, err := restored.IssueAgentCert("agent-after-restart", 0)
	if err != nil {
		t.Fatalf("issue after restore: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(b.CAPEM))
	if _, err := parseLeaf(t, b.CertPEM).Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("a certificate issued after reload must still verify: %v", err)
	}
}

// TestParseCARejectsNonCA — a leaf certificate presented as the CA would mean
// the console could not sign anything, and should fail loudly at load.
func TestParseCARejectsNonCA(t *testing.T) {
	ca := mustCA(t)
	b, err := ca.IssueAgentCert("agent", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, keyPEM, _ := ca.MarshalCA()

	if _, err := ParseCA(b.CertPEM, keyPEM); err == nil {
		t.Error("a leaf certificate must not be accepted as a CA")
	}
}

func TestParseCARejectsGarbage(t *testing.T) {
	if _, err := ParseCA("not pem", "not pem"); err == nil {
		t.Error("garbage must be rejected")
	}
}
