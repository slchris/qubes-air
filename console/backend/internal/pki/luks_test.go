package pki

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDeriveDataKeyIsDeterministic(t *testing.T) {
	master, err := NewDataMasterSecret()
	if err != nil {
		t.Fatalf("NewDataMasterSecret: %v", err)
	}
	// The same (master, qubeID) must always yield the same key — this is what
	// lets a resumed compute VM unlock the same container. A non-deterministic
	// derivation would lock the data away on the first resume.
	a, err := DeriveDataKey(master, "qube-abc")
	if err != nil {
		t.Fatalf("derive a: %v", err)
	}
	b, err := DeriveDataKey(master, "qube-abc")
	if err != nil {
		t.Fatalf("derive b: %v", err)
	}
	if a != b {
		t.Fatalf("derivation is not deterministic: %q != %q", a, b)
	}
	// A 32-byte key base64-encodes to a single non-empty line.
	if a == "" || strings.ContainsAny(a, "\n\r ") {
		t.Fatalf("derived key is not a clean single line: %q", a)
	}
	if raw, err := base64.RawStdEncoding.DecodeString(a); err != nil || len(raw) != 32 {
		t.Fatalf("derived key is not 32 raw bytes (len=%d err=%v)", len(raw), err)
	}
}

func TestDeriveDataKeyIsPerQube(t *testing.T) {
	master, err := NewDataMasterSecret()
	if err != nil {
		t.Fatalf("NewDataMasterSecret: %v", err)
	}
	a, _ := DeriveDataKey(master, "qube-abc")
	b, _ := DeriveDataKey(master, "qube-xyz")
	if a == b {
		t.Fatal("two different qubes derived the same key; one qube's disk key must not unlock another's")
	}
}

func TestDeriveDataKeyDiffersByMaster(t *testing.T) {
	m1, _ := NewDataMasterSecret()
	m2, _ := NewDataMasterSecret()
	if m1 == m2 {
		t.Fatal("two fresh masters collided; the CSPRNG is broken or fixed")
	}
	a, _ := DeriveDataKey(m1, "qube-abc")
	b, _ := DeriveDataKey(m2, "qube-abc")
	if a == b {
		t.Fatal("same qube under different masters derived the same key")
	}
}

func TestDeriveDataKeyRejectsBadInput(t *testing.T) {
	good, _ := NewDataMasterSecret()
	if _, err := DeriveDataKey(good, ""); err == nil {
		t.Fatal("empty qube id must be refused")
	}
	if _, err := DeriveDataKey("not!base64!", "q"); err == nil {
		t.Fatal("undecodable master must be refused")
	}
	// A short master must be refused rather than silently producing a weak key.
	short := base64.RawURLEncoding.EncodeToString([]byte("too-short"))
	if _, err := DeriveDataKey(short, "q"); err == nil {
		t.Fatal("short master must be refused")
	}
}
