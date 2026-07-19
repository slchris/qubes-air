package service

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

// The renewal wire contract. Both are BUILTIN services inside the agent process
// rather than /etc/qubes-rpc/<name> scripts like every other service: they
// manipulate the agent's own TLS state and its in-memory pending key, which a
// forked shell script cannot do.
//
//	BeginRenewal    in: (empty)                        out: {nonce, csr_pem}
//	CompleteRenewal in: {nonce, cert_pem, ca_pem}      out: {installed_fingerprint, not_after}
const (
	beginRenewalService    = "qubesair.BeginRenewal"
	completeRenewalService = "qubesair.CompleteRenewal"
)

// renewRelayName is the relay identity the console presents while renewing.
//
// Deliberately NOT probeRelayName. The health sweep holds a tunnel open under
// that name every interval, and a renewal runs concurrently with it; an agent
// that keys its session table by relay name would have the two evict each
// other, which would show up as a renewal that fails only on busy fleets. A
// distinct name also makes the agent's log say which console operation was in
// flight. It must satisfy transport.ValidName.
const renewRelayName = "console-renew"

// DefaultCertRenewalTimeout bounds one qube's whole renewal exchange.
//
// Wider than a probe because the agent generates a fresh P-256 key pair and the
// console signs it between the two calls, but still bounded: an agent that
// accepts TCP and then goes quiet must not hold the renewal sweep, which would
// starve every qube behind it — the same unbounded-wait failure that has
// already wedged this console once.
const DefaultCertRenewalTimeout = 30 * time.Second

// Clock skew between this console and an agent, and what renewal does about it.
//
// WHOSE CLOCK DECIDES WHAT:
//
//   - Whether a certificate is DUE is decided entirely by the console: the
//     scheduler compares the console's clock against expires_at, a value the
//     console's own CA wrote. An agent's clock cannot make a certificate look
//     fresh or overdue. Nothing to defend there.
//   - Whether a certificate is VALID at handshake time is decided by whoever is
//     verifying. The console verifies the agent's certificate on the console's
//     clock, so that direction is self-consistent too. But the AGENT verifies
//     the console's relay certificate on the AGENT's clock — and that is the one
//     place a disagreement decides whether renewal can happen at all.
//
// The dangerous direction is an agent running AHEAD of the console. The console
// sees weeks of runway, the agent has already passed the relay certificate's
// notAfter, the handshake fails, and every sweep reports "unreachable" — which
// reads as a dead VM, so the certificate quietly runs out while somebody looks
// for a hypervisor problem. A five-minute relay certificate (what the prober
// uses, and what renewal used to borrow) makes five minutes of skew fatal.
//
// An hour instead: long enough to survive the skew a heterogeneous cluster
// actually produces — hosts whose NTP has drifted, a VM resumed from a snapshot
// with a stale RTC — and still short enough that a leaked relay certificate is
// worth little, since one renewal exchange is bounded by
// DefaultCertRenewalTimeout and lasts seconds.
//
// WHAT THIS DOES NOT PROTECT AGAINST, and cannot from here:
//
//   - An agent whose clock is BEHIND the console by more than the CA's backdate.
//     The tolerance in that direction is caClockSkewBackdate, set by the CA when
//     it fills in NotBefore, and widening it means changing certificate issuance
//     for every consumer, not just renewal.
//   - Skew larger than an hour in either direction. Renewal will report the qube
//     unreachable; clockSkewHint is what stops that being misread.
//   - A wrong console clock. Everything here is measured against it, so if it is
//     wrong the whole fleet's arithmetic is wrong in the same direction and
//     nothing local can detect it.
const (
	renewRelayCertLifetime = time.Hour
	// caClockSkewBackdate mirrors the NotBefore backdate in internal/pki. It is
	// quoted in operator-facing messages so the tolerance an operator is told
	// about is the one the CA actually applies; if pki changes, this comment is
	// the thing that has to change with it.
	caClockSkewBackdate = 5 * time.Minute
)

// CertRenewalStatus is why a renewal ended the way it did.
//
// The split that matters is AgentAnswered: "the agent never spoke to us" has
// the same remedy as a dead agent (go look at the VM), while everything after
// BeginRenewal succeeded means the agent is alive and healthy and the CONSOLE
// side — the CA, the registry, or an identity that does not match — is what
// broke. Collapsing those would send an operator to SSH into a perfectly
// healthy qube.
type CertRenewalStatus string

// Certificate renewal statuses.
const (
	// CertRenewalOK means the agent installed a freshly signed certificate.
	CertRenewalOK CertRenewalStatus = "ok"
	// CertRenewalUnreachable means the agent never answered BeginRenewal: no
	// address, nothing listening, a rejected handshake, or no reply. Same
	// diagnosis as an unreachable probe.
	CertRenewalUnreachable CertRenewalStatus = "unreachable"
	// CertRenewalRefused means the agent answered but asked to be issued an
	// identity that is not its own. This is an escalation attempt or a badly
	// broken agent, never a typo to be corrected — see verifyRenewalCSR.
	CertRenewalRefused CertRenewalStatus = "refused"
	// CertRenewalConsoleFailed means the agent did its part and the console
	// could not do its own: the CA would not sign, or the registry would not
	// record the result. The qube is fine; this console is not.
	CertRenewalConsoleFailed CertRenewalStatus = "console_failed"
	// CertRenewalInstallFailed means the certificate was signed and registered
	// but the agent did not install it. The agent keeps its previous
	// certificate, so nothing is broken yet — but nothing was renewed either.
	CertRenewalInstallFailed CertRenewalStatus = "install_failed"
	// CertRenewalWithdrawn means the certificate being renewed was revoked while
	// the renewal was in flight — almost always a purge racing the sweep. The
	// renewal was correctly refused at the registry and nothing was delivered.
	//
	// Distinct from CertRenewalConsoleFailed because NOTHING IS BROKEN: an
	// operator took this qube's access away and the machinery obeyed. Reporting
	// it as a console fault would send someone to debug a CA that is working.
	CertRenewalWithdrawn CertRenewalStatus = "withdrawn"
	// CertRenewalIdentityMismatch means the registry's record of the certificate
	// being replaced names a different qube or subject than the one just signed.
	//
	// Distinct from CertRenewalWithdrawn because this one IS a defect: the
	// renewal machinery is confused about which agent it is talking to, and no
	// amount of retrying will unconfuse it. It needs a human reading rows, not a
	// backoff.
	CertRenewalIdentityMismatch CertRenewalStatus = "identity_mismatch"
	// CertRenewalNotConfigured means this console cannot renew at all (no CA, no
	// CSR signing entry point, no registry). Nothing was learned about the
	// agent, which is deliberately not the same as a failure.
	CertRenewalNotConfigured CertRenewalStatus = "not_configured"
)

// AgentAnswered reports whether the agent proved it was alive during this
// renewal. It is what decides whether a renewal failure may be recorded as
// "unreachable": a status that got past BeginRenewal is positive evidence the
// agent is up, and overwriting a healthy reading with "unreachable" would send
// the operator after the wrong machine.
func (s CertRenewalStatus) AgentAnswered() bool {
	switch s {
	case CertRenewalOK, CertRenewalRefused, CertRenewalConsoleFailed, CertRenewalInstallFailed,
		CertRenewalWithdrawn, CertRenewalIdentityMismatch:
		return true
	default:
		return false
	}
}

// CertRenewalResult is everything one renewal attempt learned.
type CertRenewalResult struct {
	QubeID   string            `json:"qube_id"`
	QubeName string            `json:"qube_name"`
	Status   CertRenewalStatus `json:"status"`
	// Reason explains a non-ok status in terms an operator can act on.
	Reason string `json:"reason,omitempty"`
	// OldFingerprint is the certificate the qube held going in. It is NOT
	// revoked on success — see CertRenewer.Renew.
	OldFingerprint string `json:"old_fingerprint,omitempty"`
	// NewFingerprint is the certificate that was signed and registered. It is
	// set even when the agent failed to install it, because the registry row
	// exists at that point and an operator tracing an orphan row needs it.
	NewFingerprint string    `json:"new_fingerprint,omitempty"`
	NotAfter       time.Time `json:"not_after,omitempty"`
	// PreviousNotAfter is when the certificate being replaced expires. It is
	// what turns "renewal failed" into "renewal failed and this qube goes dark
	// on the 14th", which is the difference between a ticket and an incident.
	PreviousNotAfter time.Time     `json:"previous_not_after,omitempty"`
	Duration         time.Duration `json:"-"`
	At               time.Time     `json:"at"`
}

// SignedAgentCert is a certificate the CA produced from an agent's CSR.
//
// There is no key here, and that is the whole point of CSR-based renewal: the
// agent generated the key pair and kept the private half. The previous scheme
// minted the key on the console and shipped it inside Proxmox cloud-init data,
// where anyone holding VM.Config.Cloudinit could read it.
type SignedAgentCert struct {
	CertPEM     string
	CAPEM       string
	Fingerprint string
	NotAfter    time.Time
	SubjectCN   string
}

// CSRSigner signs an agent-generated CSR for a named identity.
//
// wantCN is passed in rather than taken from the CSR so the signer enforces the
// caller's identity decision instead of the requester's claim. Implemented by
// *CertIssuer.
type CSRSigner interface {
	SignAgentCSR(ctx context.Context, csrPEM, wantCN string, lifetime time.Duration) (*SignedAgentCert, error)
}

// CertRegistrar records an issued certificate as permitted to connect.
// Implemented by *repository.AgentCertRepository.
type CertRegistrar interface {
	Register(ctx context.Context, c *repository.AgentCert) error
}

// CertRenewalRecorder registers a renewed certificate CONDITIONALLY on the one
// it replaces still being live and still belonging to the same qube.
// Implemented by *repository.AgentCertRepository.
//
// Separate from CertRegistrar because the two answer different questions.
// Register asks "record this certificate"; RecordRenewal asks "record this
// certificate ONLY IF the credential it succeeds is still valid" — one
// statement, so there is no window between checking and writing for a purge to
// commit in. See repository.RecordRenewal for the failure it prevents: a qube
// purged mid-renewal being handed a working credential straight back, in a row
// that is indistinguishable from a legitimate agent's.
type CertRenewalRecorder interface {
	RecordRenewal(ctx context.Context, previousFingerprint string, renewed *repository.AgentCert) error
}

// CertRevoker withdraws a certificate. Separate from CertRegistrar because a
// renewer may legitimately have one without the other, and because revoking is
// a trust decision while registering is bookkeeping.
type CertRevoker interface {
	Revoke(ctx context.Context, fingerprint, reason string) error
}

// AgentCertLister reads the certificates already issued to a qube, newest
// first. Implemented by *repository.AgentCertRepository.
type AgentCertLister interface {
	ListByQube(ctx context.Context, qubeID string) ([]*repository.AgentCert, error)
}

// caCSRSigner is the CSR-signing entry point internal/pki is adding.
//
// Asserted at runtime rather than called directly because that change lands
// separately; this file must compile and its tests must pass either way. If the
// method arrives with a different signature the assertion fails and every
// renewal reports CertRenewalNotConfigured with the exact signature that was
// looked for — loud and one line to fix, rather than a fleet that quietly
// stopped renewing, which is precisely the failure class this feature exists to
// close.
// SignAgentCSR signs an agent's CSR against the console CA.
//
// It deliberately does NOT register the result. Signing and registering are two
// steps with an order that matters during renewal (see CertRenewer.Renew), and
// folding them together here would take that choice away from the caller —
// unlike IssueFor, where the certificate is minted and delivered in one shot and
// an unregistered certificate could never work.
func (c *CertIssuer) SignAgentCSR(
	ctx context.Context, csrPEM, wantCN string, lifetime time.Duration,
) (*SignedAgentCert, error) {
	ca, err := c.loadOrCreateCA(ctx)
	if err != nil {
		return nil, fmt.Errorf("certificate authority: %w", err)
	}
	if lifetime <= 0 {
		lifetime = pki.DefaultAgentCertLifetime
	}

	// Called directly on the concrete CA, NOT through an interface assertion.
	//
	// The assertion that used to sit here was meant as a graceful degradation
	// for a CA that could not sign CSRs. It silently became the opposite: the
	// interface declared (*pki.Bundle, error) while the CA returns
	// (*pki.SignedCert, error), so the assertion ALWAYS failed and every renewal
	// in the fleet reported "not configured". Nothing caught it because the
	// tests all supply a fake signer, so no test ever crossed the seam — a
	// dead feature with a fully green suite, which is the exact failure class
	// renewal exists to close.
	//
	// A direct call cannot drift: a signature change is a compile error.
	bundle, err := ca.SignAgentCSR(csrPEM, wantCN, lifetime)
	if err != nil {
		return nil, fmt.Errorf("sign CSR for %q: %w", wantCN, err)
	}
	if bundle == nil || strings.TrimSpace(bundle.CertPEM) == "" {
		return nil, fmt.Errorf("signing %q produced no certificate", wantCN)
	}

	// Re-derive the fingerprint, expiry and subject from the certificate itself
	// rather than trusting what came back beside it. The fingerprint is the
	// registry key the next handshake is matched against: recording a value that
	// does not hash the bytes the agent will actually present would lock the
	// qube out at renewal time, which is the one moment this code must not fail.
	leaf, err := parseCertPEM(bundle.CertPEM)
	if err != nil {
		return nil, fmt.Errorf("CA returned an unusable certificate for %q: %w", wantCN, err)
	}
	if leaf.Subject.CommonName != wantCN {
		// The CA signed a different identity than was asked for. Never paper over
		// this: it would register a row under one name for a certificate that
		// authenticates as another.
		return nil, fmt.Errorf(
			"CA signed %q when %q was requested; refusing to register a mismatched identity",
			leaf.Subject.CommonName, wantCN)
	}

	// The CA certificate comes from MarshalCA, not from the signer's reply. It
	// is the same material either way, and taking it from the long-standing API
	// keeps this path working regardless of what the new entry point chooses to
	// populate.
	caPEM, _, err := ca.MarshalCA()
	if err != nil {
		return nil, fmt.Errorf("serialize CA certificate: %w", err)
	}

	return &SignedAgentCert{
		CertPEM:     bundle.CertPEM,
		CAPEM:       caPEM,
		Fingerprint: pki.FingerprintOf(leaf),
		NotAfter:    leaf.NotAfter,
		SubjectCN:   leaf.Subject.CommonName,
	}, nil
}

// CertRenewer replaces one qube's agent certificate over the mTLS channel the
// agent already holds.
//
// This exists because the only other delivery channel is cloud-init, which is
// read once at first boot. Rotating a certificate therefore meant REBUILDING the
// VM, which turned the 90-day certificate lifetime into a fleet rebuild period
// rather than a security parameter — and if nobody noticed, every qube in the
// fleet would go dark on the same day.
type CertRenewer struct {
	ca     CAProvider
	signer CSRSigner
	// registrar records the new certificate before the agent is told to install
	// it. Required: a certificate the agent holds but the registry does not know
	// is refused at the next handshake.
	registrar CertRegistrar
	// recorder is registrar's conditional form, used whenever the certificate
	// being replaced is known. It is what stops a renewal that started before a
	// purge from completing after it. Nil falls back to registrar, loudly.
	recorder CertRenewalRecorder
	// seen records that a certificate is genuinely in use. A successful install
	// IS such an observation — the agent confirmed it swapped — and without it
	// the new row stays unseen, dueness keeps being computed from the OLD
	// certificate, and the scheduler renews the same qube on every sweep.
	seen LastSeenRecorder
	// authz refuses revoked, expired and unregistered certificates at handshake
	// time. Nil disables the check, correct only where no registry exists.
	authz CertAuthorizer
	// revoker withdraws a certificate that was registered but never installed.
	// Without it a failed install leaves an orphan row that makes the qube look
	// permanently fresh, and renewal is never retried. See discardUninstalled.
	revoker CertRevoker
	// certs reads the certificate being replaced, for reporting only.
	certs AgentCertLister
	// lifetime is how long a renewed certificate is valid.
	lifetime time.Duration
	port     string
	timeout  time.Duration
}

// NewCertRenewer builds a renewer.
//
// agentListen is the same config value the agent was told to bind, so the port
// dialled here and the port the agent listens on cannot drift. Any nil
// dependency leaves the renewer unable to renew, which it reports as
// CertRenewalNotConfigured rather than as a failed agent.
func NewCertRenewer(
	ca CAProvider, signer CSRSigner, registrar CertRegistrar, certs AgentCertLister,
	agentListen string, timeout time.Duration,
) *CertRenewer {
	if timeout <= 0 {
		timeout = DefaultCertRenewalTimeout
	}
	// The registry is ONE object that registers, revokes, authorizes and records
	// renewals, so detect the extra roles rather than making every caller pass
	// the same thing four times and risk passing four different things.
	revoker, _ := registrar.(CertRevoker)
	authz, _ := registrar.(CertAuthorizer)
	seen, _ := registrar.(LastSeenRecorder)
	recorder, _ := registrar.(CertRenewalRecorder)
	if recorder == nil {
		// Said once, at construction, because the consequence is invisible at
		// runtime: renewals still succeed, and the one that completes moments
		// after a purge looks exactly like the rest.
		log.Printf("certrenew: the certificate registry cannot record conditional renewals; " +
			"a qube purged WHILE a renewal is in flight will be handed a fresh working certificate")
	}

	return &CertRenewer{
		ca:        ca,
		signer:    signer,
		registrar: registrar,
		recorder:  recorder,
		revoker:   revoker,
		authz:     authz,
		seen:      seen,
		certs:     certs,
		lifetime:  pki.DefaultAgentCertLifetime,
		port:      agentPortFrom(agentListen),
		timeout:   timeout,
	}
}

// Renew runs the two-call renewal against one qube.
//
// The order — BeginRenewal, verify, sign, REGISTER, CompleteRenewal — is chosen
// for what happens if this console dies partway through:
//
//   - Dying after registering but before CompleteRenewal leaves an unused row in
//     the registry for a certificate only the agent's discarded pending key could
//     ever have used. The agent still holds its PREVIOUS certificate, which is
//     still registered and still unrevoked, so it keeps authenticating. Inert.
//   - Registering AFTER CompleteRenewal would invert that: the agent installs a
//     certificate the registry has never heard of, and Authorize refuses it with
//     ErrCertNotRegistered on the very next handshake. The qube locks itself out
//     by successfully renewing.
//
// So the registry is always written first. The old certificate is never revoked
// here either: revoking on renewal would kill connections in flight and open a
// window in which the agent holds nothing valid, and it expires on its own
// anyway.
//
// Like Probe, this returns no error. "The agent did not renew" is a result that
// every caller has to record rather than abort on.
func (r *CertRenewer) Renew(ctx context.Context, qube *models.Qube) CertRenewalResult {
	started := time.Now()
	res := CertRenewalResult{At: started.UTC()}
	if qube != nil {
		res.QubeID, res.QubeName = qube.ID, qube.Name
	}

	done := func(status CertRenewalStatus, format string, args ...any) CertRenewalResult {
		res.Status = status
		res.Duration = time.Since(started)
		if format != "" {
			res.Reason = fmt.Sprintf(format, args...)
		}
		if status != CertRenewalOK {
			log.Printf("certrenew: qube %q certificate NOT renewed (%s): %s", res.QubeName, status, res.Reason)
		}
		return res
	}

	if qube == nil {
		return done(CertRenewalNotConfigured, "no qube given")
	}
	if r == nil || r.ca == nil || r.signer == nil || r.registrar == nil {
		return done(CertRenewalNotConfigured,
			"no certificate renewer configured; qube %q can only be re-credentialed by rebuilding it", qube.Name)
	}

	host := strings.TrimSpace(qube.IPAddress)
	if host == "" {
		return done(CertRenewalUnreachable,
			"qube %q has no IP address, so its agent cannot be reached to renew", qube.Name)
	}
	addr := net.JoinHostPort(host, r.port)

	// Recorded before anything can fail so a failure report can say WHEN this
	// qube goes dark, not merely that renewal did not work.
	if prev := r.currentCert(ctx, qube.ID); prev != nil {
		res.OldFingerprint = prev.Fingerprint
		if prev.ExpiresAt != nil {
			res.PreviousNotAfter = *prev.ExpiresAt
		}
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// Filled in by the handshake, and the reason the purge guard is precise: the
	// certificate this renewal REPLACES is the one the peer just authenticated
	// with, not whatever the registry currently ranks highest for this qube.
	peer := &verifiedPeer{}

	sess, err := r.dial(ctx, qube.Name, addr, peer)
	if err != nil {
		return done(CertRenewalUnreachable, "%s%s", err.Error(), clockSkewHint(err))
	}
	defer sess.close()

	// By POINTER, so what exchange learns reaches the caller. done closes over
	// this res; a copy would leave every field exchange fills in — the new
	// fingerprint above all — silently empty on the returned result, which is
	// how an operator ends up tracing an orphan certificate with nothing to
	// trace it by.
	return r.exchange(ctx, qube, sess, peer, &res, done)
}

// verifiedPeer remembers which certificate the agent authenticated with.
//
// Recorded during the TLS handshake rather than read back from the registry
// afterwards, because the two can disagree in a way that matters. A previous
// renewal whose install failed leaves a longer-lived row that
// discardUninstalled could not revoke — the registry then ranks that ORPHAN as
// the qube's best certificate, and a purge guard keyed on it would be checking
// a credential no agent has ever held.
type verifiedPeer struct {
	mu          sync.Mutex
	fingerprint string
}

func (p *verifiedPeer) note(fingerprint string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// First wins. The transport reconnects on its own, and a later handshake
	// during the same renewal must not move the predecessor out from under the
	// guard that is about to run.
	if p.fingerprint == "" {
		p.fingerprint = fingerprint
	}
}

func (p *verifiedPeer) presented() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fingerprint
}

// agentCaller is one authenticated channel to one agent.
//
// An interface so the protocol above can be exercised against a fake agent. The
// ordering it enforces — register before install — is the property that keeps a
// qube from locking itself out, and a property that can only be tested through a
// live gRPC tunnel is a property that will not be tested.
type agentCaller interface {
	call(ctx context.Context, target, service string, in []byte) ([]byte, error)
	address() string
}

// exchange runs the protocol once the tunnel is up.
func (r *CertRenewer) exchange(
	ctx context.Context, qube *models.Qube, sess agentCaller, peer *verifiedPeer, res *CertRenewalResult,
	done func(CertRenewalStatus, string, ...any) CertRenewalResult,
) CertRenewalResult {
	wantCN := agentCertCN(qube.Name)
	// Needed to re-dial if the exchange fails: discardUninstalled must ask the
	// agent what it actually holds before withdrawing anything.
	addr := sess.address()

	begun, err := beginRenewal(ctx, sess, qube.Name)
	if err != nil {
		return done(CertRenewalUnreachable, "%s did not answer %s: %v%s",
			sess.address(), beginRenewalService, err, clockSkewHint(err))
	}

	// The peer already proved which qube it is: the mTLS handshake pinned its
	// certificate's common name to wantCN (see verifyAgentChain). A CSR naming
	// anything else is an agent asking to be issued someone else's identity, so
	// it fails loudly instead of being silently corrected to the right name.
	if err := verifyRenewalCSR(begun.CSRPEM, wantCN); err != nil {
		return done(CertRenewalRefused,
			"agent at %s authenticated as %q but requested a certificate for a different identity: %v",
			sess.address(), wantCN, err)
	}

	signed, err := r.signer.SignAgentCSR(ctx, begun.CSRPEM, wantCN, r.lifetime)
	if err != nil {
		return done(CertRenewalConsoleFailed,
			"agent %q produced a valid request but this console could not sign it: %v", qube.Name, err)
	}
	res.NewFingerprint = signed.Fingerprint
	res.NotAfter = signed.NotAfter

	// Registered BEFORE the agent is told to install it. See Renew's comment:
	// the reverse order locks a qube out of its own fleet.
	if status, reason := r.registerRenewed(ctx, qube, peer, res.OldFingerprint, signed); status != CertRenewalOK {
		// Not delivered. Whatever went wrong, the agent keeps the certificate it
		// already has: handing it one the registry does not vouch for would be
		// rejected at the next handshake, which is strictly worse.
		return done(status, "%s", reason)
	}

	installed, err := completeRenewal(ctx, sess, qube.Name, begun.Nonce, signed)
	if err != nil {
		r.discardUninstalled(ctx, qube, addr, signed.Fingerprint)
		return done(CertRenewalInstallFailed,
			"certificate %s was signed and registered but %s failed: %v "+
				"(the agent keeps its previous certificate, which expires %s)",
			shortFingerprint(signed.Fingerprint), completeRenewalService, err,
			expiryText(res.PreviousNotAfter))
	}
	if installed.InstalledFingerprint != signed.Fingerprint {
		r.discardUninstalled(ctx, qube, addr, signed.Fingerprint)
		// The agent reports holding something other than what was just signed.
		// Treated as a failure rather than a cosmetic mismatch: whatever it
		// installed is not the certificate this console registered, so the next
		// handshake is the moment it stops working.
		return done(CertRenewalInstallFailed,
			"agent %q reports installing certificate %s but %s was signed for it",
			qube.Name, shortFingerprint(installed.InstalledFingerprint), shortFingerprint(signed.Fingerprint))
	}

	// Confirmed in use: the agent reported installing exactly this certificate.
	// Recording it is what lets the scheduler compute dueness from the new
	// certificate instead of the one it replaced — without it the qube looks
	// perpetually due and is renewed on every sweep.
	if r.seen != nil {
		touchCtx, touchCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if err := r.seen.TouchLastSeen(touchCtx, signed.Fingerprint); err != nil {
			// Not fatal — the certificate is installed and working. But say so:
			// the consequence is a qube that gets renewed again next sweep.
			log.Printf("certrenew: qube %q installed certificate %s but it could not be recorded as in use, "+
				"so it may be renewed again next sweep: %v",
				qube.Name, shortFingerprint(signed.Fingerprint), err)
		}
		touchCancel()
	}

	log.Printf("certrenew: qube %q renewed certificate %s -> %s (expires %s)",
		qube.Name, shortFingerprint(res.OldFingerprint), shortFingerprint(signed.Fingerprint),
		signed.NotAfter.Format(time.RFC3339))
	return done(CertRenewalOK, "")
}

// record writes the freshly signed certificate into the registry, conditionally
// on the credential it replaces still being valid.
//
// The condition is the whole point. Between the handshake that proved which
// agent this is and this write, a purge can commit: RevokeByQube takes the
// qube's access away, and an unconditional INSERT landing a moment later hands
// the decommissioned machine a working credential straight back — in a row that
// looks exactly like a legitimate agent's, so nothing downstream ever flags it.
// repository.RecordRenewal folds the check into the insert so there is no
// window at all, rather than hoping the two statements interleave favourably.
//
// The three outcomes are kept apart because they send an operator to three
// different places: a purge that raced a renewal (nothing is broken), a
// registry that disagrees about who this certificate belongs to (a defect that
// retrying cannot fix), and a database that would not write (a console fault).
func (r *CertRenewer) registerRenewed(
	ctx context.Context, qube *models.Qube, peer *verifiedPeer, registryPrevious string, signed *SignedAgentCert,
) (CertRenewalStatus, string) {
	row := &repository.AgentCert{
		Fingerprint: signed.Fingerprint,
		QubeID:      qube.ID,
		SubjectCN:   signed.SubjectCN,
		IssuedAt:    time.Now().UTC(),
		ExpiresAt:   &signed.NotAfter,
	}

	// The handshake-verified certificate first; the registry's view only as a
	// fallback for a renewer wired without an authorizing registry.
	previous := peer.presented()
	if previous == "" {
		previous = registryPrevious
	}

	if r.recorder == nil || previous == "" {
		// Degrading to an unconditional insert, and saying so. Silence here would
		// be the worst of both worlds: the guard is documented as protecting the
		// purge race, so a reader would assume it ran.
		log.Printf("certrenew: qube %q: recording certificate %s WITHOUT the purge guard (%s); "+
			"if this qube is purged in the next moment it keeps a working credential",
			qube.Name, shortFingerprint(signed.Fingerprint), noGuardReason(r.recorder, previous))
		if err := r.registrar.Register(ctx, row); err != nil {
			return CertRenewalConsoleFailed, fmt.Sprintf(
				"signed a new certificate for %q but could not register it, so it was NOT delivered: %v",
				qube.Name, err)
		}
		return CertRenewalOK, ""
	}

	err := r.recorder.RecordRenewal(ctx, previous, row)
	switch {
	case err == nil:
		return CertRenewalOK, ""

	case errors.Is(err, repository.ErrCertRevoked), errors.Is(err, repository.ErrCertNotRegistered):
		// The credential being renewed was withdrawn while this renewal was in
		// flight. The refusal is the mechanism working, so it must not read as a
		// fault: the certificate was never delivered and the qube keeps whatever
		// access the revocation left it, which is none.
		return CertRenewalWithdrawn, fmt.Sprintf(
			"qube %q had certificate %s withdrawn while it was being renewed, so the new one was "+
				"NOT registered and NOT delivered (%v); this is a purge or a revocation racing the "+
				"renewal sweep, not a fault -- if this qube should still be running, its access was "+
				"taken away by something else",
			qube.Name, shortFingerprint(previous), err)

	case errors.Is(err, repository.ErrRenewalIdentityMismatch):
		// The registry says the certificate just authenticated belongs to someone
		// else. Retrying cannot fix a disagreement about identity, and continuing
		// would register one agent's certificate against another's row.
		return CertRenewalIdentityMismatch, fmt.Sprintf(
			"qube %q authenticated with certificate %s, but the registry does not agree it belongs to "+
				"this qube as %q, so nothing was registered (%v); renewal for this qube will keep "+
				"failing until the rows are reconciled by hand",
			qube.Name, shortFingerprint(previous), signed.SubjectCN, err)

	default:
		return CertRenewalConsoleFailed, fmt.Sprintf(
			"signed a new certificate for %q but could not register it, so it was NOT delivered: %v",
			qube.Name, err)
	}
}

// noGuardReason names which half of the purge guard is missing, so the log line
// says what to fix rather than only that something is absent.
func noGuardReason(recorder CertRenewalRecorder, previous string) string {
	if recorder == nil {
		return "this registry cannot record conditional renewals"
	}
	if previous == "" {
		return "the certificate being replaced could not be identified"
	}
	return "unknown"
}

// currentCert returns the certificate a qube is currently expected to hold: the
// newest registered, unrevoked one. Reporting only — nothing here decides
// whether to renew, which the scheduler does from the same view.
func (r *CertRenewer) currentCert(ctx context.Context, qubeID string) *repository.AgentCert {
	if r.certs == nil {
		return nil
	}
	list, err := r.certs.ListByQube(ctx, qubeID)
	if err != nil {
		log.Printf("certrenew: could not read the certificates issued to qube %s: %v", qubeID, err)
		return nil
	}
	return newestUsableCert(list)
}

// newestUsableCert picks the certificate a qube is actually using: the one with
// the latest expiry among those not revoked.
//
// Latest expiry rather than latest issuance, because a renewal that was signed
// and registered but never installed leaves a NEWER row the agent does not hold.
// Sorting by issuance would then read the fleet as freshly renewed while every
// agent still carries the old certificate — a false green of exactly the kind
// this project keeps rediscovering. Expiry is the conservative view: it can be
// early, never late.
func newestUsableCert(list []*repository.AgentCert) *repository.AgentCert {
	// Prefer certificates the agent has actually been OBSERVED presenting.
	//
	// Registration happens before installation, so the registry can hold a row
	// for a certificate no agent ever received — a renewal whose install failed,
	// or whose reply was lost. That row is by construction the longest-lived one,
	// so ranking purely by expiry makes the scheduler read an orphan as proof the
	// qube was just renewed. It then never renews again, and the qube goes dark
	// weeks later. Reproduced in review: one attempt, then silence.
	//
	// Withdrawing the orphan is NOT a safe alternative on its own: the agent
	// installs before it replies, so a lost reply describes an agent that already
	// holds the certificate, and revoking it there is unrecoverable. So the
	// registry cannot be cleaned up on a guess — instead, dueness is computed
	// from evidence rather than from intent.
	//
	// last_seen_at is that evidence: TouchLastSeen records what the agent
	// presented at a real handshake (internal/transport/grpc/server.go and
	// AgentProber). An unseen row may be a working certificate that simply has
	// not been used yet, so it is a fallback rather than a disqualification —
	// but it never outranks one the agent is known to be using.
	best := pickNewest(list, func(c *repository.AgentCert) bool {
		return c.LastSeenAt != nil
	})
	if best != nil {
		return best
	}
	return pickNewest(list, func(*repository.AgentCert) bool { return true })
}

// pickNewest returns the longest-lived unrevoked certificate matching keep.
func pickNewest(list []*repository.AgentCert, keep func(*repository.AgentCert) bool) *repository.AgentCert {
	var best *repository.AgentCert
	for _, c := range list {
		if c == nil || c.Revoked() || c.ExpiresAt == nil || !keep(c) {
			continue
		}
		if best == nil || c.ExpiresAt.After(*best.ExpiresAt) {
			best = c
		}
	}
	return best
}

// beginRenewalReply is the agent's answer to BeginRenewal.
type beginRenewalReply struct {
	// Nonce ties this CSR to the certificate that will come back. The agent
	// holds the matching private key in memory under it and never writes it
	// until a signed certificate for that nonce arrives.
	Nonce string `json:"nonce"`
	// CSRPEM is the request. The private key does not appear here, or anywhere
	// else on the wire.
	CSRPEM string `json:"csr_pem"`
}

// completeRenewalRequest hands the signed material back to the agent.
type completeRenewalRequest struct {
	Nonce   string `json:"nonce"`
	CertPEM string `json:"cert_pem"`
	// CAPEM travels with it so a CA rotation reaches the agent by the same path.
	// Sending the agent a certificate it cannot chain would leave it unable to
	// verify the console on the next connection.
	CAPEM string `json:"ca_pem"`
}

// completeRenewalReply is the agent's confirmation.
type completeRenewalReply struct {
	// InstalledFingerprint is what the agent says it now holds. It is compared
	// against what was signed: an agent that installed something else has not
	// renewed, whatever it reports.
	InstalledFingerprint string    `json:"installed_fingerprint"`
	NotAfter             time.Time `json:"not_after"`
}

// beginRenewal asks the agent for a fresh CSR.
func beginRenewal(ctx context.Context, sess agentCaller, target string) (*beginRenewalReply, error) {
	out, err := sess.call(ctx, target, beginRenewalService, nil)
	if err != nil {
		return nil, err
	}
	var reply beginRenewalReply
	if err := json.Unmarshal(out, &reply); err != nil {
		return nil, fmt.Errorf("unparseable %s reply: %w", beginRenewalService, err)
	}
	// Both are required. An empty nonce would make CompleteRenewal ambiguous on
	// an agent with more than one renewal in flight, and an empty CSR would have
	// this console sign nothing at all.
	if strings.TrimSpace(reply.Nonce) == "" || strings.TrimSpace(reply.CSRPEM) == "" {
		return nil, fmt.Errorf("%s returned an incomplete reply (nonce=%t csr=%t)",
			beginRenewalService, reply.Nonce != "", reply.CSRPEM != "")
	}
	return &reply, nil
}

// completeRenewal hands the signed certificate back and reads the confirmation.
func completeRenewal(
	ctx context.Context, sess agentCaller, target, nonce string, signed *SignedAgentCert,
) (*completeRenewalReply, error) {
	body, err := json.Marshal(completeRenewalRequest{
		Nonce:   nonce,
		CertPEM: signed.CertPEM,
		CAPEM:   signed.CAPEM,
	})
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", completeRenewalService, err)
	}
	out, err := sess.call(ctx, target, completeRenewalService, body)
	if err != nil {
		return nil, err
	}
	var reply completeRenewalReply
	if err := json.Unmarshal(out, &reply); err != nil {
		return nil, fmt.Errorf("unparseable %s reply: %w", completeRenewalService, err)
	}
	if strings.TrimSpace(reply.InstalledFingerprint) == "" {
		return nil, fmt.Errorf("%s did not report which certificate it installed", completeRenewalService)
	}
	return &reply, nil
}

// verifyRenewalCSR is the console's identity decision about a renewal request.
//
// Three checks, and each one closes something real:
//
//   - The signature must verify against the CSR's own public key. That is proof
//     the requester holds the private half; without it an agent could relay a CSR
//     captured from another qube and have this console sign a certificate for a
//     key it does not own.
//   - The common name must equal the identity the peer already proved by mTLS.
//     Every agent holds a CA-signed certificate, so name is the only thing
//     separating one from another; a request for a different name is an
//     escalation attempt and must fail rather than be corrected.
//   - There must be no subject alternative names at all. Agent certificates are
//     issued with none, and a CSR carrying DNS names or IPs is asking for a
//     certificate that would be accepted by hostname verification somewhere the
//     console never intended — a privilege the requester does not currently have.
func verifyRenewalCSR(csrPEM, wantCN string) error {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return errors.New("the request is not a PEM certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("unparseable certificate request: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return fmt.Errorf("the request is not signed by the key it asks to certify: %w", err)
	}
	if csr.Subject.CommonName != wantCN {
		return fmt.Errorf("requested common name %q, expected %q", csr.Subject.CommonName, wantCN)
	}
	if n := len(csr.DNSNames) + len(csr.IPAddresses) + len(csr.EmailAddresses) + len(csr.URIs); n > 0 {
		return fmt.Errorf("requested %d subject alternative name(s); agent certificates carry none", n)
	}
	return nil
}

// agentSession is one authenticated tunnel to one qube's agent.
//
// Both renewal calls share it. They could be two separate connections — the
// agent keys the pending key by nonce, not by connection — but one tunnel means
// one handshake, and, more importantly, the SAME verified peer answers both
// calls: a second dial could land on a different machine that has since taken
// over the address, and this console would hand a freshly signed certificate to
// whatever answered.
type agentSession struct {
	cli    *transportgrpc.Client
	addr   string
	cancel context.CancelFunc
}

// address is where this session is connected, for messages an operator reads.
func (s *agentSession) address() string { return s.addr }

func (s *agentSession) close() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

// call runs one service over the tunnel, waiting for it to come up first.
func (s *agentSession) call(ctx context.Context, target, service string, in []byte) ([]byte, error) {
	// Call reports ErrNotConnected until the first handshake lands, so without
	// this retry every renewal would race tunnel setup and report a healthy
	// agent as unreachable.
	const retryEvery = 25 * time.Millisecond
	var lastErr error
	for {
		out, err := s.cli.Call(ctx, target, service, in)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !errors.Is(err, transportgrpc.ErrNotConnected) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("tunnel to %s never established: %w", s.addr, lastErr)
		case <-time.After(retryEvery):
		}
	}
}

// dial opens a verified tunnel to one qube's agent.
//
// TCP and TLS are established explicitly first, exactly as AgentProber.connect
// does, so "nothing is listening" and "the handshake was rejected" stay
// distinguishable — a renewal that fails because the current certificate is
// already unusable must not read as a dead VM.
func (r *CertRenewer) dial(ctx context.Context, qubeName, addr string, peer *verifiedPeer) (*agentSession, error) {
	ca, err := r.ca.CA(ctx)
	if err != nil {
		return nil, fmt.Errorf("no usable CA to authenticate to %s: %w", addr, err)
	}
	bundle, err := ca.IssueAgentCert(renewRelayName, renewRelayCertLifetime)
	if err != nil {
		return nil, fmt.Errorf("could not mint a client certificate to reach %s: %w", addr, err)
	}
	// Authorized during the handshake: a revoked certificate must not be able to
	// renew itself into a fresh one. Without this an attacker holding a leaked,
	// revoked-but-unexpired key answers at the qube's address and walks away with
	// a brand-new 90-day certificate for a key of their choosing — while the
	// prober would refuse that same peer in the same second.
	tlsCfg, err := probeTLSConfigWithAuthz(bundle, agentCertCN(qubeName), r.authorizeFingerprint(ctx, peer))
	if err != nil {
		return nil, fmt.Errorf("client certificate for %s is unusable: %w", addr, err)
	}

	cli := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      renewRelayName,
		RemoteName:     qubeName,
		ReconnectMin:   20 * time.Millisecond,
		ReconnectMax:   200 * time.Millisecond,
		// Clone: gRPC's credentials take ownership of the config.
		TLS: tlsCfg.Clone(),
	}, nil)

	// A child context so close() stops the reconnect loop even when the caller's
	// context outlives this renewal. Without it every attempt against a dead host
	// would leak a goroutine retrying forever.
	runCtx, cancel := context.WithCancel(ctx)
	go func() { _ = cli.Start(runCtx) }()

	return &agentSession{cli: cli, addr: addr, cancel: cancel}, nil
}

// parseCertPEM decodes a single PEM certificate.
func parseCertPEM(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// expiryText renders a deadline for an operator, or says it is unknown rather
// than printing year 1 — which reads as corruption instead of as missing data.
func expiryText(t time.Time) string {
	if t.IsZero() {
		return "at an unknown date"
	}
	return t.Format(time.RFC3339)
}

// discardUninstalled revokes a certificate that was registered but never
// installed, so the registry stops describing a credential the agent does not
// have.
//
// This is what makes a failed renewal RETRYABLE. Registration happens before
// installation on purpose — the reverse order locks a qube out of its own fleet
// — but it means a failed install leaves a row whose expiry is 90 days out while
// the agent still holds one expiring in weeks. The scheduler picks the
// longest-lived unrevoked certificate, so it would see that orphan, conclude the
// qube was freshly renewed, and never try again. Reproduced in review: one
// install failure, then a single attempt in the following 21 days, then silence
// until the qube went dark.
//
// The failure is not exotic — a dropped tunnel or a timeout inside the install
// window is enough — so leaving it to a human to notice is not an option.
//
// Revoking is safe precisely because the agent never installed it: nothing is
// using this certificate, and the previous one keeps working untouched.
func (r *CertRenewer) discardUninstalled(
	ctx context.Context, qube *models.Qube, addr, fingerprint string,
) {
	if r.revoker == nil || fingerprint == "" {
		return
	}

	// Revoke ONLY on positive evidence that the agent does not hold this
	// certificate. Anything less must leave it alone.
	//
	// The agent installs BEFORE it replies (internal/agent/renewal.go), so the
	// most common way this function is reached — a lost reply, a dropped tunnel,
	// the 30s exchange deadline — describes an agent that is ALREADY serving the
	// new certificate. Revoking it there is not a cleanup: both the prober and
	// this renewer refuse a revoked peer, and renewal runs over the very channel
	// that just went away. The qube is then unreachable, unrenewable, and only a
	// rebuild recovers it.
	//
	// So the two failure directions are deliberately NOT symmetric. Revoking
	// wrongly bricks a qube permanently. Failing to revoke leaves an orphan row
	// that makes the scheduler think this qube is fresh — bad, and the reason
	// this function exists, but recoverable the moment anyone looks. When we
	// cannot tell them apart, we take the recoverable one.
	held, err := r.certificateHeldBy(ctx, qube.Name, addr)
	if err != nil {
		log.Printf("certrenew: qube %q: NOT revoking certificate %s — could not confirm what the agent holds (%v); "+
			"revoking an installed certificate would lock the qube out permanently",
			qube.Name, shortFingerprint(fingerprint), err)
		return
	}
	if held == fingerprint {
		// The install actually succeeded; only the acknowledgement was lost.
		log.Printf("certrenew: qube %q: certificate %s IS installed despite the failed exchange, keeping it",
			qube.Name, shortFingerprint(fingerprint))
		return
	}
	// Detached from the caller's deadline: the registry must not be left
	// describing a certificate that does not exist just because the renewal
	// context expired a moment ago.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := r.revoker.Revoke(ctx, fingerprint, "signed for renewal but never installed"); err != nil {
		// Loud, because the consequence is silence: an orphan left behind makes
		// this qube look permanently fresh to the scheduler.
		log.Printf("certrenew: qube %q: could NOT revoke uninstalled certificate %s, "+
			"so the scheduler may treat this qube as renewed and stop retrying: %v",
			qube.Name, shortFingerprint(fingerprint), err)
		return
	}
	log.Printf("certrenew: qube %q: discarded uninstalled certificate %s so renewal will be retried",
		qube.Name, shortFingerprint(fingerprint))
}

// authorizeFingerprint returns the handshake-time authorization hook, or nil
// when no registry is available.
//
// It also records which certificate the peer presented. That is deliberate
// coupling: the fingerprint is only trustworthy once it has been authorized,
// and taking it from the same callback means the value handed to the purge
// guard is provably the one that passed the check, not one re-derived later.
func (r *CertRenewer) authorizeFingerprint(ctx context.Context, peer *verifiedPeer) func(string) error {
	if r.authz == nil {
		return nil
	}
	return func(fingerprint string) error {
		if _, err := r.authz.Authorize(ctx, fingerprint); err != nil {
			return fmt.Errorf("agent certificate %s is not authorized to renew: %w",
				shortFingerprint(fingerprint), err)
		}
		peer.note(fingerprint)
		return nil
	}
}

// clockSkewHint appends the explanation an operator will not otherwise reach.
//
// A console and an agent whose clocks differ by more than the relay
// certificate's own validity window cannot complete a TLS handshake at all, and
// the failure arrives as "unreachable" — the same words a powered-off VM
// produces. Somebody then goes looking for a dead machine that is running fine.
// The hint costs one line and points at the actual cause.
func clockSkewHint(err error) string {
	if err == nil {
		return ""
	}
	var invalid x509.CertificateInvalidError
	suspect := errors.As(err, &invalid) &&
		(invalid.Reason == x509.Expired || invalid.Reason == x509.NotAuthorizedToSign)
	if !suspect {
		// The usual shape is the AGENT rejecting the console's certificate, which
		// reaches us as an opaque TLS alert rather than a typed x509 error.
		text := strings.ToLower(err.Error())
		for _, marker := range []string{
			"bad certificate", "certificate expired", "expired certificate",
			"certificate has expired or is not yet valid", "certificate is not valid",
		} {
			if strings.Contains(text, marker) {
				suspect = true
				break
			}
		}
	}
	if !suspect {
		return ""
	}
	return fmt.Sprintf(" -- this is also exactly how a CLOCK SKEW looks: the console's relay certificate "+
		"is valid for %s from now and backdated %s, so an agent whose clock is further out than that "+
		"rejects it and the qube reads as unreachable while being perfectly healthy; check NTP on both "+
		"ends before treating this as a dead VM", renewRelayCertLifetime, caClockSkewBackdate)
}

// certificateHeldBy reports the fingerprint the agent presents RIGHT NOW.
//
// A fresh handshake on purpose. The session used for the renewal recorded the
// certificate from BEFORE the exchange, and the whole question here is whether
// the agent swapped to the new one during it. Reusing that value would answer
// the wrong question and answer it confidently.
func (r *CertRenewer) certificateHeldBy(ctx context.Context, qubeName, addr string) (string, error) {
	// Anything missing here means we cannot ask, and "cannot ask" must never be
	// read as "the agent does not have it" — that is the reading that revokes an
	// installed certificate and bricks the qube.
	if addr == "" {
		return "", errors.New("no address recorded for this qube")
	}
	if r.ca == nil {
		return "", errors.New("no CA available to authenticate a confirmation dial")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	peer := &verifiedPeer{}
	sess, err := r.dial(ctx, qubeName, addr, peer)
	if err != nil {
		return "", err
	}
	defer sess.close()

	// A call is what forces the handshake to complete; dialling alone may not.
	// Ping is the cheapest one the agent always answers.
	if _, err := sess.call(ctx, qubeName, pingService, nil); err != nil {
		if fp := peer.presented(); fp != "" {
			// The handshake DID complete and told us what we came for; the call
			// failing afterwards does not make that observation less true.
			return fp, nil
		}
		return "", err
	}
	fp := peer.presented()
	if fp == "" {
		return "", errors.New("the agent presented no certificate this console could identify")
	}
	return fp, nil
}
