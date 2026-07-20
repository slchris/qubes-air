// identity.go — the agent's mTLS identity, on disk and in memory.
//
// Agent certificates are issued for pki.DefaultAgentCertLifetime (90 days), and
// until renewal existed the only delivery channel was cloud-init, which the
// guest reads once at first boot. Replacing a certificate therefore meant
// REBUILDING the VM (verified on hardware; see docs/bootstrap-design.md §7.1),
// which made the certificate lifetime a fleet rebuild period rather than a
// security parameter — and if nobody noticed, every qube went dark on the same
// day. This file is the on-host half of the fix: it replaces the pair in place,
// atomically, and hands the running listener the new certificate without a
// restart.

package agent

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/slchris/qubes-air/console/internal/pki"
)

// File modes for the installed identity.
//
// These match what service.RenderAgentUserData writes through cloud-init at
// first boot. Renewal replaces those exact files, so a drift here would mean a
// certificate that is world-readable (harmless) or a private key that is
// (not) — the difference between an identity that can be revoked and one that
// every local account on the host already holds.
const (
	certFileMode fs.FileMode = 0o644
	keyFileMode  fs.FileMode = 0o600
)

// PEM block types this file reads and writes.
const (
	blockCertificate = "CERTIFICATE"
	blockECKey       = "EC PRIVATE KEY"
)

// Identity errors.
var (
	// ErrNoIdentity means nothing has been loaded, so there is no certificate
	// to serve.
	ErrNoIdentity = errors.New("agent has no certificate loaded")
	// ErrUntrustedChain means a certificate does not chain to the CA this agent
	// already trusts.
	ErrUntrustedChain = errors.New("certificate does not chain to this agent's CA")
)

// Identity is the agent's certificate, private key and trusted CA.
//
// The certificate is held behind an atomic pointer because it is read on every
// TLS handshake, from the accept loop, while a renewal may be replacing it. A
// swap is a single pointer store: a handshake in progress finishes with the
// certificate it already read, and the next one picks up the new one. Nothing
// is torn down, so live connections are not dropped by a renewal.
type Identity struct {
	certPath string
	keyPath  string
	caPath   string
	// commitPath is where a renewal lands atomically before it is split into
	// the two conventional files. See Install for why it has to exist.
	commitPath string

	// roots verifies both the peer's client certificate and any certificate
	// offered to us for installation.
	roots *x509.CertPool
	// rootCerts is the same material in comparable form, so a renewal offering
	// a ca_pem can be checked against what we ALREADY trust rather than
	// silently establishing a new trust root.
	rootCerts []*x509.Certificate

	current atomic.Pointer[tls.Certificate]

	// installMu serializes renewals. Two concurrent installs would interleave
	// their temp files and commit in an unpredictable order, and the loser
	// would leave a commit file describing a pair that is not the one on disk.
	installMu sync.Mutex
}

// LoadIdentity reads the agent's identity from disk.
//
// It also finishes any renewal that was interrupted after its commit — see
// Install. That recovery runs before the pair is loaded, so the certificate
// this process goes on to serve is the one the renewal decided on, not the
// superseded one that happened to still be in the file.
func LoadIdentity(certPath, keyPath, caPath string) (*Identity, error) {
	id, err := newIdentityWithCA(certPath, keyPath, caPath)
	if err != nil {
		return nil, err
	}

	pair, err := loadPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	id.current.Store(pair)
	return id, nil
}

// NewPendingIdentity builds an identity that knows its paths and trusts its CA
// but may not hold a certificate yet — the state a first boot is in before
// bootstrap has run (docs/bootstrap-design.md §9). Install is what turns it
// into a working identity, exactly as it does for a renewal.
//
// Only a pair that is entirely ABSENT yields a pending identity. A pair that
// exists but cannot be loaded is an error, same as LoadIdentity: it describes
// a host that HAD an identity and lost the ability to use it, and pretending
// that is a fresh boot would spend a bootstrap token (if one is even still
// valid) to paper over corruption an operator needs to hear about.
//
// Interrupted-install recovery runs before the pair is probed, so a bootstrap
// or renewal that crashed between commit and materialize comes back as an
// identity that is already installed, not as a second bootstrap attempt whose
// token was spent by the first.
func NewPendingIdentity(certPath, keyPath, caPath string) (*Identity, error) {
	id, err := newIdentityWithCA(certPath, keyPath, caPath)
	if err != nil {
		return nil, err
	}

	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if errors.Is(certErr, fs.ErrNotExist) && errors.Is(keyErr, fs.ErrNotExist) {
		return id, nil
	}

	pair, err := loadPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	id.current.Store(pair)
	return id, nil
}

// HasCertificate reports whether an identity is installed and serving.
func (id *Identity) HasCertificate() bool { return id.current.Load() != nil }

// newIdentityWithCA loads the trust root and finishes any interrupted install,
// the half of construction LoadIdentity and NewPendingIdentity share.
func newIdentityWithCA(certPath, keyPath, caPath string) (*Identity, error) {
	if certPath == "" || keyPath == "" || caPath == "" {
		return nil, errors.New("agent identity needs a certificate, a key and a CA")
	}
	id := &Identity{
		certPath:   certPath,
		keyPath:    keyPath,
		caPath:     caPath,
		commitPath: keyPath + ".commit",
	}

	caPEM, err := os.ReadFile(caPath) // #nosec G304 -- operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("read CA %q: %w", caPath, err)
	}
	id.rootCerts, err = parseCertificates(caPEM)
	if err != nil || len(id.rootCerts) == 0 {
		return nil, fmt.Errorf("CA file %q contains no usable certificate: %w", caPath, err)
	}
	id.roots = x509.NewCertPool()
	for _, c := range id.rootCerts {
		id.roots.AddCert(c)
	}

	id.recoverCommitted()
	return id, nil
}

// ServerCertificate returns the certificate the listener should present.
//
// Called on every handshake by transport/grpc's GetCertificate hook, which is
// what makes a renewal take effect without a restart.
func (id *Identity) ServerCertificate() (*tls.Certificate, error) {
	cert := id.current.Load()
	if cert == nil {
		return nil, ErrNoIdentity
	}
	return cert, nil
}

// Leaf returns the certificate currently being served.
func (id *Identity) Leaf() (*x509.Certificate, error) {
	cert := id.current.Load()
	if cert == nil || cert.Leaf == nil {
		return nil, ErrNoIdentity
	}
	return cert.Leaf, nil
}

// ServerTLSConfig builds the listener's mTLS config.
//
// Certificates is populated with the current pair so that a server which does
// not consult a certificate source still comes up with a working identity.
// A server that does consult one clears this field — see
// transport/grpc.ServerConfig.CertSource for why it must.
func (id *Identity) ServerTLSConfig() *tls.Config {
	cfg := &tls.Config{
		ClientCAs:  id.roots,
		ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS13,
	}
	if cert := id.current.Load(); cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	return cfg
}

// TrustsCA reports whether der is a CA certificate this agent already trusts.
func (id *Identity) TrustsCA(der []byte) bool {
	for _, c := range id.rootCerts {
		if bytes.Equal(c.Raw, der) {
			return true
		}
	}
	return false
}

// VerifyChain checks that leaf chains to this agent's CA.
//
// ExtKeyUsageAny is deliberate. Agent certificates are issued with
// ExtKeyUsageClientAuth only (pki.IssueAgentCert) yet are presented here as
// SERVER certificates, so a ServerAuth check would reject the agent's own
// perfectly valid identity. The console's prober makes the same allowance for
// the same reason — see service.verifyAgentChain.
func (id *Identity) VerifyChain(leaf *x509.Certificate, intermediates []*x509.Certificate) error {
	inters := x509.NewCertPool()
	for _, c := range intermediates {
		inters.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         id.roots,
		Intermediates: inters,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrUntrustedChain, err)
	}
	return nil
}

// Install atomically replaces the agent's certificate and private key.
//
// The pair is the problem. Two files cannot be renamed in one operation, so the
// obvious "rename the key, then rename the certificate" leaves a window in
// which a power cut strands the host with a key that does not match its
// certificate — and that host comes back up unable to present any identity at
// all, hours later, with nothing linking it to this moment. Worse, it is
// unrecoverable from the host's side: the console cannot dial an agent that
// cannot complete a handshake, which is precisely the channel renewal runs on.
//
// So the pair is committed as ONE file containing both, renamed into place
// atomically. That rename is the point of no return: before it, nothing has
// changed and the previous certificate is untouched; after it, the new pair is
// fully described by a single file that survives a crash. The two conventional
// files are then materialized from it, and a restart that finds a commit file
// left behind finishes the job (see recoverCommitted).
//
// Verification happens BEFORE anything is written. A certificate that does not
// match the key, or does not chain to our CA, is refused while the previous
// certificate is still installed and working.
func (id *Identity) Install(certPEM, keyPEM string) (*tls.Certificate, error) {
	pair, err := id.verifyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	id.installMu.Lock()
	defer id.installMu.Unlock()

	// Key first in the commit file so a truncated write — which cannot survive
	// the rename anyway — is never mistaken for a certificate-only file.
	if err := writeFileAtomic(id.commitPath, []byte(keyPEM+certPEM), keyFileMode, true); err != nil {
		// Nothing has been replaced yet, so failing here is entirely safe: the
		// agent keeps the certificate it already had.
		return nil, fmt.Errorf("stage renewed identity: %w", err)
	}

	if err := id.materialize(certPEM, keyPEM); err != nil {
		// The commit file survives, so the next start completes the install.
		// The error is still returned: the console must record this renewal as
		// failed rather than assume a certificate it never saw take effect.
		return nil, fmt.Errorf("install renewed identity (staged at %s, will be completed on restart): %w",
			id.commitPath, err)
	}

	id.current.Store(pair)

	// Best effort. A commit file that outlives its install is replayed on the
	// next start and rewrites the same bytes, which is a no-op.
	if err := os.Remove(id.commitPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("agent identity: could not remove %s after installing: %v", id.commitPath, err)
	}
	return pair, nil
}

// materialize writes the pair to the two conventional paths.
//
// Key before certificate, matching the commit file's order, so that the window
// a crash can land in is the same one recoverCommitted is written to repair.
func (id *Identity) materialize(certPEM, keyPEM string) error {
	if err := writeFileAtomic(id.keyPath, []byte(keyPEM), keyFileMode, false); err != nil {
		return fmt.Errorf("write key %q: %w", id.keyPath, err)
	}
	if err := writeFileAtomic(id.certPath, []byte(certPEM), certFileMode, false); err != nil {
		return fmt.Errorf("write certificate %q: %w", id.certPath, err)
	}
	return nil
}

// recoverCommitted finishes a renewal that was interrupted after its commit.
//
// A commit file that is present at startup means the pair in it was verified
// and committed, but one or both of the conventional files may still hold the
// previous material. Rewriting both from the commit file is idempotent, so
// running it on every start costs nothing and closes the crash window.
//
// A commit file that is unusable is removed rather than fatal. It describes an
// install that never completed; the two files on disk still hold the previous,
// working pair, and refusing to start would turn a partial renewal into an
// outage — the opposite of what renewal is for.
func (id *Identity) recoverCommitted() {
	data, err := os.ReadFile(id.commitPath) // #nosec G304 -- path derived from the operator-supplied key path
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("agent identity: cannot read staged renewal %s, keeping the installed certificate: %v",
				id.commitPath, err)
		}
		return
	}

	certPEM, keyPEM, err := splitCommitted(data)
	if err == nil {
		_, err = id.verifyPair(certPEM, keyPEM)
	}
	if err != nil {
		log.Printf("agent identity: discarding unusable staged renewal %s: %v", id.commitPath, err)
		_ = os.Remove(id.commitPath)
		return
	}

	if err := id.materialize(certPEM, keyPEM); err != nil {
		// Leave the commit file in place: the next start tries again, and the
		// installed pair is still the previous working one.
		log.Printf("agent identity: could not complete the staged renewal in %s: %v", id.commitPath, err)
		return
	}
	log.Printf("agent identity: completed a renewal that was interrupted before it finished installing")
	_ = os.Remove(id.commitPath)
}

// verifyPair checks that a certificate and key belong together and that the
// certificate is one this agent may serve.
//
// Both halves matter. tls.X509KeyPair compares the certificate's public key
// against the private key, which is what stops the agent installing a pair it
// could never present; the chain check is what stops it installing an identity
// signed by anyone other than the console's CA.
func (id *Identity) verifyPair(certPEM, keyPEM string) (*tls.Certificate, error) {
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("certificate and private key do not form a usable pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return nil, errors.New("no certificate in PEM material")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	intermediates := make([]*x509.Certificate, 0, len(pair.Certificate)-1)
	for _, der := range pair.Certificate[1:] {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("parse chain certificate: %w", err)
		}
		intermediates = append(intermediates, c)
	}
	if err := id.VerifyChain(leaf, intermediates); err != nil {
		return nil, err
	}
	pair.Leaf = leaf
	return &pair, nil
}

// Fingerprint returns the registry key for a certificate, in the encoding the
// console's agent_certs table uses.
func Fingerprint(cert *x509.Certificate) string { return pki.FingerprintOf(cert) }

// loadPair reads an installed certificate and key.
func loadPair(certPath, keyPath string) (*tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load key pair (%q, %q): %w", certPath, keyPath, err)
	}
	if pair.Leaf == nil {
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("parse certificate %q: %w", certPath, err)
		}
		pair.Leaf = leaf
	}
	return &pair, nil
}

// writeFileAtomic writes data to path via a temp file and a rename.
//
// durable additionally fsyncs the containing directory, which is what makes the
// rename itself survive a power cut — without it the file's contents can be on
// disk while the directory entry pointing at them is not. It is demanded only
// for the commit file, where that guarantee is the whole point; for the two
// materialized files a lost rename is repaired from the commit file on the next
// start, so paying for it twice more buys nothing.
func writeFileAtomic(path string, data []byte, mode fs.FileMode, durable bool) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	// No-op once the rename succeeds; on every failure path below it is what
	// stops a half-written key being left behind next to the real one.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %q: %w", tmpName, err)
	}
	// Flush before the rename. A rename is atomic with respect to readers, but
	// it says nothing about whether the bytes reached the platter: without this
	// a crash can leave the new name pointing at a zero-length file, which for a
	// private key is indistinguishable from having no identity at all.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %q: %w", tmpName, err)
	}
	// CreateTemp makes the file 0600, so the key is never briefly world
	// readable; widening to 0644 for a certificate happens before it is visible
	// under its real name.
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("chmod %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into %q: %w", path, err)
	}
	if durable {
		if err := fsyncDir(dir); err != nil {
			return fmt.Errorf("sync directory %q: %w", dir, err)
		}
	}
	return nil
}

// fsyncDir flushes a directory entry so a rename survives a power cut.
func fsyncDir(dir string) error {
	d, err := os.Open(dir) // #nosec G304 -- the directory the caller is writing into
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// splitCommitted separates a commit file into its certificate and key halves.
func splitCommitted(data []byte) (certPEM, keyPEM string, err error) {
	var certs, keys strings.Builder
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch {
		case block.Type == blockCertificate:
			certs.Write(pem.EncodeToMemory(block))
		case strings.HasSuffix(block.Type, "PRIVATE KEY"):
			keys.Write(pem.EncodeToMemory(block))
		}
	}
	if certs.Len() == 0 || keys.Len() == 0 {
		return "", "", errors.New("staged renewal does not contain both a certificate and a private key")
	}
	return certs.String(), keys.String(), nil
}

// parseCertificates decodes every CERTIFICATE block in PEM material.
func parseCertificates(pemBytes []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != blockCertificate {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		out = append(out, c)
	}
	return out, nil
}
