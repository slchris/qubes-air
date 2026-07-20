// agentbootstrap.go — the console's half of first-certificate issuance.
//
// The console DIALS the agent, the same direction probing and renewal already
// use (docs/bootstrap-design.md §9.3). Two calls over one tunnel:
//
//	console → qubesair.BeginBootstrap     → agent returns {nonce, token, csr_pem}
//	console   (redeems the token, signs the CSR, registers the fingerprint)
//	console → qubesair.CompleteBootstrap  → agent installs and confirms
//
// The trust in each direction is established differently, and neither relies
// on the transport:
//
//   - The AGENT authenticates this console by the client certificate offered
//     here, which chains to the CA cloud-init delivered. That check happens on
//     the agent, in its listener, before it surrenders the token.
//   - This console authenticates the AGENT by the token. It cannot do so by
//     certificate — the whole point is that the agent has none yet — so the
//     server certificate is deliberately NOT verified, and the token, redeemed
//     against a store that guarantees exactly one winner, is what proves the
//     answering host is the one we provisioned.
//
// A man in the middle is closed off from both sides: impersonating the agent
// yields no token, and impersonating the console requires a CA-signed client
// certificate. Relaying is not available either, since a client certificate
// signs the handshake transcript it appears in.
//
// Ordering mirrors renewal exactly — sign, REGISTER, then deliver — because
// the failure modes are the same. See BootstrapIssuer for why redemption comes
// before signing.

package service

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/agent"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

// Bootstrap service names, as the agent registers them.
const (
	beginBootstrapService    = "qubesair.BeginBootstrap"
	completeBootstrapService = "qubesair.CompleteBootstrap"
)

// bootstrapRelayName is the identity the console presents while bootstrapping.
// Distinct from renewRelayName so an agent's logs say which conversation it
// was part of, and so a certificate minted for one is not silently reused for
// the other.
const bootstrapRelayName = "console-bootstrap"

// DefaultBootstrapTimeout bounds one qube's whole bootstrap exchange.
//
// Longer than renewal's: a freshly booted guest may still be finishing
// cloud-init, and the tunnel retry inside agentSession.call needs room to
// catch a listener that came up a second after we dialed.
const DefaultBootstrapTimeout = 60 * time.Second

// BootstrapStatus is why a bootstrap ended the way it did.
//
// Split along the same seam as CertRenewalStatus: everything from
// BootstrapUnreachable is "go look at the VM", everything after the agent
// answered is "the console side broke", and conflating them sends an operator
// to SSH into a healthy qube.
type BootstrapStatus string

// Bootstrap statuses.
const (
	// BootstrapOK means the agent installed its first certificate.
	BootstrapOK BootstrapStatus = "ok"
	// BootstrapUnreachable means the agent never answered: no address, nothing
	// listening, a rejected handshake, or no reply.
	BootstrapUnreachable BootstrapStatus = "unreachable"
	// BootstrapAlreadyDone means the agent reports it already holds an
	// identity. Not a failure: it is what a retry against a qube that
	// bootstrapped on the previous sweep looks like.
	BootstrapAlreadyDone BootstrapStatus = "already_bootstrapped"
	// BootstrapRefused means the token was not accepted — unknown, expired, or
	// already spent. The qube needs a fresh token, which means re-provisioning
	// its user-data; no amount of retrying will help.
	BootstrapRefused BootstrapStatus = "refused"
	// BootstrapConsoleFailed means the agent did its part and this console
	// could not do its own: the CA would not sign, or the registry would not
	// record the result.
	BootstrapConsoleFailed BootstrapStatus = "console_failed"
	// BootstrapInstallFailed means the certificate was signed and registered
	// but the agent did not install it. The token is spent, so recovery is a
	// new token — not a retry.
	BootstrapInstallFailed BootstrapStatus = "install_failed"
	// BootstrapNotConfigured means this console cannot bootstrap at all.
	BootstrapNotConfigured BootstrapStatus = "not_configured"
)

// AgentAnswered reports whether the agent proved it was alive. Same purpose as
// CertRenewalStatus.AgentAnswered: it decides whether a failure may be recorded
// as "unreachable" and send someone after the machine.
func (s BootstrapStatus) AgentAnswered() bool {
	switch s {
	case BootstrapOK, BootstrapAlreadyDone, BootstrapRefused,
		BootstrapConsoleFailed, BootstrapInstallFailed:
		return true
	case BootstrapUnreachable, BootstrapNotConfigured:
		return false
	default:
		return false
	}
}

// BootstrapResult is everything one bootstrap attempt learned.
type BootstrapResult struct {
	QubeID      string
	QubeName    string
	Status      BootstrapStatus
	Reason      string
	Fingerprint string
	NotAfter    time.Time
	At          time.Time
	Duration    time.Duration
}

// FirstCertificateIssuer redeems a token and signs the CSR it authorizes.
// Implemented by *BootstrapIssuer.
type FirstCertificateIssuer interface {
	IssueFirstCertificate(ctx context.Context, token, csrPEM string) (*IssuedBootstrapCert, error)
}

// AgentBootstrapper runs bootstrap against qubes that have no identity yet.
type AgentBootstrapper struct {
	ca      CAProvider
	issuer  FirstCertificateIssuer
	port    string
	timeout time.Duration
}

// NewAgentBootstrapper builds the bootstrapper.
func NewAgentBootstrapper(ca CAProvider, issuer FirstCertificateIssuer, agentListen string, timeout time.Duration) *AgentBootstrapper {
	if timeout <= 0 {
		timeout = DefaultBootstrapTimeout
	}
	return &AgentBootstrapper{
		ca:      ca,
		issuer:  issuer,
		port:    agentPortFrom(agentListen),
		timeout: timeout,
	}
}

// beginBootstrapReply is what qubesair.BeginBootstrap returns.
type beginBootstrapReply struct {
	Nonce  string `json:"nonce"`
	Token  string `json:"token"`
	CSRPEM string `json:"csr_pem"`
}

// completeBootstrapRequest is what qubesair.CompleteBootstrap takes.
type completeBootstrapRequest struct {
	Nonce   string `json:"nonce"`
	CertPEM string `json:"cert_pem"`
	CAPEM   string `json:"ca_pem"`
}

// completeBootstrapReply is what qubesair.CompleteBootstrap returns.
type completeBootstrapReply struct {
	InstalledFingerprint string    `json:"installed_fingerprint"`
	NotAfter             time.Time `json:"not_after"`
}

// Bootstrap runs the two-call exchange against one qube.
//
// Like Probe and Renew, it returns no error: "the agent did not bootstrap" is
// a result every caller records rather than aborts on.
func (b *AgentBootstrapper) Bootstrap(ctx context.Context, qube *models.Qube) BootstrapResult {
	started := time.Now()
	res := BootstrapResult{At: started.UTC()}
	if qube != nil {
		res.QubeID, res.QubeName = qube.ID, qube.Name
	}

	done := func(status BootstrapStatus, format string, args ...any) BootstrapResult {
		res.Status = status
		res.Duration = time.Since(started)
		if format != "" {
			res.Reason = fmt.Sprintf(format, args...)
		}
		if status != BootstrapOK && status != BootstrapAlreadyDone {
			log.Printf("bootstrap: qube %q did not bootstrap (%s): %s", res.QubeName, status, res.Reason)
		}
		return res
	}

	if qube == nil {
		return done(BootstrapNotConfigured, "no qube given")
	}
	if b == nil || b.ca == nil || b.issuer == nil {
		return done(BootstrapNotConfigured,
			"no bootstrapper configured; qube %q cannot be issued a first certificate", qube.Name)
	}
	host := strings.TrimSpace(qube.IPAddress)
	if host == "" {
		return done(BootstrapUnreachable,
			"qube %q has no IP address, so its agent cannot be reached to bootstrap", qube.Name)
	}
	addr := net.JoinHostPort(host, b.port)

	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	sess, err := b.dial(ctx, qube.Name, addr)
	if err != nil {
		return done(BootstrapUnreachable, "%v", err)
	}
	defer sess.close()

	return b.exchange(ctx, qube, sess, done)
}

// exchange runs the protocol once the tunnel is up.
func (b *AgentBootstrapper) exchange(
	ctx context.Context, qube *models.Qube, sess agentCaller,
	done func(BootstrapStatus, string, ...any) BootstrapResult,
) BootstrapResult {
	out, err := sess.call(ctx, qube.Name, beginBootstrapService, nil)
	if err != nil {
		// An agent that already holds an identity says so rather than handing
		// over a token. That is the expected answer on any sweep after the
		// first, so it must not be reported as a fault.
		//
		// Matched by string because the transport carries a message, not a
		// wrapped error — but against the agent's own sentinel rather than a
		// literal, so a reworded message cannot leave this branch silently
		// unreachable while every already-bootstrapped qube starts reporting
		// as unreachable.
		if strings.Contains(err.Error(), agent.ErrAlreadyBootstrapped.Error()) {
			return done(BootstrapAlreadyDone,
				"qube %q already holds an identity; nothing to bootstrap", qube.Name)
		}
		return done(BootstrapUnreachable, "%s at %s: %v%s",
			beginBootstrapService, sess.address(), err, clockSkewHint(err))
	}

	var begun beginBootstrapReply
	if err := json.Unmarshal(out, &begun); err != nil {
		return done(BootstrapConsoleFailed, "unparseable %s reply from %s: %v",
			beginBootstrapService, sess.address(), err)
	}
	// All three are required. A missing nonce makes the second call ambiguous,
	// a missing token means there is nothing to authenticate this agent with,
	// and a missing CSR means there is nothing to sign.
	if strings.TrimSpace(begun.Nonce) == "" || strings.TrimSpace(begun.Token) == "" ||
		strings.TrimSpace(begun.CSRPEM) == "" {
		return done(BootstrapConsoleFailed,
			"%s returned an incomplete reply (nonce=%t token=%t csr=%t)",
			beginBootstrapService, begun.Nonce != "", begun.Token != "", begun.CSRPEM != "")
	}

	// Redeem, sign and register. The CN is taken from the redeemed token, never
	// from the CSR, so an agent asking for another qube's name is refused here
	// rather than issued — see BootstrapIssuer.
	issued, err := b.issuer.IssueFirstCertificate(ctx, begun.Token, begun.CSRPEM)
	if err != nil {
		if errors.Is(err, repository.ErrBootstrapTokenRejected) {
			return done(BootstrapRefused,
				"the token qube %q presented was not accepted; it needs re-provisioning to get a fresh one", qube.Name)
		}
		return done(BootstrapConsoleFailed, "issuing a first certificate for %q: %v", qube.Name, err)
	}

	// A certificate signed for a name other than the qube we dialed means the
	// token in that guest belongs to a different qube — a provisioning mixup,
	// and installing it would give this host an identity the prober will refuse
	// at the very next sweep.
	if want := AgentCommonName(qube.Name); issued.SubjectCN != want {
		return done(BootstrapConsoleFailed,
			"the token presented at %s authorizes %q, but this address should be serving %q; "+
				"two qubes appear to have been provisioned with each other's user-data",
			sess.address(), issued.SubjectCN, want)
	}

	body, err := json.Marshal(completeBootstrapRequest{
		Nonce:   begun.Nonce,
		CertPEM: issued.CertPEM,
		CAPEM:   issued.CAPEM,
	})
	if err != nil {
		return done(BootstrapConsoleFailed, "encode %s request: %v", completeBootstrapService, err)
	}

	out, err = sess.call(ctx, qube.Name, completeBootstrapService, body)
	if err != nil {
		// The token is spent and the certificate is registered, but the agent
		// does not have it. Recovery is a NEW token, not a retry: the agent
		// discarded its pending key with the nonce, so nothing on either side
		// can still complete this exchange.
		return done(BootstrapInstallFailed,
			"certificate %s was signed and registered but %s failed: %v; "+
				"qube %q needs a fresh token to try again",
			shortFingerprint(issued.Fingerprint), completeBootstrapService, err, qube.Name)
	}

	var installed completeBootstrapReply
	if err := json.Unmarshal(out, &installed); err != nil {
		return done(BootstrapInstallFailed, "unparseable %s reply: %v", completeBootstrapService, err)
	}
	// Confirm the agent installed the certificate we registered, rather than
	// inferring it from a call that returned without error. A mismatch means
	// the registry authorizes a fingerprint the agent will never present.
	if installed.InstalledFingerprint != issued.Fingerprint {
		return done(BootstrapInstallFailed,
			"registered certificate %s but qube %q reports installing %s",
			shortFingerprint(issued.Fingerprint), qube.Name, shortFingerprint(installed.InstalledFingerprint))
	}

	res := done(BootstrapOK, "")
	res.Fingerprint = issued.Fingerprint
	res.NotAfter = issued.NotAfter
	log.Printf("bootstrap: qube %q installed its first certificate %s, valid until %s",
		qube.Name, shortFingerprint(issued.Fingerprint), issued.NotAfter.UTC().Format(time.RFC3339))
	return res
}

// dial opens a tunnel to an agent that has no certificate yet.
//
// The server certificate is deliberately NOT verified. A bootstrapping agent
// presents a self-signed placeholder — it has nothing else, which is the
// condition being repaired — so there is no chain to check and no name to
// match. What makes this safe is that the agent verifies US: it will not
// surrender its token to a peer whose client certificate does not chain to the
// CA cloud-init delivered, so an impostor listening at this address learns
// nothing and receives nothing. The token it would have to produce is what
// authenticates the agent in return.
//
// This is the ONLY dial in the console that skips peer verification, and it is
// confined to hosts that have no identity to verify. Once bootstrap succeeds,
// every later conversation with this qube runs through the prober's and
// renewer's fully verified paths.
func (b *AgentBootstrapper) dial(ctx context.Context, qubeName, addr string) (*agentSession, error) {
	ca, err := b.ca.CA(ctx)
	if err != nil {
		return nil, fmt.Errorf("no usable CA to authenticate to %s: %w", addr, err)
	}
	bundle, err := ca.IssueAgentCert(bootstrapRelayName, renewRelayCertLifetime)
	if err != nil {
		return nil, fmt.Errorf("could not mint a client certificate to reach %s: %w", addr, err)
	}
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	if err != nil {
		return nil, fmt.Errorf("client certificate for %s is unusable: %w", addr, err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS13,
		// See the doc comment: there is no issued certificate to verify yet.
		// The token, not the transport, authenticates the agent.
		InsecureSkipVerify: true, //nolint:gosec // bootstrap peers hold no certificate; the one-shot token authenticates them
	}

	cli := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      bootstrapRelayName,
		RemoteName:     qubeName,
		ReconnectMin:   20 * time.Millisecond,
		ReconnectMax:   200 * time.Millisecond,
		TLS:            tlsCfg,
	}, nil)

	runCtx, cancel := context.WithCancel(ctx)
	go func() { _ = cli.Start(runCtx) }()

	return &agentSession{cli: cli, addr: addr, cancel: cancel}, nil
}
