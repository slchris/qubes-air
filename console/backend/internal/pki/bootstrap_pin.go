package pki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"time"
)

// A qube that has no certificate yet still has to answer a TLS handshake: the
// console dials it to trade the one-shot bootstrap token for a first identity.
// The listener therefore presents a self-signed PLACEHOLDER, and this file is
// both halves of how the console authenticates it anyway.
//
// The placeholder's key is not random. It is derived from the bootstrap token
// and the qube's name, so the console — which minted the token — can compute
// the matching public key when it issues the token, store only its SPKI digest
// (the "pin"), and refuse any peer that does not hold the derived private key.
// Nothing secret is stored or sent to do this: the token never leaves the guest
// before the pinned handshake completes, and the pin is a hash of a public key.
//
// Minting and verifying live side by side for the same reason AgentCommonName
// does: the agent mints what the console verifies, and two definitions of the
// name, the key derivation or the role would drift into a bootstrap that fails
// on every qube with an error neither side can explain.

// bootstrapPinSalt domain-separates the placeholder key from any other use of
// the token. Changing it changes every pin, so it is versioned rather than
// edited.
const bootstrapPinSalt = "qubes-air bootstrap placeholder key v1"

// bootstrapPlaceholderBackdate is how far before "now" a placeholder claims to
// be valid. A first boot's clock may still be settling, and a placeholder that
// is "not yet valid" to the console reads as a dead VM from outside.
const bootstrapPlaceholderBackdate = time.Hour

// BootstrapPlaceholderCommonName is the subject common name of the placeholder
// a qube presents before it has an identity.
func BootstrapPlaceholderCommonName(qubeName string) string { return "bootstrap-" + qubeName }

// BootstrapPlaceholderPrivateKey derives the placeholder listener key from the
// one-shot token, scoped to one qube name. Both sides compute it; neither
// persists it.
func BootstrapPlaceholderPrivateKey(token, qubeName string) (ed25519.PrivateKey, error) {
	if token == "" || qubeName == "" {
		return nil, errors.New("pki: bootstrap placeholder key needs a token and a qube name")
	}
	seed, err := hkdf.Key(sha256.New, []byte(token), []byte(bootstrapPinSalt), "qube="+qubeName, ed25519.SeedSize)
	if err != nil {
		return nil, fmt.Errorf("pki: derive bootstrap placeholder key: %w", err)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// BootstrapPlaceholderSPKIFingerprint returns the pin for a token: the
// lowercase hex SHA-256 of the placeholder public key's DER SubjectPublicKeyInfo.
// It is public and safe to store; the token it came from is not recoverable
// from it.
func BootstrapPlaceholderSPKIFingerprint(token, qubeName string) (string, error) {
	key, err := BootstrapPlaceholderPrivateKey(token, qubeName)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return "", fmt.Errorf("pki: marshal bootstrap placeholder public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

// NewBootstrapPlaceholderCertificate mints the self-signed certificate a
// pending agent serves until it holds a CA-issued identity. It carries the
// agent role and ServerAuth usage so it passes the same role and usage checks
// the console applies to every agent it dials; what it cannot pass without the
// token is the pin.
func NewBootstrapPlaceholderCertificate(token, qubeName string, now time.Time, lifetime time.Duration) (*tls.Certificate, error) {
	if lifetime <= 0 {
		return nil, errors.New("pki: bootstrap placeholder needs a positive lifetime")
	}
	key, err := BootstrapPlaceholderPrivateKey(token, qubeName)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: BootstrapPlaceholderCommonName(qubeName)},
		NotBefore:    now.Add(-bootstrapPlaceholderBackdate),
		NotAfter:     now.Add(lifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		URIs:         []*url.URL{roleURI(RoleAgent)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("pki: sign bootstrap placeholder: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// VerifyBootstrapPlaceholder is the console's whole trust decision about a
// pending agent it dialed. certs is the peer chain as presented, leaf first.
// The leaf's public key must hash to wantPin, and the leaf must be the
// placeholder for exactly qubeName — self-signed, inside its validity window,
// digitalSignature + ServerAuth only, carrying the agent role.
//
// The pin is checked first because it is the one claim that cannot be minted
// without the token, and because its failure is the diagnosis an operator
// needs: an agent package older than pinning, another qube's user-data and an
// impostor all fail here, rather than on whichever shape check their random
// certificate happens to miss.
func VerifyBootstrapPlaceholder(certs []*x509.Certificate, qubeName, wantPin string) error {
	if qubeName == "" {
		return errors.New("pki: no qube name to verify the bootstrap peer against")
	}
	want, err := decodeBootstrapPin(wantPin)
	if err != nil {
		return err
	}
	if len(certs) == 0 || certs[0] == nil {
		return errors.New("bootstrap peer did not present a certificate")
	}
	leaf := certs[0]
	got := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
		return errors.New("bootstrap peer public key does not match the pin issued with its token")
	}
	return checkBootstrapPlaceholder(leaf, qubeName, time.Now())
}

// CheckBootstrapPin reports whether pin is well formed: a lowercase or
// uppercase hex SHA-256 digest. It lets a caller tell a damaged stored pin
// from a peer that fails verification.
func CheckBootstrapPin(pin string) error {
	_, err := decodeBootstrapPin(pin)
	return err
}

// decodeBootstrapPin refuses anything that is not a SHA-256 digest in hex. An
// empty pin in particular must never mean "skip the check".
func decodeBootstrapPin(pin string) ([]byte, error) {
	if pin == "" {
		return nil, errors.New("bootstrap peer pin is missing")
	}
	want, err := hex.DecodeString(pin)
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("bootstrap peer pin is not a SHA-256 hex digest")
	}
	return want, nil
}

// checkBootstrapPlaceholder applies the certificate-shape checks. Each one is
// a separate refusal so an operator reading the log learns which part of the
// peer was wrong.
func checkBootstrapPlaceholder(leaf *x509.Certificate, qubeName string, now time.Time) error {
	if want := BootstrapPlaceholderCommonName(qubeName); leaf.Subject.CommonName != want {
		return fmt.Errorf("bootstrap peer names %q, but this address should be serving %q",
			leaf.Subject.CommonName, want)
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return fmt.Errorf("bootstrap peer certificate is outside its validity period (%s to %s); check the guest clock",
			leaf.NotBefore.UTC().Format(time.RFC3339), leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if leaf.KeyUsage != x509.KeyUsageDigitalSignature || len(leaf.ExtKeyUsage) != 1 ||
		leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || len(leaf.UnknownExtKeyUsage) != 0 {
		return errors.New("bootstrap peer certificate is not limited to server authentication")
	}
	// CheckSignature, not CheckSignatureFrom: the latter insists the parent be a
	// CA, which a leaf that signs only itself is not and must not claim to be.
	if !bytes.Equal(leaf.RawSubject, leaf.RawIssuer) ||
		leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) != nil {
		return errors.New("bootstrap peer certificate is not self-signed by its own key")
	}
	if role, err := RoleOf(leaf); err != nil || role != RoleAgent {
		return errors.New("bootstrap peer certificate does not carry the agent role")
	}
	return nil
}

// BootstrapDialTLSConfig returns the client configuration for dialing a pending
// agent: authenticate with client (the console-bootstrap identity) and accept
// the peer only if VerifyBootstrapPlaceholder passes for qubeName and pin.
//
// Invalid input is refused rather than defaulted, as AgentDialTLSConfig does:
// an empty or malformed pin would otherwise build a config whose verifier
// rejects every peer, which reads as "agent unreachable" instead of the
// console-side bug it is.
func BootstrapDialTLSConfig(client tls.Certificate, qubeName, pin string) (*tls.Config, error) {
	if len(client.Certificate) == 0 || client.PrivateKey == nil {
		return nil, errors.New("pki: no client certificate to authenticate to the bootstrap peer with")
	}
	if qubeName == "" {
		return nil, errors.New("pki: no qube name to pin the bootstrap peer to")
	}
	if _, err := decodeBootstrapPin(pin); err != nil {
		return nil, fmt.Errorf("pki: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{client},
		MinVersion:   tls.VersionTLS13,
		// The placeholder is self-signed, so the stack's chain and hostname
		// checks cannot pass for a genuine agent. VerifyConnection replaces them
		// with the pin check on every handshake, resumed or not, reading
		// PeerCertificates because InsecureSkipVerify leaves the chain unverified.
		InsecureSkipVerify: true, // #nosec G402 -- VerifyConnection below runs VerifyBootstrapPlaceholder on every handshake: token-derived SPKI pin, exact target CN, validity, ServerAuth-only usage, self-signature and RoleAgent //nolint:gosec // peer verified in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			return VerifyBootstrapPlaceholder(cs.PeerCertificates, qubeName, pin)
		},
	}, nil
}
