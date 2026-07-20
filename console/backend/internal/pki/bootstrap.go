package pki

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Bootstrap tokens: getting the agent's private key out of the delivery path.
//
// Today the console mints a keypair for each qube and ships all of it — the
// certificate, the CA, and the PRIVATE KEY — inside the cloud-init document.
// That key is a valid identity for the whole life of the certificate, and it
// exists in at least three places it does not need to: the rendered identity
// file on the console, the snippet uploaded to the hypervisor, and whatever
// backups either of those lands in.
//
// The renewal path already does it the right way: qubesair.BeginRenewal has the
// AGENT generate a keypair and return only a CSR, so the private key never
// leaves the machine. Bootstrap is the one place that still ships a key, and
// only because of a chicken-and-egg — renewal is authenticated by the mTLS
// certificate the agent does not yet have.
//
// A bootstrap token breaks that cycle. It authorizes issuing exactly one
// certificate, for exactly one qube name, once, before it expires. The agent
// generates its own key, sends a CSR plus the token, and the console signs.
//
// Why this is better than shipping a key, stated precisely rather than as a
// blanket claim:
//
//   - It is single-use. After redemption a stolen token yields nothing; a
//     stolen private key remains a working identity until the certificate
//     expires or is revoked.
//   - It is short-lived, so the window in which a leak matters is minutes
//     rather than the certificate's lifetime.
//   - It cannot be used to impersonate a qube that has already booted, which is
//     the failure a leaked key does allow.
//
// What it does NOT solve: an attacker positioned between the console and a
// not-yet-bootstrapped agent can still read the token in flight and redeem it
// first. That is the same position that today lets an attacker read the private
// key out of the same channel, so this is not a regression — but it is not a
// fix either, and on providers that offer signed instance attestation (a GCP
// instance identity token, for example) that attestation should replace the
// token rather than sit beside it.

// Errors a caller is expected to distinguish.
var (
	// ErrTokenExpired means the token was valid but is past its deadline.
	ErrTokenExpired = errors.New("bootstrap token expired")
	// ErrTokenRedeemed means the token was already used. Single-use is the
	// property that makes a leaked token worthless after the fact.
	ErrTokenRedeemed = errors.New("bootstrap token already redeemed")
	// ErrTokenMismatch covers both a wrong secret and a token presented for a
	// different qube. They are deliberately the same error: telling a caller
	// which half was wrong tells an attacker which half to keep guessing.
	ErrTokenMismatch = errors.New("bootstrap token does not match")
)

// bootstrapTokenBytes is the entropy in a token. 32 bytes is far past guessing
// range and keeps the encoded form short enough to sit in a cloud-init file
// without wrapping.
const bootstrapTokenBytes = 32

// BootstrapRecord is what the console keeps. It holds the token's HASH, never
// the token: a leaked database yields nothing usable, the same reason a
// password store keeps digests.
type BootstrapRecord struct {
	// QubeName is the only name this token can obtain a certificate for.
	QubeName string `json:"qube_name"`
	// SecretHash is the SHA-256 of the token, hex-encoded.
	SecretHash string `json:"secret_hash"`
	// NotAfter is when the token stops being accepted.
	NotAfter time.Time `json:"not_after"`
	// RedeemedAt is set the first time the token is successfully used.
	RedeemedAt *time.Time `json:"redeemed_at,omitempty"`
}

// NewBootstrapToken mints a token for one qube and returns the secret alongside
// the record to store.
//
// The secret is returned exactly once: it is not recoverable from the record,
// so a caller that loses it must issue a new token. That is the point.
func NewBootstrapToken(qubeName string, ttl time.Duration) (secret string, rec *BootstrapRecord, err error) {
	if qubeName == "" {
		return "", nil, fmt.Errorf("bootstrap token needs a qube name")
	}
	if ttl <= 0 {
		return "", nil, fmt.Errorf("bootstrap token needs a positive lifetime")
	}

	buf := make([]byte, bootstrapTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("generate bootstrap token: %w", err)
	}
	// RawURLEncoding: no padding and no characters that need quoting in a
	// cloud-init YAML scalar or a shell variable.
	secret = base64.RawURLEncoding.EncodeToString(buf)

	return secret, &BootstrapRecord{
		QubeName:   qubeName,
		SecretHash: hashBootstrapSecret(secret),
		NotAfter:   time.Now().UTC().Add(ttl),
	}, nil
}

// hashBootstrapSecret is the one place the digest is computed, so a change here
// cannot leave minting and verification disagreeing.
func hashBootstrapSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Verify reports whether this token may be redeemed for qubeName at now.
//
// It does NOT mark the token redeemed — that is Redeem's job, and it is split
// so the caller can verify inside a transaction and record redemption together
// with whatever it issued. A caller that verifies and forgets to redeem has
// re-created a reusable token, so Redeem returns an error rather than being
// silently optional.
func (r *BootstrapRecord) Verify(secret, qubeName string, now time.Time) error {
	if r == nil {
		return ErrTokenMismatch
	}
	if r.RedeemedAt != nil {
		return ErrTokenRedeemed
	}
	if now.After(r.NotAfter) {
		return ErrTokenExpired
	}

	// Constant time, and the name is compared the same way. Comparing the name
	// with == would leak, through timing, whether the secret matched a record
	// for a different qube.
	nameOK := subtle.ConstantTimeCompare([]byte(r.QubeName), []byte(qubeName)) == 1
	hashOK := subtle.ConstantTimeCompare(
		[]byte(r.SecretHash), []byte(hashBootstrapSecret(secret))) == 1

	// Both checks always run: returning early on the first failure would make
	// the two distinguishable by timing.
	if !nameOK || !hashOK {
		return ErrTokenMismatch
	}
	return nil
}

// Redeem marks the token used. It is idempotent only in the sense that a second
// call reports ErrTokenRedeemed rather than silently succeeding.
func (r *BootstrapRecord) Redeem(now time.Time) error {
	if r == nil {
		return ErrTokenMismatch
	}
	if r.RedeemedAt != nil {
		return ErrTokenRedeemed
	}
	t := now.UTC()
	r.RedeemedAt = &t
	return nil
}

// Redeemed reports whether this token has been used.
func (r *BootstrapRecord) Redeemed() bool {
	return r != nil && r.RedeemedAt != nil
}
