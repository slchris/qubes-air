// Package keyring manages multiple versioned AES-256 encryption keys so that
// the credential encryption key can be rotated without invalidating existing
// ciphertext.
//
// Rotation model
//
//	Every credential row stores the key_version that encrypted it. A Keyring
//	holds one or more versioned keys and a designated "primary" version used
//	for all NEW encryption. Decryption picks the key matching the row's
//	version. To rotate: add a new higher version key, make it primary, then
//	re-encrypt existing rows from their old version to the new primary (see
//	cmd/rotate-key). Old versions must remain present in the keyring until no
//	row references them.
package keyring

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// KeySize is the required AES-256 key length in bytes.
const KeySize = 32

// Keyring holds versioned encryption keys and tracks which version is primary
// (used for new encryption). It is immutable after construction.
type Keyring struct {
	keys    map[int][]byte
	primary int
}

// New builds a Keyring from a map of version->key. The primary version is used
// for new encryption and must exist in keys. Every key must be exactly KeySize
// bytes. New copies key material so callers may reuse their buffers.
func New(keys map[int][]byte, primary int) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("keyring: no keys provided")
	}
	kr := &Keyring{keys: make(map[int][]byte, len(keys))}
	for v, k := range keys {
		if v < 1 {
			return nil, fmt.Errorf("keyring: key version must be >= 1, got %d", v)
		}
		if len(k) != KeySize {
			return nil, fmt.Errorf("keyring: key for version %d must be exactly %d bytes, got %d", v, KeySize, len(k))
		}
		cp := make([]byte, KeySize)
		copy(cp, k)
		kr.keys[v] = cp
	}
	if _, ok := kr.keys[primary]; !ok {
		return nil, fmt.Errorf("keyring: primary version %d has no key", primary)
	}
	kr.primary = primary
	return kr, nil
}

// NewSingle builds a Keyring with a single key at version 1 (the default
// version for legacy rows). This preserves the pre-rotation single-key
// behaviour.
func NewSingle(key []byte) (*Keyring, error) {
	return New(map[int][]byte{1: key}, 1)
}

// PrimaryVersion returns the version used for new encryption.
func (kr *Keyring) PrimaryVersion() int {
	return kr.primary
}

// Key returns the key for a specific version, or an error if that version is
// not present in the keyring.
func (kr *Keyring) Key(version int) ([]byte, error) {
	k, ok := kr.keys[version]
	if !ok {
		return nil, fmt.Errorf("keyring: no key for version %d (rotate/decrypt would fail; ensure all in-use key versions are configured)", version)
	}
	return k, nil
}

// PrimaryKey returns the key for the primary version.
func (kr *Keyring) PrimaryKey() ([]byte, error) {
	return kr.Key(kr.primary)
}

// Versions returns all configured versions in ascending order.
func (kr *Keyring) Versions() []int {
	vs := make([]int, 0, len(kr.keys))
	for v := range kr.keys {
		vs = append(vs, v)
	}
	sort.Ints(vs)
	return vs
}

// ParseSpec parses a multi-version key spec of the form
//
//	"v1:<32-byte-key>,v2:<32-byte-key>"
//
// (the leading "v" on each version is optional, so "1:key,2:key" also works).
// The highest version present becomes the primary. Whitespace around entries is
// trimmed. Every key must be exactly KeySize bytes.
//
// This is the format consumed from QUBES_AIR_ENCRYPTION_KEYS. It exists so an
// operator can stage a new key alongside the old one for a rotation window
// without a redeploy between "add new key" and "re-encrypt".
func ParseSpec(spec string) (*Keyring, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("keyring: empty key spec")
	}
	keys := make(map[int][]byte)
	primary := 0
	for entry := range strings.SplitSeq(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		verStr, key, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, fmt.Errorf("keyring: malformed entry %q, want VERSION:KEY", entry)
		}
		verStr = strings.TrimSpace(verStr)
		verStr = strings.TrimPrefix(verStr, "v")
		verStr = strings.TrimPrefix(verStr, "V")
		version, err := strconv.Atoi(verStr)
		if err != nil {
			return nil, fmt.Errorf("keyring: invalid version %q: %w", verStr, err)
		}
		if version < 1 {
			return nil, fmt.Errorf("keyring: version must be >= 1, got %d", version)
		}
		if _, dup := keys[version]; dup {
			return nil, fmt.Errorf("keyring: duplicate version %d", version)
		}
		if len(key) != KeySize {
			return nil, fmt.Errorf("keyring: key for version %d must be exactly %d bytes, got %d", version, KeySize, len(key))
		}
		keys[version] = []byte(key)
		if version > primary {
			primary = version
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("keyring: no valid entries in spec")
	}
	return New(keys, primary)
}
