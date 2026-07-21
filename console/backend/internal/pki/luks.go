package pki

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// dataMasterLen is the size of the console's data-encryption master secret.
// 256 bits: every qube's disk key is HKDF-derived from this one secret, so its
// compromise exposes every data disk at once — it gets the strongest practical
// size, and unlike a password it is CSPRNG output, not something memorable.
const dataMasterLen = 32

// dataKeyInfoPrefix domain-separates the derivation. The trailing v1 leaves room
// to rotate the derivation scheme (a different prefix yields entirely different
// per-qube keys) without colliding with keys already protecting real data.
const dataKeyInfoPrefix = "qubes-air-luks-data-key:v1:"

// NewDataMasterSecret returns a fresh random master secret, base64 (raw-url)
// encoded so it stores as one clean line in the credential store. The console
// keeps exactly one of these; every qube's disk key is derived from it, and it
// never leaves the console — that is the whole point of encrypting the disk on
// an untrusted remote.
func NewDataMasterSecret() (string, error) {
	buf := make([]byte, dataMasterLen)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generate data master secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// DeriveDataKey derives a qube's LUKS passphrase from the console master secret
// and the qube's stable id.
//
// Two properties matter and both come from HKDF over (master, qubeID):
//   - Deterministic: the same qube id always yields the same passphrase, so a
//     resumed compute VM — a brand new VM with a new MAC and a fresh DHCP lease —
//     still unlocks the SAME container. Nothing about the key is bound to the
//     instance, only to the qube's identity.
//   - Isolated: a different qube id yields an unrelated key, so learning one
//     qube's derived key (or brute-forcing its ciphertext) reveals nothing about
//     any other qube's data.
//
// The result is base64 so it is a single line with no shell-hostile bytes,
// suitable to hand to `cryptsetup --key-file`.
func DeriveDataKey(masterB64, qubeID string) (string, error) {
	if qubeID == "" {
		return "", fmt.Errorf("refusing to derive a data key for an empty qube id")
	}
	master, err := base64.RawURLEncoding.DecodeString(masterB64)
	if err != nil {
		return "", fmt.Errorf("decode data master secret: %w", err)
	}
	if len(master) < dataMasterLen {
		return "", fmt.Errorf("data master secret is %d bytes; refusing to derive from fewer than %d",
			len(master), dataMasterLen)
	}
	// salt is nil on purpose: HKDF-Extract's salt is what rescues a low-entropy
	// input, and this master is already 256 bits of CSPRNG output. info binds the
	// output to THIS qube so two qubes never share a key.
	key, err := hkdf.Key(sha256.New, master, nil, dataKeyInfoPrefix+qubeID, 32)
	if err != nil {
		return "", fmt.Errorf("derive data key: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(key), nil
}
