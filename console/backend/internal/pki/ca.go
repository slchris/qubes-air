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

// IssueAgentCert signs a client certificate for one agent.
//
// A fresh key pair is generated here rather than having the agent produce a CSR.
// That is a real trade-off and worth naming: the private key exists on the
// console and travels to the remote, typically through cloud-init, so anyone who
// can read that VM's cloud-init data can read the key. The alternative — the
// agent generating its own key and submitting a CSR — needs a bootstrap channel
// authenticated some other way, which does not exist yet. Given the remote is
// assumed compromisable anyway, and the registry bounds the damage by making
// revocation immediate, this is the pragmatic starting point rather than the
// end state.
func (ca *CA) IssueAgentCert(commonName string, lifetime time.Duration) (*Bundle, error) {
	if ca == nil || ca.Cert == nil || ca.Key == nil {
		return nil, ErrNoCA
	}
	if lifetime <= 0 {
		lifetime = DefaultAgentCertLifetime
	}
	// Never outlive the CA: a certificate valid past its issuer's expiry cannot
	// be verified, and fails at the least convenient moment.
	notAfter := time.Now().Add(lifetime)
	if notAfter.After(ca.Cert.NotAfter) {
		notAfter = ca.Cert.NotAfter
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate agent key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
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

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("sign agent cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse agent cert: %w", err)
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
