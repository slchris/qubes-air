// renewal.go — certificate renewal over the mTLS channel the agent already
// holds.
//
// The console initiates: it already dials every qube on a health sweep
// (service.AgentProber) and it owns the registry that knows when a certificate
// expires. The agent only answers, in two calls:
//
//	qubesair.BeginRenewal     ()                        -> {nonce, csr_pem}
//	qubesair.CompleteRenewal  {nonce, cert_pem, ca_pem}  -> {installed_fingerprint, not_after}
//
// The private key NEVER crosses the network. The agent generates it here and
// returns only a certificate signing request. Bootstrap now works the same way
// (internal/agent/bootstrap.go), so a private key is never shipped in
// cloud-init at all — a key born on the host it authenticates cannot leak from
// anywhere it was never sent.
//
// The old certificate is left to expire rather than revoked on renewal.
// Revoking at the moment of issue would kill connections that are in flight and
// open a window in which the agent holds nothing the console will accept.

package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// Renewal service names. Both are BUILTIN: they manipulate this process's own
// TLS state, so they cannot be /etc/qubes-rpc scripts — see builtin.go.
const (
	ServiceBeginRenewal    = "qubesair.BeginRenewal"
	ServiceCompleteRenewal = "qubesair.CompleteRenewal"
)

// DefaultPendingRenewalTTL bounds how long a generated key waits for its
// certificate.
//
// A renewal is two calls on one open tunnel; the console signs in between, and
// that takes milliseconds. Five minutes is generous enough that a slow console
// or a retried call still lands, and short enough that a renewal abandoned
// halfway — the console crashed, the tunnel dropped — does not pin private key
// material in memory for the rest of the agent's uptime.
const DefaultPendingRenewalTTL = 5 * time.Minute

// maxPendingRenewals caps how many keys may be waiting at once.
//
// Every BeginRenewal allocates a P-256 key and holds it until CompleteRenewal
// or the TTL, so an unbounded map is a memory-growth vector reachable by
// anything that can complete an mTLS handshake — including a compromised
// console-side credential, which is exactly the case where the agent should
// degrade rather than die. Four is well above what renewal needs (one, plus a
// retry racing a still-live predecessor) and low enough that the ceiling is
// measured in kilobytes.
const maxPendingRenewals = 4

// nonceBytes is the size of the renewal nonce.
//
// It is a lookup key for an in-memory entry, not an authenticator — the mTLS
// peer is already authenticated and only that peer can reach these calls — but
// it is generated from crypto/rand at a size where guessing one is not a thing
// anyone need reason about.
const nonceBytes = 16

// Renewal errors.
var (
	// ErrNoPendingRenewal means the nonce is unknown: never issued, already
	// used, or expired.
	ErrNoPendingRenewal = errors.New("no pending renewal for this nonce")
	// ErrTooManyPendingRenewals means the pending table is full.
	ErrTooManyPendingRenewals = errors.New("too many renewals already in flight")
	// ErrRenewalKeyMismatch means the signed certificate does not match the key
	// the agent generated for this renewal.
	ErrRenewalKeyMismatch = errors.New("renewed certificate does not match the pending private key")
	// ErrRenewalIdentityMismatch means the signed certificate names a different
	// identity than this agent's.
	ErrRenewalIdentityMismatch = errors.New("renewed certificate does not name this agent")
	// ErrUntrustedRenewalCA means the renewal offered CA material this agent
	// does not already trust.
	ErrUntrustedRenewalCA = errors.New("renewal offered a CA this agent does not already trust")
)

// beginRenewalResponse is the reply to qubesair.BeginRenewal.
type beginRenewalResponse struct {
	Nonce  string `json:"nonce"`
	CSRPEM string `json:"csr_pem"`
}

// completeRenewalRequest is the body of qubesair.CompleteRenewal.
type completeRenewalRequest struct {
	Nonce   string `json:"nonce"`
	CertPEM string `json:"cert_pem"`
	CAPEM   string `json:"ca_pem"`
}

// completeRenewalResponse is the reply to qubesair.CompleteRenewal.
//
// The fingerprint is the registry key, so the console can confirm that the
// certificate it registered is the one actually installed rather than assuming
// it from a successful call.
type completeRenewalResponse struct {
	InstalledFingerprint string    `json:"installed_fingerprint"`
	NotAfter             time.Time `json:"not_after"`
}

// pendingRenewal is a key waiting for its certificate.
type pendingRenewal struct {
	key *ecdsa.PrivateKey
	// expiry deletes the entry without a sweeper goroutine to own. A sweeper
	// would need a lifecycle, and one that is never started turns the TTL into
	// a comment; a per-entry timer cannot be forgotten.
	expiry *time.Timer
}

// RenewalService implements the two renewal builtins.
type RenewalService struct {
	id  *Identity
	ttl time.Duration

	mu      sync.Mutex
	pending map[string]*pendingRenewal
}

// NewRenewalService builds the renewal handler over an identity.
func NewRenewalService(id *Identity, ttl time.Duration) *RenewalService {
	if ttl <= 0 {
		ttl = DefaultPendingRenewalTTL
	}
	return &RenewalService{
		id:      id,
		ttl:     ttl,
		pending: make(map[string]*pendingRenewal),
	}
}

// RegisterBuiltins binds both renewal calls on an invoker.
func (r *RenewalService) RegisterBuiltins(inv *LocalInvoker) error {
	if err := inv.RegisterBuiltin(ServiceBeginRenewal, r.Begin); err != nil {
		return err
	}
	return inv.RegisterBuiltin(ServiceCompleteRenewal, r.Complete)
}

// Begin generates a fresh key pair and returns a CSR for it.
//
// The key stays in this process, in memory, keyed by the returned nonce. It is
// never written: until a matching signed certificate comes back there is
// nothing worth persisting, and a key on disk with no certificate is one of the
// states Install exists to make impossible.
func (r *RenewalService) Begin(_ context.Context, _ string, _ []byte) ([]byte, error) {
	leaf, err := r.id.Leaf()
	if err != nil {
		return nil, fmt.Errorf("cannot renew: %w", err)
	}

	// P-256, matching pki.IssueAgentCert. A renewal that quietly changed the
	// algorithm would be discovered by whatever first failed to parse it.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate renewal key: %w", err)
	}

	// The subject is copied from the certificate we currently hold. The agent
	// does not get to choose its own name: the console verifies the peer's
	// common name against the qube it dialed, so asking for anything else
	// would either be refused or produce an identity the console can no longer
	// recognize.
	//
	// RawSubject rather than Subject, so the copy is exact. Re-marshaling a
	// parsed pkix.Name silently drops any attribute Go has no struct field for,
	// which would make the renewed certificate subtly different from the one it
	// replaces — a difference that only shows up wherever something compares
	// the full subject rather than the common name.
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		RawSubject:         leaf.RawSubject,
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}, key)
	if err != nil {
		return nil, fmt.Errorf("create CSR: %w", err)
	}

	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	if err := r.store(nonce, key); err != nil {
		return nil, err
	}

	log.Printf("agent renewal: issued CSR for %q (nonce %s)", leaf.Subject.CommonName, nonce[:8])
	return json.Marshal(beginRenewalResponse{
		Nonce:  nonce,
		CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})),
	})
}

// Complete verifies a signed certificate and installs it.
//
// Everything is checked before a byte is written, and any failure leaves the
// PREVIOUS certificate installed and serving. Degrading to "no valid identity"
// would be worse than a stale certificate that still has weeks left on it — the
// stale one can still be renewed, because renewal runs over the connection it
// authenticates.
func (r *RenewalService) Complete(_ context.Context, _ string, in []byte) ([]byte, error) {
	var req completeRenewalRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("malformed CompleteRenewal request: %w", err)
	}

	// Single use, consumed whether or not the rest succeeds. A nonce that
	// survived a failed attempt would let a caller retry against the same key
	// as many times as it liked; a fresh BeginRenewal costs one round trip and
	// leaves no such surface.
	//
	// The console must therefore treat a lost response as a failed renewal and
	// start again rather than replay this call — which is safe, since a renewal
	// that did install is visible as the new fingerprint on the next probe.
	key := r.take(req.Nonce)
	if key == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoPendingRenewal, shortNonce(req.Nonce))
	}

	leaf, err := singleCertificate(req.CertPEM)
	if err != nil {
		return nil, err
	}

	// The certificate must belong to the key we just generated. Without this
	// the agent would happily write a mismatched pair, and the failure would
	// arrive hours later at the next restart, as a process that cannot load its
	// own identity, with nothing pointing back at this moment. tls.X509KeyPair
	// inside Install catches it too; checking here is what makes the error say
	// which of the two things went wrong.
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, ErrRenewalKeyMismatch
	}

	// The identity must not change. The console holds the peer's common name to
	// "agent-<qube name>" (service.verifyAgentChain); installing a certificate
	// with any other name would make this agent unrecognizable to the probe
	// that is supposed to notice it is unhealthy.
	current, err := r.id.Leaf()
	if err != nil {
		return nil, err
	}
	if got, want := leaf.Subject.CommonName, current.Subject.CommonName; got != want {
		return nil, fmt.Errorf("%w: this agent is %q, the signed certificate names %q",
			ErrRenewalIdentityMismatch, want, got)
	}

	// A ca_pem that is not the CA we already trust is REFUSED, not adopted.
	// The console authenticated to us under that CA, so it is speaking with
	// authority the CA granted it — which is not authority to replace the CA.
	// Accepting one here would turn a certificate refresh into a takeover of
	// every future peer check, from a channel whose only credential was issued
	// by the root being replaced. Rotating the CA is deliberately not something
	// this call can do.
	if err := r.checkOfferedCA(req.CAPEM); err != nil {
		return nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal renewed key: %w", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: blockECKey, Bytes: keyDER}))

	// Install re-verifies the pair and the chain before it writes anything, and
	// commits both files as one atomic unit.
	pair, err := r.id.Install(req.CertPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	fp := Fingerprint(pair.Leaf)
	log.Printf("agent renewal: installed certificate %s for %q, valid until %s",
		fp[:16], pair.Leaf.Subject.CommonName, pair.Leaf.NotAfter.UTC().Format(time.RFC3339))

	return json.Marshal(completeRenewalResponse{
		InstalledFingerprint: fp,
		NotAfter:             pair.Leaf.NotAfter.UTC(),
	})
}

// checkOfferedCA refuses CA material the agent does not already trust.
// The decision itself is shared with bootstrap — see verifyOfferedCA.
func (r *RenewalService) checkOfferedCA(caPEM string) error {
	return verifyOfferedCA(r.id, caPEM)
}

// store records a pending key, refusing to grow past the cap.
func (r *RenewalService) store(nonce string, key *ecdsa.PrivateKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.pending) >= maxPendingRenewals {
		// Refusing beats evicting the oldest: eviction would let a caller that
		// keeps starting renewals push out the one that is about to complete,
		// turning a memory bound into a way to prevent renewal entirely.
		return fmt.Errorf("%w (%d)", ErrTooManyPendingRenewals, len(r.pending))
	}
	r.pending[nonce] = &pendingRenewal{
		key:    key,
		expiry: time.AfterFunc(r.ttl, func() { r.discard(nonce) }),
	}
	return nil
}

// take removes and returns a pending key, or nil if there is none.
func (r *RenewalService) take(nonce string) *ecdsa.PrivateKey {
	if nonce == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.pending[nonce]
	if !ok {
		return nil
	}
	delete(r.pending, nonce)
	p.expiry.Stop()
	return p.key
}

// discard drops an expired pending renewal.
func (r *RenewalService) discard(nonce string) {
	r.mu.Lock()
	_, ok := r.pending[nonce]
	delete(r.pending, nonce)
	r.mu.Unlock()
	if ok {
		log.Printf("agent renewal: abandoned renewal %s expired after %s; its key was discarded",
			shortNonce(nonce), r.ttl)
	}
}

// pendingCount reports how many renewals are waiting. Test support.
func (r *RenewalService) pendingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// singleCertificate parses exactly one certificate from PEM material.
func singleCertificate(certPEM string) (*x509.Certificate, error) {
	certs, err := parseCertificates([]byte(certPEM))
	if err != nil {
		return nil, fmt.Errorf("parse signed certificate: %w", err)
	}
	if len(certs) == 0 {
		return nil, errors.New("cert_pem contains no certificate")
	}
	return certs[0], nil
}

func newNonce() (string, error) {
	b := make([]byte, nonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate renewal nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// shortNonce abbreviates a nonce for logs and errors.
//
// The value on the CompleteRenewal path is caller-supplied, so it is echoed
// back only if it looks like a nonce we could have issued. Copying arbitrary
// remote bytes into a log line is how newline injection gets a forged entry
// into the same file an operator reads to work out what happened.
func shortNonce(nonce string) string {
	if len(nonce) > 8 {
		nonce = nonce[:8]
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return "<malformed>"
	}
	return nonce
}
