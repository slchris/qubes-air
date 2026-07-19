// Package pki issues the client certificates agents use to authenticate.
//
// The console runs its own CA rather than depending on an external one: the
// only relying party is the console itself, so a public CA would add a third
// party to the trust chain without adding anything. Issuance is paired with
// registration in the agent_certs table — see repository.AgentCertRepository —
// because a CA signature alone grants permanent access, and the registry is
// what makes revocation possible.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// PEM block types.
const (
	blockCertificate = "CERTIFICATE"
	blockECKey       = "EC PRIVATE KEY"
	blockCSR         = "CERTIFICATE REQUEST"
)

// DefaultCALifetime is how long a newly created CA is valid.
//
// Long, because rotating a CA means reissuing every agent certificate; short
// enough that the key does not outlive reasonable cryptographic assumptions.
const DefaultCALifetime = 10 * 365 * 24 * time.Hour

// DefaultAgentCertLifetime is how long an issued agent certificate is valid.
//
// Deliberately not "forever". Even with the registry providing revocation, an
// expiry bounds the damage from a certificate whose loss was never noticed —
// revocation only helps once you know to revoke.
const DefaultAgentCertLifetime = 90 * 24 * time.Hour

// Errors returned by this package.
var (
	ErrNoCA         = errors.New("no CA material available")
	ErrMalformedPEM = errors.New("malformed PEM material")
	// ErrCSRSubjectMismatch means a certificate request asked to be signed under
	// a name other than the one the caller already authenticated. It is a
	// distinct error because it is the signature of an attempt to move sideways
	// through the fleet, not an ordinary malformed input.
	ErrCSRSubjectMismatch = errors.New("certificate request names a different agent")
	// ErrCSRKeyUnsupported means the key inside a request is not one this fleet
	// issues certificates for.
	ErrCSRKeyUnsupported = errors.New("certificate request carries an unsupported public key")
)

// CA is the console's signing authority.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// Bundle is what an agent needs to authenticate: its own certificate and key,
// plus the CA that signs the relay it will talk to.
//
// The CA's PRIVATE key is deliberately absent. Nothing an agent does requires
// it, and an agent runs on a host assumed to be compromisable — shipping it
// would let whoever takes that host mint any identity in the fleet.
type Bundle struct {
	// CertPEM is the agent's client certificate.
	CertPEM string `json:"cert_pem"`
	// KeyPEM is the agent's private key.
	KeyPEM string `json:"key_pem"`
	// CAPEM is the CA certificate, for verifying the peer.
	CAPEM string `json:"ca_pem"`
	// Fingerprint identifies this certificate in the registry, and is what a
	// revocation names.
	Fingerprint string `json:"fingerprint"`
	// NotAfter is when this certificate stops being accepted.
	NotAfter time.Time `json:"not_after"`
}

// SignedCert is a certificate issued from a request, for a key the console
// never saw.
//
// It is a Bundle minus KeyPEM, and that absence is the entire point: the agent
// generated the key and kept it, so renewal does not repeat the weakness of
// IssueAgentCert, where the private key travels to the remote inside cloud-init
// data that anyone holding VM.Config.Cloudinit can read.
type SignedCert struct {
	// CertPEM is the newly signed certificate.
	CertPEM string `json:"cert_pem"`
	// CAPEM is the CA certificate, sent alongside so an agent whose stored copy
	// is missing or stale ends a renewal able to verify its peer.
	CAPEM string `json:"ca_pem"`
	// Fingerprint identifies this certificate in the registry, and is what a
	// revocation names. Same value and same encoding as Bundle.Fingerprint.
	Fingerprint string `json:"fingerprint"`
	// NotAfter is when this certificate stops being accepted.
	NotAfter time.Time `json:"not_after"`
}

// NewCA creates a self-signed CA.
func NewCA(commonName string, lifetime time.Duration) (*CA, error) {
	if lifetime <= 0 {
		lifetime = DefaultCALifetime
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"Qubes Air"}},
		NotBefore:             time.Now().Add(-5 * time.Minute), // tolerate mild clock skew
		NotAfter:              time.Now().Add(lifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		// This CA signs agent certificates and nothing else. Constraining the
		// depth stops a leaked leaf key from being used to sign further certs.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("self-sign CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse CA: %w", err)
	}
	return &CA{Cert: cert, Key: key}, nil
}

// IssueAgentCert signs a client certificate for one agent, generating the key
// pair here.
//
// This is the BOOTSTRAP path, used when a qube has no identity yet and so has no
// authenticated channel to ask over. The cost is real and worth naming: the
// private key exists on the console and travels to the remote through
// cloud-init, so anyone who can read that VM's cloud-init data can read the key.
// Once an agent holds a certificate it renews through SignAgentCSR instead,
// where the key is generated on the remote and never crosses the network.
func (ca *CA) IssueAgentCert(commonName string, lifetime time.Duration) (*Bundle, error) {
	if ca == nil || ca.Cert == nil || ca.Key == nil {
		return nil, ErrNoCA
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate agent key: %w", err)
	}
	cert, der, err := ca.signAgentCert(commonName, lifetime, &key.PublicKey)
	if err != nil {
		return nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal agent key: %w", err)
	}

	return &Bundle{
		CertPEM:     encodePEM(blockCertificate, der),
		KeyPEM:      encodePEM(blockECKey, keyDER),
		CAPEM:       encodePEM(blockCertificate, ca.Cert.Raw),
		Fingerprint: FingerprintOf(cert),
		NotAfter:    cert.NotAfter,
	}, nil
}

// SignAgentCSR renews an agent's certificate from a request it generated itself.
//
// expectedCN is the identity the CALLER already proved, by dialling the agent
// over mTLS and verifying the certificate it presented. Everything here hangs
// off that: the console is not deciding who the requester is, it is refusing to
// sign anything that disagrees with who the requester already turned out to be.
//
// A request naming a different agent is refused rather than corrected. Rewriting
// it to the expected name would produce a working certificate and destroy the
// only evidence that something asked for another agent's identity — which is an
// attempt to move sideways through the fleet, not a typo to be helpful about.
func (ca *CA) SignAgentCSR(csrPEM, expectedCN string, lifetime time.Duration) (*SignedCert, error) {
	if ca == nil || ca.Cert == nil || ca.Key == nil {
		return nil, ErrNoCA
	}

	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != blockCSR {
		return nil, fmt.Errorf("%w: certificate request", ErrMalformedPEM)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate request: %w", err)
	}

	// A request is signed by the private key matching the public key inside it,
	// which is the only proof that the requester actually holds that key.
	// Skipping this check would let anything paste in a public key it does not
	// control and have an agent identity bound to it — after which whoever DOES
	// hold the matching private key can authenticate as that agent.
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("certificate request signature is invalid: %w", err)
	}

	// Exact comparison, deliberately not case-insensitive or normalized. These
	// names are derived from qube names, and two qubes whose names differ only
	// in case are two different qubes; folding them together would make the
	// check pass for the wrong one.
	if csr.Subject.CommonName != expectedCN {
		return nil, fmt.Errorf("%w: dialled %q but the request asks for %q",
			ErrCSRSubjectMismatch, expectedCN, csr.Subject.CommonName)
	}

	if err := checkAgentPublicKey(csr.PublicKey); err != nil {
		return nil, err
	}

	// The public key is the ONLY thing taken from the request. Serial, validity,
	// key usage and extended key usage all come from signAgentCert, so any
	// extension or attribute the request asked for — a SAN, basic constraints
	// claiming CA, a longer life — is dropped by construction rather than by
	// remembering to filter it. A CSR is untrusted input that happens to carry a
	// signature; the signature proves who sent it, not that anything it asks for
	// is allowed.
	cert, der, err := ca.signAgentCert(expectedCN, lifetime, csr.PublicKey)
	if err != nil {
		return nil, err
	}

	return &SignedCert{
		CertPEM:     encodePEM(blockCertificate, der),
		CAPEM:       encodePEM(blockCertificate, ca.Cert.Raw),
		Fingerprint: FingerprintOf(cert),
		NotAfter:    cert.NotAfter,
	}, nil
}

// signAgentCert produces the one and only shape of agent certificate this CA
// issues, for a public key that arrived from anywhere.
//
// Both issuance paths go through here so that a renewed agent is
// indistinguishable from a freshly provisioned one. That property is structural
// rather than a matter of keeping two templates in step: if each path built its
// own, they would drift, and a renewal would quietly change what an agent is
// permitted to do — the kind of difference nobody finds until a certificate that
// should work does not.
func (ca *CA) signAgentCert(commonName string, lifetime time.Duration, pub any) (*x509.Certificate, []byte, error) {
	if lifetime <= 0 {
		lifetime = DefaultAgentCertLifetime
	}
	// Never outlive the CA: a certificate valid past its issuer's expiry cannot
	// be verified, and fails at the least convenient moment.
	notAfter := time.Now().Add(lifetime)
	if notAfter.After(ca.Cert.NotAfter) {
		notAfter = ca.Cert.NotAfter
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName, Organization: []string{"Qubes Air Agent"}},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		// Client auth only. An agent certificate must not be usable to
		// impersonate a server, and must not be able to sign anything.
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("sign agent cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("parse agent cert: %w", err)
	}
	return cert, der, nil
}

// checkAgentPublicKey refuses keys this fleet does not issue certificates for.
//
// The request's self-signature only proves the requester holds the matching
// private key — a deliberately weak key satisfies it perfectly well. Since every
// agent the console provisions gets P-256, a renewal arriving with anything else
// is either an agent built against a different contract or a request to be bound
// to a key whose signatures somebody else can produce, and neither should be
// signed on the strength of a valid mTLS session alone.
func checkAgentPublicKey(pub any) error {
	key, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("%w: want an ECDSA key, got %T", ErrCSRKeyUnsupported, pub)
	}
	switch key.Curve {
	case elliptic.P256(), elliptic.P384(), elliptic.P521():
		return nil
	case nil:
		return fmt.Errorf("%w: ECDSA key names no curve", ErrCSRKeyUnsupported)
	default:
		return fmt.Errorf("%w: unsupported curve %s", ErrCSRKeyUnsupported, key.Curve.Params().Name)
	}
}

// FingerprintOf returns the registry key for a certificate. It must match
// repository.Fingerprint — the two are compared across the TLS boundary.
func FingerprintOf(cert *x509.Certificate) string {
	return sha256Hex(cert.Raw)
}

// MarshalCA serializes the CA for storage, returning cert and key PEM.
//
// The key is returned separately so a caller cannot accidentally persist it
// alongside the certificate in somewhere only the certificate belongs.
func (ca *CA) MarshalCA() (certPEM, keyPEM string, err error) {
	if ca == nil || ca.Cert == nil || ca.Key == nil {
		return "", "", ErrNoCA
	}
	keyDER, err := x509.MarshalECPrivateKey(ca.Key)
	if err != nil {
		return "", "", fmt.Errorf("marshal CA key: %w", err)
	}
	return encodePEM(blockCertificate, ca.Cert.Raw), encodePEM(blockECKey, keyDER), nil
}

// ParseCA reconstructs a CA from stored PEM material.
func ParseCA(certPEM, keyPEM string) (*CA, error) {
	certBlock, _ := pem.Decode([]byte(certPEM))
	if certBlock == nil || certBlock.Type != blockCertificate {
		return nil, fmt.Errorf("%w: CA certificate", ErrMalformedPEM)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("stored certificate is not a CA")
	}

	keyBlock, _ := pem.Decode([]byte(keyPEM))
	if keyBlock == nil || keyBlock.Type != blockECKey {
		return nil, fmt.Errorf("%w: CA key", ErrMalformedPEM)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA key: %w", err)
	}
	return &CA{Cert: cert, Key: key}, nil
}

// randomSerial produces a serial number with enough entropy that two issuers
// cannot collide, per RFC 5280's recommendation against sequential serials.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}

// sha256Hex is the fingerprint encoding shared with repository.Fingerprint.
// The two MUST agree: one produces the value at issuance, the other consumes it
// at TLS verification, and a mismatch would reject every agent.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func encodePEM(blockType string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}
