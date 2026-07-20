package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"
)

// makeCSR builds a certificate request the way an agent would: a fresh key that
// never leaves this function, and a request carrying only the public half.
func makeCSR(t *testing.T, cn string, tmpl *x509.CertificateRequest) (string, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if tmpl == nil {
		tmpl = &x509.CertificateRequest{}
	}
	tmpl.Subject = pkix.Name{CommonName: cn}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: blockCSR, Bytes: der})), key
}

// TestSignedCSRVerifiesAgainstCA — the renewal path end to end. A certificate
// that does not chain to the CA cannot authenticate, and one bound to a key the
// agent does not hold cannot be used at all.
func TestSignedCSRVerifiesAgainstCA(t *testing.T) {
	ca := mustCA(t)
	csrPEM, key := makeCSR(t, "agent-dev-work", nil)

	signed, err := ca.SignAgentCSR(csrPEM, "agent-dev-work", 0)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(signed.CAPEM)) {
		t.Fatal("returned CA is not usable as a trust root")
	}
	leaf := parseLeaf(t, signed.CertPEM)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("a renewed certificate must verify for client auth: %v", err)
	}

	// The certificate must be bound to the key the AGENT holds. If it were bound
	// to anything else the agent would install a certificate it cannot present.
	got, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("renewed certificate carries a %T", leaf.PublicKey)
	}
	if !got.Equal(&key.PublicKey) {
		t.Error("the renewed certificate is not bound to the requester's key")
	}
	if signed.Fingerprint != FingerprintOf(leaf) {
		t.Error("the returned fingerprint must be the registry key for the returned certificate")
	}
	if !signed.NotAfter.Equal(leaf.NotAfter) {
		t.Errorf("NotAfter %s disagrees with the certificate's %s", signed.NotAfter, leaf.NotAfter)
	}
}

// TestSignAgentCSRRefusesMismatchedCommonName is the escalation check. The peer
// proved one identity by mTLS; a request for a different one is an attempt to
// obtain another agent's credential, and must fail loudly rather than be
// silently corrected to the name it was allowed to have.
func TestSignAgentCSRRefusesMismatchedCommonName(t *testing.T) {
	ca := mustCA(t)
	csrPEM, _ := makeCSR(t, "agent-vault", nil)

	signed, err := ca.SignAgentCSR(csrPEM, "agent-dev-work", 0)
	if err == nil {
		t.Fatal("a request naming a different agent must be refused, got a certificate")
	}
	if signed != nil {
		t.Error("nothing must be returned alongside the refusal")
	}
	if !errors.Is(err, ErrCSRSubjectMismatch) {
		t.Errorf("want ErrCSRSubjectMismatch, got %v", err)
	}
	// Both names must appear: whoever reads this needs to know which agent was
	// dialed and which one was asked for, or the log says nothing useful.
	for _, want := range []string{"agent-vault", "agent-dev-work"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q, got: %v", want, err)
		}
	}
}

// TestSignAgentCSRDoesNotFixUpTheName — a mismatch must not quietly produce a
// certificate for the expected name. That would work, which is exactly why it
// would never be noticed.
func TestSignAgentCSRDoesNotFixUpTheName(t *testing.T) {
	ca := mustCA(t)
	// Differs only in case, the most tempting thing to normalize away.
	csrPEM, _ := makeCSR(t, "Agent-Dev-Work", nil)

	if _, err := ca.SignAgentCSR(csrPEM, "agent-dev-work", 0); !errors.Is(err, ErrCSRSubjectMismatch) {
		t.Errorf("names differing only in case are different qubes; want refusal, got %v", err)
	}
}

// TestSignAgentCSRRefusesBrokenSelfSignature — the self-signature is the only
// proof the requester holds the private key. Without the check, anything could
// submit a public key it does not control and have an agent identity bound to
// it; whoever held the matching private key could then authenticate as that
// agent.
func TestSignAgentCSRRefusesBrokenSelfSignature(t *testing.T) {
	ca := mustCA(t)
	csrPEM, _ := makeCSR(t, "agent-dev-work", nil)

	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		t.Fatal("test CSR is not valid PEM")
	}
	tampered := append([]byte(nil), block.Bytes...)
	// The signature sits at the end of the DER, so flipping the last byte breaks
	// it while leaving the structure parseable — which is the case that matters.
	// A request that failed to parse would be rejected for the wrong reason.
	tampered[len(tampered)-1] ^= 0xff
	if _, err := x509.ParseCertificateRequest(tampered); err != nil {
		t.Fatalf("tampered request must still parse, so the signature check is what rejects it: %v", err)
	}
	tamperedPEM := string(pem.EncodeToMemory(&pem.Block{Type: blockCSR, Bytes: tampered}))

	if _, err := ca.SignAgentCSR(tamperedPEM, "agent-dev-work", 0); err == nil {
		t.Fatal("a request with an invalid self-signature must be refused")
	}
}

// TestSignAgentCSRRefusesKeySubstitution — the concrete attack the signature
// check stops: take a legitimate request and swap in somebody else's public key,
// keeping the name. The signature no longer matches the key, and signing anyway
// would issue that agent's identity to a key it does not hold.
func TestSignAgentCSRRefusesKeySubstitution(t *testing.T) {
	ca := mustCA(t)
	victimPEM, _ := makeCSR(t, "agent-dev-work", nil)
	attackerPEM, _ := makeCSR(t, "agent-dev-work", nil)

	victimBlock, _ := pem.Decode([]byte(victimPEM))
	attackerBlock, _ := pem.Decode([]byte(attackerPEM))
	victim, err := x509.ParseCertificateRequest(victimBlock.Bytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	attacker, err := x509.ParseCertificateRequest(attackerBlock.Bytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Splice the attacker's public key bytes over the victim's, leaving the
	// victim's signature in place.
	spliced := strings.Replace(string(victim.Raw),
		string(victim.RawSubjectPublicKeyInfo), string(attacker.RawSubjectPublicKeyInfo), 1)
	if spliced == string(victim.Raw) {
		t.Skip("could not splice the key bytes; the signature check is covered by the tamper test")
	}
	splicedPEM := string(pem.EncodeToMemory(&pem.Block{Type: blockCSR, Bytes: []byte(spliced)}))

	if _, err := ca.SignAgentCSR(splicedPEM, "agent-dev-work", 0); err == nil {
		t.Fatal("a request whose key was substituted must be refused")
	}
}

// TestSignAgentCSRIgnoresRequestedExtensions — a request is untrusted input that
// happens to be signed. Anything it asks for beyond the public key must be
// dropped, or an agent could ask to be a CA, or to be valid for server auth, and
// renew its way into privileges it was never issued.
func TestSignAgentCSRIgnoresRequestedExtensions(t *testing.T) {
	ca := mustCA(t)
	csrPEM, _ := makeCSR(t, "agent-dev-work", &x509.CertificateRequest{
		DNSNames:       []string{"console.internal", "*.qubes-air"},
		EmailAddresses: []string{"root@example.com"},
	})

	signed, err := ca.SignAgentCSR(csrPEM, "agent-dev-work", 0)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	leaf := parseLeaf(t, signed.CertPEM)

	if len(leaf.DNSNames) != 0 {
		t.Errorf("requested DNS names must not be copied, got %v", leaf.DNSNames)
	}
	if len(leaf.EmailAddresses) != 0 {
		t.Errorf("requested email addresses must not be copied, got %v", leaf.EmailAddresses)
	}
	if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Error("a renewed certificate must never be able to sign others")
	}
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			t.Error("a renewed certificate must not be valid for server auth")
		}
	}
	if leaf.Subject.CommonName != "agent-dev-work" {
		t.Errorf("subject must be rebuilt from the expected name, got %q", leaf.Subject.CommonName)
	}
}

// TestRenewedCertHasSameShapeAsIssued — the property that makes renewal safe to
// roll out: an agent that renewed must be indistinguishable from one freshly
// provisioned. If the two shapes drifted, renewal would quietly change what an
// agent is permitted to do.
func TestRenewedCertHasSameShapeAsIssued(t *testing.T) {
	ca := mustCA(t)
	fresh, err := ca.IssueAgentCert("agent-dev-work", 0)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}
	csrPEM, _ := makeCSR(t, "agent-dev-work", nil)
	renewed, err := ca.SignAgentCSR(csrPEM, "agent-dev-work", 0)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}

	a, b := parseLeaf(t, fresh.CertPEM), parseLeaf(t, renewed.CertPEM)

	if a.KeyUsage != b.KeyUsage {
		t.Errorf("key usage differs: issued %v, renewed %v", a.KeyUsage, b.KeyUsage)
	}
	if len(a.ExtKeyUsage) != len(b.ExtKeyUsage) {
		t.Fatalf("extended key usage differs: issued %v, renewed %v", a.ExtKeyUsage, b.ExtKeyUsage)
	}
	for i := range a.ExtKeyUsage {
		if a.ExtKeyUsage[i] != b.ExtKeyUsage[i] {
			t.Errorf("extended key usage differs: issued %v, renewed %v", a.ExtKeyUsage, b.ExtKeyUsage)
		}
	}
	if a.Subject.String() != b.Subject.String() {
		t.Errorf("subject differs: issued %q, renewed %q", a.Subject, b.Subject)
	}
	if a.Issuer.String() != b.Issuer.String() {
		t.Errorf("issuer differs: issued %q, renewed %q", a.Issuer, b.Issuer)
	}
	if a.IsCA != b.IsCA || a.BasicConstraintsValid != b.BasicConstraintsValid {
		t.Error("basic constraints differ between issuance and renewal")
	}
	if a.SerialNumber.Cmp(b.SerialNumber) == 0 {
		t.Error("a renewal must not reuse the serial of another certificate")
	}
}

// TestSignedCSRNeverOutlivesCA — a certificate valid past its issuer's expiry
// cannot be verified. Renewal must obey the same clamp as issuance, otherwise
// the renewal path becomes the one that produces unverifiable certificates.
func TestSignedCSRNeverOutlivesCA(t *testing.T) {
	shortCA, err := NewCA("short-lived", time.Hour)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	csrPEM, _ := makeCSR(t, "agent", nil)

	signed, err := shortCA.SignAgentCSR(csrPEM, "agent", 365*24*time.Hour)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	if parseLeaf(t, signed.CertPEM).NotAfter.After(shortCA.Cert.NotAfter) {
		t.Error("a renewed certificate must not outlive its CA")
	}
}

// TestSignedCertNeverCarriesAnyKey — the renewal response travels to a host
// assumed to be compromisable, and by construction contains no private key at
// all: the agent's never left it, and the CA's must never leave the console.
func TestSignedCertNeverCarriesAnyKey(t *testing.T) {
	ca := mustCA(t)
	csrPEM, _ := makeCSR(t, "agent", nil)
	signed, err := ca.SignAgentCSR(csrPEM, "agent", 0)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}

	for name, field := range map[string]string{"CertPEM": signed.CertPEM, "CAPEM": signed.CAPEM} {
		if strings.Contains(field, "PRIVATE KEY") {
			t.Errorf("%s must never contain a private key", name)
		}
	}
}

// TestSignAgentCSRRefusesWeakKey — the self-signature proves possession, not
// strength: a deliberately weak key satisfies it perfectly. Binding an agent
// identity to a key somebody else can forge signatures for defeats the point of
// having certificates at all.
func TestSignAgentCSRRefusesWeakKey(t *testing.T) {
	ca := mustCA(t)
	key, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		t.Skipf("P-224 unavailable in this build: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "agent"}}, key)
	if err != nil {
		t.Skipf("P-224 requests unsupported in this build: %v", err)
	}
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: blockCSR, Bytes: der}))

	if _, err := ca.SignAgentCSR(csrPEM, "agent", 0); !errors.Is(err, ErrCSRKeyUnsupported) {
		t.Errorf("want ErrCSRKeyUnsupported for an off-policy curve, got %v", err)
	}
}

// TestSignAgentCSRRejectsMalformedInput — an agent that sends nonsense must get
// an error, not a certificate for whatever could be salvaged.
func TestSignAgentCSRRejectsMalformedInput(t *testing.T) {
	ca := mustCA(t)
	bundle, err := ca.IssueAgentCert("agent", 0)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"not PEM at all":               "hello",
		"empty":                        "",
		"a certificate, not a request": bundle.CertPEM,
		"a private key":                bundle.KeyPEM,
		"correct block, garbage body": string(pem.EncodeToMemory(
			&pem.Block{Type: blockCSR, Bytes: []byte("nope")})),
	}
	for name, input := range cases {
		if _, err := ca.SignAgentCSR(input, "agent", 0); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// TestSignAgentCSRWithoutCA — an issuer with no key must refuse rather than
// panic, since the CA is loaded lazily and may be absent.
func TestSignAgentCSRWithoutCA(t *testing.T) {
	csrPEM, _ := makeCSR(t, "agent", nil)
	var nilCA *CA
	if _, err := nilCA.SignAgentCSR(csrPEM, "agent", 0); !errors.Is(err, ErrNoCA) {
		t.Errorf("want ErrNoCA, got %v", err)
	}
	if _, err := (&CA{}).SignAgentCSR(csrPEM, "agent", 0); !errors.Is(err, ErrNoCA) {
		t.Errorf("want ErrNoCA for a half-built CA, got %v", err)
	}
}
