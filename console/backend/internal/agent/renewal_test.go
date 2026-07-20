package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
)

// begin runs qubesair.BeginRenewal and decodes the reply.
func begin(t *testing.T, r *RenewalService) beginRenewalResponse {
	t.Helper()
	out, err := r.Begin(context.Background(), "console", nil)
	if err != nil {
		t.Fatalf("BeginRenewal: %v", err)
	}
	var resp beginRenewalResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode BeginRenewal reply: %v", err)
	}
	return resp
}

// complete runs qubesair.CompleteRenewal.
func complete(t *testing.T, r *RenewalService, req completeRenewalRequest) ([]byte, error) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return r.Complete(context.Background(), "console", body)
}

// parseCSR decodes a CSR and checks its self-signature, which is the console's
// first act on receiving one.
func parseCSR(t *testing.T, csrPEM string) *x509.CertificateRequest {
	t.Helper()
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("BeginRenewal did not return a CSR: %q", csrPEM)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatalf("CSR is not signed by the key it carries: %v", err)
	}
	return csr
}

// signCSR is the console's half: sign what the agent asked for.
func signCSR(t *testing.T, ca *pki.CA, csrPEM string) string {
	t.Helper()
	csr := parseCSR(t, csrPEM)
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("CSR carries a %T, not an ECDSA key", csr.PublicKey)
	}
	return certFor(t, ca, pub, csr.Subject.CommonName, 90*24*time.Hour)
}

// TestBeginProducesCSRForThisAgent — the agent does not get to pick its own
// name. The console holds the peer's common name to "agent-<qube>", so a CSR
// asking for anything else would either be refused or produce an identity the
// console can no longer recognize.
func TestBeginProducesCSRForThisAgent(t *testing.T) {
	id, _, _ := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, 0)

	resp := begin(t, r)
	if resp.Nonce == "" {
		t.Fatal("BeginRenewal returned no nonce")
	}
	csr := parseCSR(t, resp.CSRPEM)
	if csr.Subject.CommonName != testAgentCN {
		t.Errorf("CSR names %q, want %q", csr.Subject.CommonName, testAgentCN)
	}
	if _, ok := csr.PublicKey.(*ecdsa.PublicKey); !ok {
		t.Errorf("CSR key is %T; pki.IssueAgentCert uses ECDSA P-256 and renewal must match", csr.PublicKey)
	}
	if r.pendingCount() != 1 {
		t.Errorf("want 1 pending renewal, got %d", r.pendingCount())
	}
}

// TestRenewalRoundTrip is the whole point: a certificate replaced over the
// channel the agent already holds, with the private key never leaving the host.
func TestRenewalRoundTrip(t *testing.T) {
	id, ca, dir := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, 0)
	oldLeaf, _ := id.Leaf()
	oldKeyOnDisk := readAt(t, filepath.Join(dir, "agent-key.pem"))

	started := begin(t, r)
	certPEM := signCSR(t, ca, started.CSRPEM)

	out, err := complete(t, r, completeRenewalRequest{
		Nonce:   started.Nonce,
		CertPEM: certPEM,
		CAPEM:   readAt(t, filepath.Join(dir, "ca.pem")),
	})
	if err != nil {
		t.Fatalf("CompleteRenewal: %v", err)
	}

	var resp completeRenewalResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode CompleteRenewal reply: %v", err)
	}

	newLeaf, err := id.Leaf()
	if err != nil {
		t.Fatal(err)
	}
	if resp.InstalledFingerprint != Fingerprint(newLeaf) {
		t.Error("the reported fingerprint is not the certificate that was installed")
	}
	if resp.InstalledFingerprint == Fingerprint(oldLeaf) {
		t.Error("the certificate did not change")
	}
	if !resp.NotAfter.Equal(newLeaf.NotAfter.UTC()) {
		t.Errorf("reported not_after %s, installed %s", resp.NotAfter, newLeaf.NotAfter.UTC())
	}
	if !newLeaf.NotAfter.After(oldLeaf.NotAfter) {
		t.Error("renewal did not extend the expiry")
	}
	if newLeaf.Subject.CommonName != testAgentCN {
		t.Errorf("renewal changed the agent's name to %q", newLeaf.Subject.CommonName)
	}

	// The key on disk must be the NEW one: the agent generated it, so a key
	// that did not change means the console's key traveled instead.
	if readAt(t, filepath.Join(dir, "agent-key.pem")) == oldKeyOnDisk {
		t.Error("the private key on disk did not change, so it was not generated here")
	}
	if _, err := loadPair(filepath.Join(dir, "agent.pem"), filepath.Join(dir, "agent-key.pem")); err != nil {
		t.Fatalf("the installed pair does not load, so the agent would not restart: %v", err)
	}
	if r.pendingCount() != 0 {
		t.Errorf("pending key was not released: %d still held", r.pendingCount())
	}
}

// TestCompleteRejectsCertificateForAnotherKey: the certificate is CA-signed and
// names this agent, but belongs to a key the agent does not hold. Writing it
// would produce a host that cannot present an identity after its next restart.
func TestCompleteRejectsCertificateForAnotherKey(t *testing.T) {
	id, ca, dir := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, 0)
	before := snapshot(t, dir)

	started := begin(t, r)
	stranger := newKey(t)
	certPEM := certFor(t, ca, &stranger.PublicKey, testAgentCN, time.Hour)

	_, err := complete(t, r, completeRenewalRequest{Nonce: started.Nonce, CertPEM: certPEM})
	if !errors.Is(err, ErrRenewalKeyMismatch) {
		t.Fatalf("want ErrRenewalKeyMismatch, got %v", err)
	}
	assertUnchanged(t, dir, before)
}

// TestCompleteRejectsBrokenChain — a certificate signed by anything other than
// the CA this agent already trusts.
func TestCompleteRejectsBrokenChain(t *testing.T) {
	id, _, dir := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, 0)
	before := snapshot(t, dir)

	foreign, err := pki.NewCA("attacker-ca", 0)
	if err != nil {
		t.Fatal(err)
	}
	started := begin(t, r)
	certPEM := signCSR(t, foreign, started.CSRPEM)

	_, err = complete(t, r, completeRenewalRequest{Nonce: started.Nonce, CertPEM: certPEM})
	if !errors.Is(err, ErrUntrustedChain) {
		t.Fatalf("want ErrUntrustedChain, got %v", err)
	}
	assertUnchanged(t, dir, before)
	if leaf, _ := id.Leaf(); leaf.Subject.CommonName != testAgentCN {
		t.Error("the previous certificate stopped working after a rejected renewal")
	}
}

// TestCompleteRejectsIdentityChange: a correctly signed certificate for the
// wrong name. Installing it would make the agent invisible to the very probe
// that checks whether it is healthy — it holds the peer to "agent-<qube>".
func TestCompleteRejectsIdentityChange(t *testing.T) {
	id, ca, dir := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, 0)
	before := snapshot(t, dir)

	started := begin(t, r)
	csr := parseCSR(t, started.CSRPEM)
	pub := csr.PublicKey.(*ecdsa.PublicKey)
	certPEM := certFor(t, ca, pub, "agent-someone-else", time.Hour)

	_, err := complete(t, r, completeRenewalRequest{Nonce: started.Nonce, CertPEM: certPEM})
	if !errors.Is(err, ErrRenewalIdentityMismatch) {
		t.Fatalf("want ErrRenewalIdentityMismatch, got %v", err)
	}
	assertUnchanged(t, dir, before)
}

// TestCompleteRejectsForeignCAMaterial: the console authenticated to us under
// our CA, which is not authority to replace it. A renewal that could swap the
// trust root would be a takeover of every future peer check.
func TestCompleteRejectsForeignCAMaterial(t *testing.T) {
	id, ca, dir := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, 0)
	before := snapshot(t, dir)

	foreign, err := pki.NewCA("attacker-ca", 0)
	if err != nil {
		t.Fatal(err)
	}
	foreignPEM, _, err := foreign.MarshalCA()
	if err != nil {
		t.Fatal(err)
	}

	started := begin(t, r)
	certPEM := signCSR(t, ca, started.CSRPEM) // properly signed…

	_, err = complete(t, r, completeRenewalRequest{
		Nonce:   started.Nonce,
		CertPEM: certPEM,
		CAPEM:   foreignPEM, // …but shipped with a CA we do not trust
	})
	if !errors.Is(err, ErrUntrustedRenewalCA) {
		t.Fatalf("want ErrUntrustedRenewalCA, got %v", err)
	}
	assertUnchanged(t, dir, before)
}

// TestNonceIsSingleUse — a nonce survives exactly one CompleteRenewal, whether
// or not it succeeded.
func TestNonceIsSingleUse(t *testing.T) {
	id, ca, _ := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, 0)

	started := begin(t, r)
	certPEM := signCSR(t, ca, started.CSRPEM)
	if _, err := complete(t, r, completeRenewalRequest{Nonce: started.Nonce, CertPEM: certPEM}); err != nil {
		t.Fatalf("first CompleteRenewal: %v", err)
	}
	_, err := complete(t, r, completeRenewalRequest{Nonce: started.Nonce, CertPEM: certPEM})
	if !errors.Is(err, ErrNoPendingRenewal) {
		t.Fatalf("replaying a nonce must fail, got %v", err)
	}

	// And a failed attempt consumes it too.
	started = begin(t, r)
	if _, err := complete(t, r, completeRenewalRequest{Nonce: started.Nonce, CertPEM: "junk"}); err == nil {
		t.Fatal("junk certificate was accepted")
	}
	certPEM = signCSR(t, ca, started.CSRPEM)
	_, err = complete(t, r, completeRenewalRequest{Nonce: started.Nonce, CertPEM: certPEM})
	if !errors.Is(err, ErrNoPendingRenewal) {
		t.Fatalf("a nonce must not survive a failed attempt, got %v", err)
	}
}

// TestUnknownNonceRejected covers the nonce that was never issued at all.
func TestUnknownNonceRejected(t *testing.T) {
	id, _, _ := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, 0)

	for _, nonce := range []string{"", "deadbeef", "not-hex-at-all\nforged log line"} {
		_, err := complete(t, r, completeRenewalRequest{Nonce: nonce, CertPEM: "x"})
		if !errors.Is(err, ErrNoPendingRenewal) {
			t.Errorf("nonce %q: want ErrNoPendingRenewal, got %v", nonce, err)
		}
	}
}

// TestPendingKeysExpire — an abandoned renewal must not pin private key
// material in memory for the rest of the agent's uptime.
func TestPendingKeysExpire(t *testing.T) {
	id, ca, _ := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, 20*time.Millisecond)

	started := begin(t, r)
	certPEM := signCSR(t, ca, started.CSRPEM)

	deadline := time.Now().Add(2 * time.Second)
	for r.pendingCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("pending renewal never expired; its key is pinned for the process lifetime")
		}
		time.Sleep(5 * time.Millisecond)
	}

	_, err := complete(t, r, completeRenewalRequest{Nonce: started.Nonce, CertPEM: certPEM})
	if !errors.Is(err, ErrNoPendingRenewal) {
		t.Fatalf("an expired renewal must not complete, got %v", err)
	}
}

// TestPendingRenewalsAreCapped — the pending table is the one thing a caller
// can make grow, so it has a ceiling. Refusing beats evicting: eviction would
// let a caller that keeps starting renewals push out the one about to finish.
func TestPendingRenewalsAreCapped(t *testing.T) {
	id, ca, _ := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, time.Minute)

	first := begin(t, r)
	for i := 1; i < maxPendingRenewals; i++ {
		begin(t, r)
	}
	if _, err := r.Begin(context.Background(), "console", nil); !errors.Is(err, ErrTooManyPendingRenewals) {
		t.Fatalf("want ErrTooManyPendingRenewals, got %v", err)
	}

	// The renewal already in flight is untouched and still completes.
	certPEM := signCSR(t, ca, first.CSRPEM)
	if _, err := complete(t, r, completeRenewalRequest{Nonce: first.Nonce, CertPEM: certPEM}); err != nil {
		t.Fatalf("a renewal in flight was lost when the table filled: %v", err)
	}
}

// TestCompleteRejectsMalformedRequest — the body arrives over the network.
func TestCompleteRejectsMalformedRequest(t *testing.T) {
	id, _, _ := installedIdentity(t, testAgentCN)
	r := NewRenewalService(id, 0)

	if _, err := r.Complete(context.Background(), "console", []byte("{oops")); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
}
