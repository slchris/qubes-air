// bootstrap.go — the agent's FIRST identity, obtained without a private key
// ever crossing the network.
//
// Renewal (renewal.go) already has the agent generate its own key and hand out
// only a CSR, but renewal is authenticated by the mTLS certificate the agent
// does not yet have. First boot is the one moment that leaves. The bootstrap
// token breaks the cycle: cloud-init delivers {token, ca.pem} — no key, no
// certificate — and the console dials in to run two calls that mirror
// renewal's shape:
//
//	qubesair.BeginBootstrap     ()                         -> {nonce, token, csr_pem}
//	qubesair.CompleteBootstrap  {nonce, cert_pem, ca_pem}   -> {installed_fingerprint, not_after}
//
// The console dials the agent, not the reverse, because that is the direction
// every other console↔agent conversation already uses (probing, renewal), and
// because the reverse direction would demand connectivity that does not
// otherwise need to exist — see docs/bootstrap-design.md §9.3.
//
// Authentication is deliberately asymmetric:
//
//   - The agent trusts the CALLER because this listener requires a client
//     certificate chaining to the CA cloud-init delivered. The token is never
//     handed to a peer the CA did not vouch for; that check lives in the TLS
//     config, before Begin ever runs.
//   - The console trusts the AGENT because of the token. The listener's own
//     certificate is a self-signed placeholder — there is no real one yet, that
//     is the problem being solved — so the tunnel proves nothing about who
//     answered. Possession of the one-shot token is what does, and the console
//     redeems it against a store that guarantees exactly one winner.
//
// Like renewal, the agent installs BEFORE it replies. A lost reply therefore
// describes an agent that already holds its identity — recoverable by the next
// probe — never an agent that was told it succeeded and holds nothing.

package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
)

// Bootstrap service names. Builtins, for the same reason renewal's are: they
// mint this process's own identity, and a script in ServiceDir must not be
// able to pretend it did.
const (
	ServiceBeginBootstrap    = "qubesair.BeginBootstrap"
	ServiceCompleteBootstrap = "qubesair.CompleteBootstrap"
)

// DefaultPendingBootstrapTTL bounds how long a generated key waits for its
// certificate. Same figure and same reasoning as renewal's: the console signs
// in between the two calls on one open tunnel, which takes milliseconds, and a
// bootstrap abandoned halfway must not pin key material for the process's
// remaining uptime.
const DefaultPendingBootstrapTTL = DefaultPendingRenewalTTL

// maxPendingBootstraps caps how many keys may wait at once — renewal's
// rationale applies unchanged (renewal.go maxPendingRenewals).
const maxPendingBootstraps = 4

// placeholderCertLifetime is how long the self-signed listener certificate
// lasts. It authenticates nothing, so its expiry is not a security boundary;
// it only needs to outlive the longest plausible gap between the agent
// starting and the console dialing in, with slack for a first boot whose
// clock has not settled yet.
const placeholderCertLifetime = 24 * time.Hour

// Bootstrap errors.
var (
	// ErrAlreadyBootstrapped means this agent holds an identity, so bootstrap
	// is closed. The token this process was born with is spent or void; a
	// caller wanting a new certificate is describing renewal.
	ErrAlreadyBootstrapped = errors.New("agent already holds an identity; bootstrap is closed")
	// ErrNoPendingBootstrap means the nonce is unknown: never issued, already
	// used, or expired.
	ErrNoPendingBootstrap = errors.New("no pending bootstrap for this nonce")
	// ErrTooManyPendingBootstraps means the pending table is full.
	ErrTooManyPendingBootstraps = errors.New("too many bootstraps already in flight")
	// ErrBootstrapKeyMismatch means the signed certificate does not match the
	// key this agent generated for the nonce it came with.
	ErrBootstrapKeyMismatch = errors.New("bootstrap certificate does not match the pending private key")
	// ErrBootstrapIdentityMismatch means the signed certificate names someone
	// other than this agent.
	ErrBootstrapIdentityMismatch = errors.New("bootstrap certificate does not name this agent")
)

// beginBootstrapResponse is the reply to qubesair.BeginBootstrap.
//
// The token rides in the RESPONSE, not the request: the agent is the party
// holding it, and it surrenders it only over a handshake whose client
// certificate already chained to the cloud-init CA.
type beginBootstrapResponse struct {
	Nonce  string `json:"nonce"`
	Token  string `json:"token"`
	CSRPEM string `json:"csr_pem"`
}

// completeBootstrapRequest is the body of qubesair.CompleteBootstrap.
type completeBootstrapRequest struct {
	Nonce   string `json:"nonce"`
	CertPEM string `json:"cert_pem"`
	CAPEM   string `json:"ca_pem"`
}

// completeBootstrapResponse is the reply to qubesair.CompleteBootstrap. The
// fingerprint lets the console confirm the certificate it registered is the
// one actually installed, rather than assuming it from a successful call.
type completeBootstrapResponse struct {
	InstalledFingerprint string    `json:"installed_fingerprint"`
	NotAfter             time.Time `json:"not_after"`
}

// BootstrapService implements the two bootstrap builtins and supplies the
// listener's certificate while there is no real one.
type BootstrapService struct {
	id         *Identity
	remoteName string
	token      string
	ttl        time.Duration

	// placeholder is what the listener presents until Install succeeds. Its
	// key is generated here and never persisted: the certificate exists only
	// so a TLS listener has something to hold the port open with.
	placeholder *tls.Certificate

	// onInstalled fires once, after the identity is installed and before the
	// reply is sent. The caller uses it to scrub the token file — best effort,
	// since the token is already spent server-side by then.
	onInstalled func()

	mu      sync.Mutex
	pending map[string]*pendingRenewal
	done    bool
}

// NewBootstrapService builds the bootstrap handler over a pending identity.
func NewBootstrapService(id *Identity, remoteName, token string, onInstalled func()) (*BootstrapService, error) {
	if id == nil {
		return nil, errors.New("bootstrap needs the identity it is meant to fill")
	}
	if remoteName == "" {
		return nil, errors.New("bootstrap needs the remote's name; it becomes the certificate's CN")
	}
	if token == "" {
		return nil, errors.New("bootstrap needs a token; without one the console cannot tell this agent from anyone answering its address")
	}
	placeholder, err := selfSignedPlaceholder(remoteName)
	if err != nil {
		return nil, fmt.Errorf("mint placeholder listener certificate: %w", err)
	}
	return &BootstrapService{
		id:          id,
		remoteName:  remoteName,
		token:       token,
		ttl:         DefaultPendingBootstrapTTL,
		placeholder: placeholder,
		onInstalled: onInstalled,
		pending:     make(map[string]*pendingRenewal),
	}, nil
}

// ServerCertificate implements transport/grpc.ServerCertSource.
//
// Once an identity is installed it wins — including on the very tunnel the
// bootstrap ran over, at the next handshake — and until then the placeholder
// keeps the listener able to accept the console's call at all.
func (b *BootstrapService) ServerCertificate() (*tls.Certificate, error) {
	if cert, err := b.id.ServerCertificate(); err == nil {
		return cert, nil
	}
	return b.placeholder, nil
}

// RegisterBuiltins binds both bootstrap calls on an invoker.
func (b *BootstrapService) RegisterBuiltins(inv *LocalInvoker) error {
	if err := inv.RegisterBuiltin(ServiceBeginBootstrap, b.Begin); err != nil {
		return err
	}
	return inv.RegisterBuiltin(ServiceCompleteBootstrap, b.Complete)
}

// Begin generates a fresh key pair and surrenders the token beside a CSR.
//
// The key stays in this process, in memory, keyed by the returned nonce,
// exactly as a renewal's does. The CSR names this agent — the console's signer
// refuses any CSR whose CN disagrees with what the redeemed token authorizes,
// so an agent asking for another name would only burn its own token.
func (b *BootstrapService) Begin(_ context.Context, _ string, _ []byte) ([]byte, error) {
	if b.id.HasCertificate() {
		// Do not hand the token to anyone once an identity exists. It is spent
		// or superseded server-side, but "spent" is the console's knowledge,
		// not this host's; refusing here costs nothing and keeps the token
		// from traveling after its purpose is over.
		return nil, ErrAlreadyBootstrapped
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate bootstrap key: %w", err)
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: pki.AgentCommonName(b.remoteName)},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}, key)
	if err != nil {
		return nil, fmt.Errorf("create bootstrap CSR: %w", err)
	}

	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	if err := b.store(nonce, key); err != nil {
		return nil, err
	}

	log.Printf("agent bootstrap: issued CSR for %q (nonce %s)", pki.AgentCommonName(b.remoteName), nonce[:8])
	return json.Marshal(beginBootstrapResponse{
		Nonce:  nonce,
		Token:  b.token,
		CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})),
	})
}

// Complete verifies a signed certificate and installs it as this agent's
// first identity.
//
// Everything is checked before a byte is written; any failure leaves the agent
// still bootstrappable with its next Begin — except that a failure AFTER the
// console redeemed the token means bootstrap needs a fresh token, which is the
// fail-closed cost the token design accepts (service.BootstrapIssuer).
func (b *BootstrapService) Complete(_ context.Context, _ string, in []byte) ([]byte, error) {
	var req completeBootstrapRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("malformed CompleteBootstrap request: %w", err)
	}

	// Single use, consumed whether or not the rest succeeds — renewal's
	// reasoning, unchanged (renewal.go Complete).
	key := b.take(req.Nonce)
	if key == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoPendingBootstrap, shortNonce(req.Nonce))
	}

	leaf, err := singleCertificate(req.CertPEM)
	if err != nil {
		return nil, err
	}

	// The certificate must belong to the key generated at Begin.
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, ErrBootstrapKeyMismatch
	}

	// The certificate must name THIS agent. There is no current leaf to compare
	// against — that is what bootstrap means — so the expectation comes from
	// the same derivation the CSR used. A certificate for another name would
	// make this host unrecognizable to the prober that is about to look for it.
	if got, want := leaf.Subject.CommonName, pki.AgentCommonName(b.remoteName); got != want {
		return nil, fmt.Errorf("%w: this agent is %q, the signed certificate names %q",
			ErrBootstrapIdentityMismatch, want, got)
	}

	// CA material that is not the CA cloud-init delivered is refused, not
	// adopted — the same rule as renewal, for the same reason: a certificate
	// delivery must not be able to replace the trust root that authenticated it.
	if err := verifyOfferedCA(b.id, req.CAPEM); err != nil {
		return nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal bootstrap key: %w", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: blockECKey, Bytes: keyDER}))

	// Install re-verifies the pair and the chain before writing, and commits
	// both files as one atomic unit — the identical machinery renewal uses.
	pair, err := b.id.Install(req.CertPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	fp := Fingerprint(pair.Leaf)
	log.Printf("agent bootstrap: installed first certificate %s for %q, valid until %s",
		fp[:16], pair.Leaf.Subject.CommonName, pair.Leaf.NotAfter.UTC().Format(time.RFC3339))

	b.finish()

	return json.Marshal(completeBootstrapResponse{
		InstalledFingerprint: fp,
		NotAfter:             pair.Leaf.NotAfter.UTC(),
	})
}

// finish runs the one-time post-install hook and drops any leftover pending
// keys — nothing can complete against them once an identity is installed.
func (b *BootstrapService) finish() {
	b.mu.Lock()
	already := b.done
	b.done = true
	for nonce, p := range b.pending {
		p.expiry.Stop()
		delete(b.pending, nonce)
	}
	b.mu.Unlock()

	if !already && b.onInstalled != nil {
		b.onInstalled()
	}
}

// store records a pending key, refusing to grow past the cap.
func (b *BootstrapService) store(nonce string, key *ecdsa.PrivateKey) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.pending) >= maxPendingBootstraps {
		return fmt.Errorf("%w (%d)", ErrTooManyPendingBootstraps, len(b.pending))
	}
	b.pending[nonce] = &pendingRenewal{
		key:    key,
		expiry: time.AfterFunc(b.ttl, func() { b.discard(nonce) }),
	}
	return nil
}

// take removes and returns a pending key, or nil if there is none.
func (b *BootstrapService) take(nonce string) *ecdsa.PrivateKey {
	if nonce == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	p, ok := b.pending[nonce]
	if !ok {
		return nil
	}
	delete(b.pending, nonce)
	p.expiry.Stop()
	return p.key
}

// discard drops an expired pending bootstrap.
func (b *BootstrapService) discard(nonce string) {
	b.mu.Lock()
	_, ok := b.pending[nonce]
	delete(b.pending, nonce)
	b.mu.Unlock()
	if ok {
		log.Printf("agent bootstrap: abandoned bootstrap %s expired after %s; its key was discarded",
			shortNonce(nonce), b.ttl)
	}
}

// pendingCount reports how many bootstraps are waiting. Test support.
func (b *BootstrapService) pendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// verifyOfferedCA refuses CA material an identity does not already trust.
//
// Shared by renewal and bootstrap: in both, the peer proved itself under the
// CA the agent already holds, which is authority granted BY that root, not
// authority to replace it. Rotating the CA is deliberately something neither
// call can do.
func verifyOfferedCA(id *Identity, caPEM string) error {
	if caPEM == "" {
		return nil
	}
	offered, err := parseCertificates([]byte(caPEM))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUntrustedRenewalCA, err)
	}
	if len(offered) == 0 {
		return fmt.Errorf("%w: ca_pem contains no certificate", ErrUntrustedRenewalCA)
	}
	for _, c := range offered {
		if !id.TrustsCA(c.Raw) {
			return fmt.Errorf("%w: %q", ErrUntrustedRenewalCA, c.Subject.CommonName)
		}
	}
	return nil
}

// selfSignedPlaceholder mints the certificate the listener presents before it
// has a real one.
//
// It proves nothing and is trusted by nobody: the console dialing a
// bootstrapping agent does not verify the server certificate — it cannot,
// nothing has been issued — and relies on the token instead. This exists only
// because a TLS listener must present something to complete a handshake. The
// key behind it is generated here and discarded with the process.
func selfSignedPlaceholder(remoteName string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(0).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "bootstrap-" + remoteName},
		// Generous NotBefore: a first boot's clock may still be settling, and a
		// placeholder that is "not yet valid" reads as a dead VM from outside.
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(placeholderCertLifetime),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}
