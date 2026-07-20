// bootstrap_test.go — the agent's half of first-certificate issuance.
//
// The properties pinned here are the ones a green build must not lie about:
// the token is surrendered only through Begin, the private key never appears
// in any wire message, the certificate that comes back must match the pending
// key AND this agent's name AND the cloud-init CA, and once an identity is
// installed the whole surface closes.

package agent

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
)

const testBootstrapToken = "one-shot-token-for-tests"

// pendingBootstrapIdentity lays out what cloud-init delivers under the token
// design: the CA alone. No certificate, no key.
func pendingBootstrapIdentity(t *testing.T) (*Identity, *pki.CA, string) {
	t.Helper()
	ca, err := pki.NewCA("test-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	dir := t.TempDir()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}))
	writeAt(t, filepath.Join(dir, "ca.pem"), caPEM, 0o644)

	id, err := NewPendingIdentity(
		filepath.Join(dir, "agent.pem"),
		filepath.Join(dir, "agent-key.pem"),
		filepath.Join(dir, "ca.pem"),
	)
	if err != nil {
		t.Fatalf("NewPendingIdentity: %v", err)
	}
	if id.HasCertificate() {
		t.Fatalf("a directory holding only a CA produced an identity with a certificate")
	}
	return id, ca, dir
}

func bootstrapService(t *testing.T, id *Identity, onInstalled func()) *BootstrapService {
	t.Helper()
	svc, err := NewBootstrapService(id, "qube-1", testBootstrapToken, onInstalled)
	if err != nil {
		t.Fatalf("NewBootstrapService: %v", err)
	}
	return svc
}

func beginBootstrap(t *testing.T, b *BootstrapService) beginBootstrapResponse {
	t.Helper()
	out, err := b.Begin(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("BeginBootstrap: %v", err)
	}
	var resp beginBootstrapResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("BeginBootstrap reply is not JSON: %v", err)
	}
	return resp
}

func completeBootstrap(t *testing.T, b *BootstrapService, req completeBootstrapRequest) ([]byte, error) {
	t.Helper()
	in, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return b.Complete(context.Background(), "", in)
}

func caPEMOf(t *testing.T, ca *pki.CA) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}))
}

func TestBootstrapBeginSurrendersTokenBesideACSRForThisAgent(t *testing.T) {
	id, _, _ := pendingBootstrapIdentity(t)
	svc := bootstrapService(t, id, nil)

	resp := beginBootstrap(t, svc)

	if resp.Token != testBootstrapToken {
		t.Fatalf("Begin returned token %q, want the one this host was born with", resp.Token)
	}
	if resp.Nonce == "" {
		t.Fatalf("Begin returned no nonce")
	}
	csr := parseCSR(t, resp.CSRPEM)
	if got, want := csr.Subject.CommonName, pki.AgentCommonName("qube-1"); got != want {
		t.Fatalf("CSR names %q, want %q — the console's signer will refuse anything else", got, want)
	}
	if n := svc.pendingCount(); n != 1 {
		t.Fatalf("pendingCount = %d after one Begin, want 1", n)
	}
}

func TestBootstrapRoundTripInstallsAndCloses(t *testing.T) {
	id, ca, dir := pendingBootstrapIdentity(t)
	installed := 0
	svc := bootstrapService(t, id, func() { installed++ })

	// Before: the listener can only offer the placeholder.
	cert, err := svc.ServerCertificate()
	if err != nil {
		t.Fatalf("ServerCertificate before install: %v", err)
	}
	if got := cert.Leaf.Subject.CommonName; got != "bootstrap-qube-1" {
		t.Fatalf("pre-install listener certificate names %q, want the placeholder", got)
	}

	resp := beginBootstrap(t, svc)
	out, err := completeBootstrap(t, svc, completeBootstrapRequest{
		Nonce:   resp.Nonce,
		CertPEM: signCSR(t, ca, resp.CSRPEM),
		CAPEM:   caPEMOf(t, ca),
	})
	if err != nil {
		t.Fatalf("CompleteBootstrap: %v", err)
	}

	var done completeBootstrapResponse
	if err := json.Unmarshal(out, &done); err != nil {
		t.Fatalf("CompleteBootstrap reply is not JSON: %v", err)
	}
	if done.InstalledFingerprint == "" {
		t.Fatalf("no fingerprint reported; the console cannot confirm what was installed")
	}

	if !id.HasCertificate() {
		t.Fatalf("identity still pending after a successful bootstrap")
	}
	leaf, err := id.Leaf()
	if err != nil {
		t.Fatalf("Leaf: %v", err)
	}
	if got, want := leaf.Subject.CommonName, pki.AgentCommonName("qube-1"); got != want {
		t.Fatalf("installed identity names %q, want %q", got, want)
	}
	if got := Fingerprint(leaf); got != done.InstalledFingerprint {
		t.Fatalf("reported fingerprint %q does not match the installed certificate %q", done.InstalledFingerprint, got)
	}

	// The pair reached disk with the modes cloud-init would have used.
	assertMode(t, filepath.Join(dir, "agent.pem"), 0o644)
	assertMode(t, filepath.Join(dir, "agent-key.pem"), 0o600)

	// The listener now presents the real identity — no restart involved.
	cert, err = svc.ServerCertificate()
	if err != nil {
		t.Fatalf("ServerCertificate after install: %v", err)
	}
	if got, want := cert.Leaf.Subject.CommonName, pki.AgentCommonName("qube-1"); got != want {
		t.Fatalf("post-install listener certificate names %q, want %q", got, want)
	}

	if installed != 1 {
		t.Fatalf("onInstalled ran %d times, want exactly once", installed)
	}

	// And the surface is closed: no more tokens leave this process.
	if _, err := svc.Begin(context.Background(), "", nil); !errors.Is(err, ErrAlreadyBootstrapped) {
		t.Fatalf("Begin after install = %v, want ErrAlreadyBootstrapped", err)
	}
	if n := svc.pendingCount(); n != 0 {
		t.Fatalf("pendingCount = %d after install, want 0 — leftover keys must be dropped", n)
	}
}

// The private key must not appear in either wire message. The response to
// Begin carries a CSR (public), and Complete's reply carries a fingerprint.
func TestBootstrapWireMessagesCarryNoPrivateKey(t *testing.T) {
	id, ca, _ := pendingBootstrapIdentity(t)
	svc := bootstrapService(t, id, nil)

	out, err := svc.Begin(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if containsKeyMaterial(string(out)) {
		t.Fatalf("BeginBootstrap reply contains private key material")
	}

	var resp beginBootstrapResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	reply, err := completeBootstrap(t, svc, completeBootstrapRequest{
		Nonce:   resp.Nonce,
		CertPEM: signCSR(t, ca, resp.CSRPEM),
		CAPEM:   caPEMOf(t, ca),
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if containsKeyMaterial(string(reply)) {
		t.Fatalf("CompleteBootstrap reply contains private key material")
	}
}

func containsKeyMaterial(s string) bool {
	return strings.Contains(s, "PRIVATE KEY") || strings.Contains(s, "key_pem")
}

func TestBootstrapCompleteRejectsCertificateForAnotherKey(t *testing.T) {
	id, ca, _ := pendingBootstrapIdentity(t)
	svc := bootstrapService(t, id, nil)
	resp := beginBootstrap(t, svc)

	other := newKey(t)
	_, err := completeBootstrap(t, svc, completeBootstrapRequest{
		Nonce:   resp.Nonce,
		CertPEM: certFor(t, ca, &other.PublicKey, pki.AgentCommonName("qube-1"), time.Hour),
		CAPEM:   caPEMOf(t, ca),
	})
	if !errors.Is(err, ErrBootstrapKeyMismatch) {
		t.Fatalf("Complete with someone else's key = %v, want ErrBootstrapKeyMismatch", err)
	}
	if id.HasCertificate() {
		t.Fatalf("a refused certificate was installed anyway")
	}
}

func TestBootstrapCompleteRejectsAnotherAgentsName(t *testing.T) {
	id, ca, _ := pendingBootstrapIdentity(t)
	svc := bootstrapService(t, id, nil)
	resp := beginBootstrap(t, svc)

	// Same pending key, wrong subject: as if the console signed for the wrong
	// qube. Installing it would make this host unrecognizable to the prober.
	csr := parseCSR(t, resp.CSRPEM)
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("CSR carries a %T", csr.PublicKey)
	}
	_, err := completeBootstrap(t, svc, completeBootstrapRequest{
		Nonce:   resp.Nonce,
		CertPEM: certFor(t, ca, pub, pki.AgentCommonName("qube-2"), time.Hour),
		CAPEM:   caPEMOf(t, ca),
	})
	if !errors.Is(err, ErrBootstrapIdentityMismatch) {
		t.Fatalf("Complete naming another agent = %v, want ErrBootstrapIdentityMismatch", err)
	}
}

func TestBootstrapCompleteRejectsForeignCAMaterial(t *testing.T) {
	id, ca, _ := pendingBootstrapIdentity(t)
	svc := bootstrapService(t, id, nil)
	resp := beginBootstrap(t, svc)

	foreign, err := pki.NewCA("attacker-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, err = completeBootstrap(t, svc, completeBootstrapRequest{
		Nonce:   resp.Nonce,
		CertPEM: signCSR(t, ca, resp.CSRPEM),
		CAPEM:   caPEMOf(t, foreign),
	})
	if !errors.Is(err, ErrUntrustedRenewalCA) {
		t.Fatalf("Complete offering a foreign CA = %v, want ErrUntrustedRenewalCA", err)
	}
}

func TestBootstrapCompleteRejectsBrokenChain(t *testing.T) {
	id, _, _ := pendingBootstrapIdentity(t)
	svc := bootstrapService(t, id, nil)
	resp := beginBootstrap(t, svc)

	// Signed by a CA this agent has never heard of. The offered ca_pem is
	// empty, so the refusal must come from Install's chain verification.
	foreign, err := pki.NewCA("attacker-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, err = completeBootstrap(t, svc, completeBootstrapRequest{
		Nonce:   resp.Nonce,
		CertPEM: signCSR(t, foreign, resp.CSRPEM),
	})
	if !errors.Is(err, ErrUntrustedChain) {
		t.Fatalf("Complete with a foreign-signed certificate = %v, want ErrUntrustedChain", err)
	}
	if id.HasCertificate() {
		t.Fatalf("a certificate outside our chain was installed")
	}
}

func TestBootstrapNonceIsSingleUse(t *testing.T) {
	id, ca, _ := pendingBootstrapIdentity(t)
	svc := bootstrapService(t, id, nil)
	resp := beginBootstrap(t, svc)

	req := completeBootstrapRequest{
		Nonce:   resp.Nonce,
		CertPEM: signCSR(t, ca, resp.CSRPEM),
		CAPEM:   caPEMOf(t, ca),
	}
	if _, err := completeBootstrap(t, svc, req); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if _, err := completeBootstrap(t, svc, req); !errors.Is(err, ErrNoPendingBootstrap) {
		t.Fatalf("second Complete with the same nonce = %v, want ErrNoPendingBootstrap", err)
	}
}

func TestBootstrapPendingIsCapped(t *testing.T) {
	id, _, _ := pendingBootstrapIdentity(t)
	svc := bootstrapService(t, id, nil)

	for range maxPendingBootstraps {
		beginBootstrap(t, svc)
	}
	if _, err := svc.Begin(context.Background(), "", nil); !errors.Is(err, ErrTooManyPendingBootstraps) {
		t.Fatalf("Begin past the cap = %v, want ErrTooManyPendingBootstraps", err)
	}
}

// NewPendingIdentity must only treat a FULLY absent pair as pending. Half a
// pair, or a corrupt one, is a host that had an identity and lost it — an
// operator problem, not a fresh boot.
func TestNewPendingIdentityRefusesHalfAPair(t *testing.T) {
	ca, err := pki.NewCA("test-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	dir := t.TempDir()
	writeAt(t, filepath.Join(dir, "ca.pem"), caPEMOf(t, ca), 0o644)
	// A key with no certificate.
	writeAt(t, filepath.Join(dir, "agent-key.pem"), keyPEMOf(t, newKey(t)), 0o600)

	_, err = NewPendingIdentity(
		filepath.Join(dir, "agent.pem"),
		filepath.Join(dir, "agent-key.pem"),
		filepath.Join(dir, "ca.pem"),
	)
	if err == nil {
		t.Fatalf("half a pair produced a pending identity; corruption is being mistaken for a first boot")
	}
}

// An interrupted install (commit file present, pair files missing) must come
// back as an INSTALLED identity, not as a pending one — its token is already
// spent, so a second bootstrap attempt could never succeed.
func TestNewPendingIdentityRecoversInterruptedInstall(t *testing.T) {
	id, ca, dir := pendingBootstrapIdentity(t)
	svc := bootstrapService(t, id, nil)
	resp := beginBootstrap(t, svc)
	if _, err := completeBootstrap(t, svc, completeBootstrapRequest{
		Nonce:   resp.Nonce,
		CertPEM: signCSR(t, ca, resp.CSRPEM),
		CAPEM:   caPEMOf(t, ca),
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Simulate the crash window: the commit file exists, the materialized pair
	// does not.
	certPath := filepath.Join(dir, "agent.pem")
	keyPath := filepath.Join(dir, "agent-key.pem")
	commit := readAt(t, keyPath) + readAt(t, certPath)
	writeAt(t, keyPath+".commit", commit, 0o600)
	if err := os.Remove(certPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove: %v", err)
	}

	recovered, err := NewPendingIdentity(certPath, keyPath, filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatalf("NewPendingIdentity after interrupted install: %v", err)
	}
	if !recovered.HasCertificate() {
		t.Fatalf("interrupted install came back as pending; a second bootstrap would burn a spent token")
	}
}
