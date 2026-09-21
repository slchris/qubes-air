package pki

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// RevocationLifetime bounds replay of a previously signed status document.
const RevocationLifetime = 5 * time.Minute

// RevocationState contains no keys, names, addresses or revocation reasons.
type RevocationState struct {
	Version   int       `json:"version"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   []string  `json:"revoked"`
}

type signedRevocations struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

// SignRevocations signs a short-lived denylist using the existing fleet CA.
func (ca *CA) SignRevocations(revoked []string, now time.Time) ([]byte, error) {
	state := RevocationState{Version: 1, IssuedAt: now.UTC(), ExpiresAt: now.Add(RevocationLifetime).UTC(), Revoked: revoked}
	if ca == nil || ca.Key == nil || ca.Cert == nil {
		return nil, ErrNoCA
	}
	if err := validateRevocations(state, now); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	signature, err := ecdsa.SignASN1(rand.Reader, ca.Key, digest[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(signedRevocations{Payload: payload, Signature: signature})
}

// VerifyRevocations verifies exact signed bytes against a currently trusted CA.
func VerifyRevocations(document []byte, roots []*x509.Certificate, now time.Time) (*RevocationState, error) {
	var signed signedRevocations
	if len(document) > 1024*1024 || json.Unmarshal(document, &signed) != nil {
		return nil, errors.New("invalid revocation document")
	}
	digest := sha256.Sum256(signed.Payload)
	trusted := false
	for _, ca := range roots {
		key, ok := ca.PublicKey.(*ecdsa.PublicKey)
		if ok && ca.IsCA && !now.Before(ca.NotBefore) && now.Before(ca.NotAfter) &&
			ecdsa.VerifyASN1(key, digest[:], signed.Signature) {
			trusted = true
			break
		}
	}
	if !trusted {
		return nil, errors.New("untrusted revocation signature")
	}
	var state RevocationState
	if err := json.Unmarshal(signed.Payload, &state); err != nil {
		return nil, err
	}
	if err := validateRevocations(state, now); err != nil {
		return nil, err
	}
	return &state, nil
}

func validateRevocations(state RevocationState, now time.Time) error {
	if state.Version != 1 || state.IssuedAt.After(now.Add(30*time.Second)) ||
		!state.ExpiresAt.After(now) || !state.ExpiresAt.After(state.IssuedAt) ||
		state.ExpiresAt.Sub(state.IssuedAt) > RevocationLifetime || len(state.Revoked) > 10000 {
		return errors.New("invalid or stale revocation state")
	}
	for _, fp := range state.Revoked {
		raw, err := hex.DecodeString(fp)
		if err != nil || len(raw) != sha256.Size {
			return errors.New("invalid revoked fingerprint")
		}
	}
	return nil
}

// ValidateRevocationURL narrows the public feed target without allowing
// credentials or control characters into cloud-init environment files.
func ValidateRevocationURL(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(endpoint) > 4096 ||
		strings.ContainsAny(endpoint, "\r\n\t \"'\\") {
		return errors.New("revocation URL must be an explicit HTTP(S) endpoint without credentials, query or fragment")
	}
	return nil
}
