package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
)

// The CSR relay-bootstrap generates must be signable by the console CA for the
// relay's own identity, and the returned key must match it — i.e. the two halves
// of provisioning fit together end to end (minus the qrexec hop).
func TestGenerateKeyAndCSR_SignsAndRoundTrips(t *testing.T) {
	cn := pki.RelayCommonName("sys-relay-pve")
	keyPEM, csrPEM, err := generateKeyAndCSR(cn)
	if err != nil {
		t.Fatalf("generateKeyAndCSR: %v", err)
	}
	if keyPEM == "" || csrPEM == "" {
		t.Fatal("empty key or CSR")
	}

	ca, err := pki.NewCA("test-ca", 72*time.Hour)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	signed, err := ca.SignAgentCSR(csrPEM, cn, time.Hour)
	if err != nil {
		t.Fatalf("the generated CSR was not signable: %v", err)
	}
	if err := checkIdentity(signed.CertPEM, cn); err != nil {
		t.Fatalf("checkIdentity rejected a valid cert: %v", err)
	}
	if err := checkIdentity(signed.CertPEM, pki.RelayCommonName("someone-else")); err == nil {
		t.Fatal("checkIdentity accepted a cert for the wrong CN")
	}
}

func TestStoreIdentity_KeyIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	dir := filepath.Join(t.TempDir(), "id")
	if err := storeIdentity(dir, "KEY", "CERT", "CA"); err != nil {
		t.Fatalf("storeIdentity: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "relay.key"))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("relay.key mode = %o, want 600", perm)
	}
	// Certificate and CA are public; only the key must be tight.
	for _, f := range []string{"relay.crt", "ca.crt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "relay.crt"))
	if err != nil || string(got) != "CERT" {
		t.Fatalf("relay.crt content = %q err=%v", got, err)
	}
}

func TestParseSigned_RejectsGarbageAndEmpty(t *testing.T) {
	if _, err := parseSigned([]byte("not json")); err == nil {
		t.Fatal("expected error on non-JSON")
	}
	if _, err := parseSigned([]byte(`{"cert_pem":"","ca_pem":""}`)); err == nil {
		t.Fatal("expected error on empty cert/CA")
	}
}
