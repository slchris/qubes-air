package pki

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// dataMasterLen is the size of the LEGACY data-encryption master secret, the
// one disks formatted before per-qube keys derived from. 256 bits: every qube's
// key derived from a master, so its compromise exposed every data disk at once.
// The console no longer creates masters; this only validates ones being read for
// migration.
const dataMasterLen = 32

// dataKeyInfoPrefix domain-separates the derivation. The trailing v1 leaves room
// to rotate the derivation scheme (a different prefix yields entirely different
// per-qube keys) without colliding with keys already protecting real data.
const dataKeyInfoPrefix = "qubes-air-luks-data-key:v1:"

// NewDataKey returns a fresh, random per-qube data key, base64 (raw-std) so it
// is a single shell-safe line like DeriveDataKey's output.
//
// Unlike DeriveDataKey this is NOT reproducible from any master: the stored copy
// is the only way to decrypt the disk, so deleting it is a real crypto-shred.
// That is what makes a purge irreversible even if a copy of the ciphertext
// survives (a backup, a snapshot, a cloned volume) — the key is gone.
func NewDataKey() (string, error) {
	buf := make([]byte, dataKeyLen)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generate data key: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(buf), nil
}

// dataKeyLen is the size of a per-qube data key. 256 bits, matching the master.
const dataKeyLen = 32

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
