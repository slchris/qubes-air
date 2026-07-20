package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
)

// testAgentCN is the common name the console mints for a qube's agent
// (service.AgentCommonName). Renewal must preserve it exactly.
const testAgentCN = "agent-qube-1"

// installedIdentity lays out an agent's identity the way cloud-init does and
// loads it.
func installedIdentity(t *testing.T, cn string) (*Identity, *pki.CA, string) {
	t.Helper()
	ca, err := pki.NewCA("test-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	dir := writeBundle(t, ca, cn)
	id, err := LoadIdentity(
		filepath.Join(dir, "agent.pem"),
		filepath.Join(dir, "agent-key.pem"),
		filepath.Join(dir, "ca.pem"),
	)
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	return id, ca, dir
}

// writeBundle writes ca.pem / agent.pem / agent-key.pem with the modes
// service.RenderAgentUserData uses.
func writeBundle(t *testing.T, ca *pki.CA, cn string) string {
	t.Helper()
	bundle, err := ca.IssueAgentCert(cn, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}
	dir := t.TempDir()
	writeAt(t, filepath.Join(dir, "ca.pem"), bundle.CAPEM, 0o644)
	writeAt(t, filepath.Join(dir, "agent.pem"), bundle.CertPEM, 0o644)
	writeAt(t, filepath.Join(dir, "agent-key.pem"), bundle.KeyPEM, 0o600)
	return dir
}

func writeAt(t *testing.T, path, content string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readAt(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// dirEntries lists a directory, including the dot-files a temp-and-rename would
// leave behind on a partial write.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// snapshot captures every file in dir so a failed operation can be shown to
// have changed nothing at all.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range dirEntries(t, dir) {
		out[name] = readAt(t, filepath.Join(dir, name))
	}
	return out
}

func assertUnchanged(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	after := snapshot(t, dir)
	if len(after) != len(before) {
		t.Fatalf("directory changed: before %v, after %v", keysOf(before), keysOf(after))
	}
	for name, content := range before {
		if after[name] != content {
			t.Errorf("%s was modified by an operation that failed", name)
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// certFor mints a certificate for pub, signed by ca, with the given common
// name. It is how the console's signing side is stood in for.
func certFor(t *testing.T, ca *pki.CA, pub *ecdsa.PublicKey, cn string, lifetime time.Duration) string {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"Qubes Air Agent"}},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(lifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.Key)
	if err != nil {
		t.Fatalf("sign certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func keyPEMOf(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: blockECKey, Bytes: der}))
}

// TestInstallRejectsMismatchedPair is the failure that would otherwise surface
// hours later, at the next restart, as an agent that cannot load its own
// identity — with nothing linking it back to the renewal that caused it.
func TestInstallRejectsMismatchedPair(t *testing.T) {
	id, ca, dir := installedIdentity(t, testAgentCN)
	before := snapshot(t, dir)
	served, _ := id.ServerCertificate()

	// A certificate for one key, offered alongside another.
	certKey := newKey(t)
	otherKey := newKey(t)
	certPEM := certFor(t, ca, &certKey.PublicKey, testAgentCN, time.Hour)

	if _, err := id.Install(certPEM, keyPEMOf(t, otherKey)); err == nil {
		t.Fatal("Install accepted a certificate that does not match the key")
	}

	assertUnchanged(t, dir, before)
	if now, _ := id.ServerCertificate(); now != served {
		t.Error("a failed install replaced the certificate being served")
	}
}

// TestInstallRejectsForeignCA: a certificate that verifies perfectly against
// some other CA is not an identity this agent may adopt. Accepting one would
// let anyone who can complete a handshake replace the agent's identity with one
// the console will never recognize.
func TestInstallRejectsForeignCA(t *testing.T) {
	id, _, dir := installedIdentity(t, testAgentCN)
	before := snapshot(t, dir)

	foreign, err := pki.NewCA("attacker-ca", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	key := newKey(t)
	certPEM := certFor(t, foreign, &key.PublicKey, testAgentCN, time.Hour)

	_, err = id.Install(certPEM, keyPEMOf(t, key))
	if !errors.Is(err, ErrUntrustedChain) {
		t.Fatalf("want ErrUntrustedChain, got %v", err)
	}
	assertUnchanged(t, dir, before)
}

// TestInstallRejectsExpiredCertificate — a certificate that is already out of
// date is a downgrade, not a renewal.
func TestInstallRejectsExpiredCertificate(t *testing.T) {
	id, ca, dir := installedIdentity(t, testAgentCN)
	before := snapshot(t, dir)

	key := newKey(t)
	certPEM := certFor(t, ca, &key.PublicKey, testAgentCN, -time.Hour)

	if _, err := id.Install(certPEM, keyPEMOf(t, key)); err == nil {
		t.Fatal("Install accepted an expired certificate")
	}
	assertUnchanged(t, dir, before)
}

// TestFailedInstallLeavesNoPartialState covers the temp files as well as the
// real ones: a rejected install must not leave half a private key sitting next
// to the live one, where the next thing to read the directory finds two.
func TestFailedInstallLeavesNoPartialState(t *testing.T) {
	id, _, dir := installedIdentity(t, testAgentCN)

	for i := 0; i < 3; i++ {
		key := newKey(t)
		if _, err := id.Install("not a certificate", keyPEMOf(t, key)); err == nil {
			t.Fatal("Install accepted garbage")
		}
	}

	want := []string{"agent-key.pem", "agent.pem", "ca.pem"}
	got := dirEntries(t, dir)
	if len(got) != len(want) {
		t.Fatalf("failed installs left files behind: want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

// TestInstallReplacesPairAtomically checks the successful path end to end:
// both files updated, correct modes, nothing staged left behind, and the new
// certificate being served in memory.
func TestInstallReplacesPairAtomically(t *testing.T) {
	id, ca, dir := installedIdentity(t, testAgentCN)
	oldLeaf, err := id.Leaf()
	if err != nil {
		t.Fatal(err)
	}

	key := newKey(t)
	certPEM := certFor(t, ca, &key.PublicKey, testAgentCN, 48*time.Hour)
	keyPEM := keyPEMOf(t, key)

	pair, err := id.Install(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if got := readAt(t, filepath.Join(dir, "agent.pem")); got != certPEM {
		t.Error("certificate file does not hold the installed certificate")
	}
	if got := readAt(t, filepath.Join(dir, "agent-key.pem")); got != keyPEM {
		t.Error("key file does not hold the installed key")
	}
	assertMode(t, filepath.Join(dir, "agent.pem"), 0o644)
	assertMode(t, filepath.Join(dir, "agent-key.pem"), 0o600)

	// The commit file is an implementation detail of atomicity, not something
	// that should outlive a successful install.
	if _, err := os.Stat(filepath.Join(dir, "agent-key.pem.commit")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("commit file survived a successful install")
	}
	if got := len(dirEntries(t, dir)); got != 3 {
		t.Errorf("want 3 files after install, got %v", dirEntries(t, dir))
	}

	served, err := id.ServerCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if served != pair {
		t.Error("the installed certificate is not the one being served")
	}
	if served.Leaf.SerialNumber.Cmp(oldLeaf.SerialNumber) == 0 {
		t.Error("the certificate being served did not change")
	}

	// And it must still be loadable from disk, which is the state a restart
	// would find.
	if _, err := loadPair(filepath.Join(dir, "agent.pem"), filepath.Join(dir, "agent-key.pem")); err != nil {
		t.Fatalf("installed pair does not load from disk: %v", err)
	}
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %v, want %v", filepath.Base(path), got, want)
	}
}

// TestRecoverCompletesInterruptedInstall simulates the power cut: the commit
// landed, the two files did not. Startup must finish the job rather than come
// up on the superseded certificate — or, worse, on a mismatched pair.
func TestRecoverCompletesInterruptedInstall(t *testing.T) {
	id, ca, dir := installedIdentity(t, testAgentCN)
	oldLeaf, _ := id.Leaf()

	renewed, err := ca.IssueAgentCert(testAgentCN, 48*time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}
	// Key first, matching what Install writes.
	writeAt(t, filepath.Join(dir, "agent-key.pem.commit"), renewed.KeyPEM+renewed.CertPEM, 0o600)

	reloaded, err := LoadIdentity(
		filepath.Join(dir, "agent.pem"),
		filepath.Join(dir, "agent-key.pem"),
		filepath.Join(dir, "ca.pem"),
	)
	if err != nil {
		t.Fatalf("LoadIdentity after an interrupted install: %v", err)
	}

	leaf, err := reloaded.Leaf()
	if err != nil {
		t.Fatal(err)
	}
	if leaf.SerialNumber.Cmp(oldLeaf.SerialNumber) == 0 {
		t.Fatal("startup came up on the superseded certificate, losing a committed renewal")
	}
	if got := readAt(t, filepath.Join(dir, "agent.pem")); got != renewed.CertPEM {
		t.Error("certificate file was not completed from the staged renewal")
	}
	if got := readAt(t, filepath.Join(dir, "agent-key.pem")); got != renewed.KeyPEM {
		t.Error("key file was not completed from the staged renewal")
	}
	if _, err := os.Stat(filepath.Join(dir, "agent-key.pem.commit")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("commit file survived recovery, so it would be replayed forever")
	}
	assertMode(t, filepath.Join(dir, "agent-key.pem"), 0o600)
	assertMode(t, filepath.Join(dir, "agent.pem"), 0o644)
}

// TestRecoverDiscardsUnusableCommit — a staged renewal that cannot be verified
// must not stop the agent starting. The installed pair is still good, and
// refusing to come up would turn an incomplete renewal into an outage.
func TestRecoverDiscardsUnusableCommit(t *testing.T) {
	_, _, dir := installedIdentity(t, testAgentCN)
	certBefore := readAt(t, filepath.Join(dir, "agent.pem"))

	for _, junk := range []string{
		"garbage",
		"", // truncated to nothing by a crash mid-write
	} {
		writeAt(t, filepath.Join(dir, "agent-key.pem.commit"), junk, 0o600)

		id, err := LoadIdentity(
			filepath.Join(dir, "agent.pem"),
			filepath.Join(dir, "agent-key.pem"),
			filepath.Join(dir, "ca.pem"),
		)
		if err != nil {
			t.Fatalf("an unusable staged renewal must not stop startup: %v", err)
		}
		if _, err := id.Leaf(); err != nil {
			t.Fatalf("Leaf: %v", err)
		}
		if got := readAt(t, filepath.Join(dir, "agent.pem")); got != certBefore {
			t.Error("an unusable staged renewal overwrote the working certificate")
		}
		if _, err := os.Stat(filepath.Join(dir, "agent-key.pem.commit")); !errors.Is(err, fs.ErrNotExist) {
			t.Error("unusable commit file was not discarded, so it is retried on every start")
		}
	}
}

// TestTrustsCA is the check that keeps a renewal from installing a new trust
// root: only the CA already on disk counts as trusted.
func TestTrustsCA(t *testing.T) {
	id, ca, _ := installedIdentity(t, testAgentCN)
	if !id.TrustsCA(ca.Cert.Raw) {
		t.Error("the agent does not recognize its own CA")
	}
	other, err := pki.NewCA("other-ca", 0)
	if err != nil {
		t.Fatal(err)
	}
	if id.TrustsCA(other.Cert.Raw) {
		t.Error("an unrelated CA was reported as trusted")
	}
}
