// Command relay-bootstrap provisions a relay qube's client identity.
//
// It is the relay-side half of relay certificate provisioning for the
// separate-relay transport (docs/grpc-transport-design.md §0.5), and the mirror
// of what an agent does at first boot: generate a keypair, send only a CSR, and
// receive a signed certificate — the private key never leaves this qube.
//
// The relay sends its CSR to the console over the qubesair.IssueRelayCert qrexec
// service. dom0 tells the console who is calling (the relay's own qube name),
// the console pins the certificate's common name to it, and returns the signed
// certificate plus the CA. This command writes relay.key (0600), relay.crt and
// ca.crt into the identity directory for relay-call to load.
//
// It is idempotent and safe to run from a systemd timer: each run obtains a
// fresh short-lived certificate, which is how the relay renews.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
)

const issueService = "qubesair.IssueRelayCert"

func main() {
	log.SetFlags(0)
	log.SetPrefix("relay-bootstrap: ")

	console := flag.String("console", "qubesair-console", "console qube to request the certificate from")
	dir := flag.String("dir", "/rw/config/qubesair-relay", "identity directory to write into")
	name := flag.String("name", "", "this relay's qube name (default: QubesDB /name)")
	flag.Parse()

	relayName := *name
	if relayName == "" {
		relayName = qubesdbName()
	}
	if relayName == "" {
		log.Fatal("could not determine this relay's qube name; pass -name")
	}
	cn := pki.RelayCommonName(relayName)

	keyPEM, csrPEM, err := generateKeyAndCSR(cn)
	must(err)

	signed, err := requestCert(*console, csrPEM)
	must(err)

	// Belt and braces: the console pins the CN, but verify the returned
	// certificate is the identity we asked for before trusting it on disk.
	if err := checkIdentity(signed.CertPEM, cn); err != nil {
		log.Fatalf("console returned an unexpected certificate: %v", err)
	}

	must(storeIdentity(*dir, keyPEM, signed.CertPEM, signed.CAPEM))
	fmt.Printf("relay identity %s written to %s (expires %s)\n",
		cn, *dir, signed.NotAfter.Format(time.RFC3339))
}

// generateKeyAndCSR makes a fresh P-256 key and a CSR carrying only its public
// half, exactly as an agent does at bootstrap. The private key is returned in
// PEM so the caller can persist it; it is never sent anywhere.
func generateKeyAndCSR(cn string) (keyPEM, csrPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		return "", "", fmt.Errorf("create CSR: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("marshal key: %w", err)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	csrPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	return keyPEM, csrPEM, nil
}

// requestCert sends the CSR to the console over qrexec and parses the signed
// certificate it returns. qrexec-client-vm connects stdin/stdout to the console
// service; the CSR goes in, the JSON SignedCert comes back.
func requestCert(console, csrPEM string) (*pki.SignedCert, error) {
	cmd := exec.Command("qrexec-client-vm", console, issueService)
	cmd.Stdin = strings.NewReader(csrPEM)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s call failed: %w (%s)", issueService, err,
			strings.TrimSpace(errBuf.String()))
	}
	return parseSigned(out.Bytes())
}

func parseSigned(b []byte) (*pki.SignedCert, error) {
	var signed pki.SignedCert
	if err := json.Unmarshal(bytes.TrimSpace(b), &signed); err != nil {
		return nil, fmt.Errorf("unparseable response from console: %w", err)
	}
	if signed.CertPEM == "" || signed.CAPEM == "" {
		return nil, errors.New("console response missing cert or CA")
	}
	return &signed, nil
}

// checkIdentity confirms the returned certificate is for the common name we
// requested — the relay must never install an identity it did not ask for.
func checkIdentity(certPEM, wantCN string) error {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return errors.New("no certificate in response")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	if cert.Subject.CommonName != wantCN {
		return fmt.Errorf("got CN %q, wanted %q", cert.Subject.CommonName, wantCN)
	}
	return nil
}

// storeIdentity writes the key, certificate and CA into dir. The key is written
// 0600 and every file is written via a temp-file rename so a crashed run can
// never leave relay-call reading a half-written key against a new certificate.
func storeIdentity(dir, keyPEM, certPEM, caPEM string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	files := []struct {
		name string
		data string
		mode os.FileMode
	}{
		{"relay.key", keyPEM, 0o600},
		{"relay.crt", certPEM, 0o644},
		{"ca.crt", caPEM, 0o644},
	}
	for _, f := range files {
		if err := writeAtomic(filepath.Join(dir, f.name), f.data, f.mode); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}
	return nil
}

func writeAtomic(path, data string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.WriteString(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// qubesdbName reads this qube's own name from QubesDB, the name dom0 also uses
// as QREXEC_REMOTE_DOMAIN when this qube calls out — so the CN the relay signs
// into its CSR matches what the console pins it to.
func qubesdbName() string {
	out, err := exec.Command("qubesdb-read", "/name").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func must(err error) {
	if err != nil {
		log.Fatal(strings.TrimSpace(err.Error()))
	}
}
