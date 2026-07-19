package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

// AgentProbeStatus is why a probe ended the way it did.
//
// The distinctions are the point. "The console could not reach the agent" is
// useless to whoever has to fix it: a qube with no address yet is waiting on
// terraform, a refused connection means the unit never started, and a rejected
// handshake almost always means a certificate problem on a host that is
// otherwise perfectly healthy. Collapsing those into one failure is what let a
// dead agent look green for hours.
type AgentProbeStatus string

const (
	// AgentProbeOK means the agent answered qubesair.Ping.
	AgentProbeOK AgentProbeStatus = "ok"
	// AgentProbeNoAddress means the qube has no IP address yet, so there was
	// nothing to dial. Not a failure of the agent — usually the qube is still
	// being built, or terraform never learned its address.
	AgentProbeNoAddress AgentProbeStatus = "no_address"
	// AgentProbeUnreachable means the address exists but TCP did not connect:
	// refused, filtered, or timed out. The agent is not listening.
	AgentProbeUnreachable AgentProbeStatus = "unreachable"
	// AgentProbeTLSRejected means TCP connected but the TLS handshake failed.
	// Something IS listening, so this is a trust problem — a wrong CA, an
	// expired or reissued agent certificate — not a down agent. Restarting the
	// unit will not fix it.
	AgentProbeTLSRejected AgentProbeStatus = "tls_rejected"
	// AgentProbeRPCFailed means the mTLS connection stood up but the Ping call
	// did not complete: the tunnel never established, or the agent refused or
	// failed to execute the service.
	AgentProbeRPCFailed AgentProbeStatus = "rpc_failed"
	// AgentProbeNotConfigured means this console cannot probe at all (no CA
	// available, or no prober wired up). The agent's health is UNKNOWN, which is
	// deliberately not the same as unhealthy.
	AgentProbeNotConfigured AgentProbeStatus = "not_configured"
)

// DefaultAgentProbeTimeout bounds one probe end to end.
//
// Sized for a synchronous API call rather than for patience: a probe is a
// health check, and an operator waiting on a page would rather be told "no
// answer in 10s" than hold a connection open. It also bounds the provisioning
// worker, which must never be wedged by a qube that accepts TCP and then goes
// quiet — this project has already been bitten by an unbounded wait.
const DefaultAgentProbeTimeout = 10 * time.Second

// defaultAgentPort is where the agent listens when config says nothing.
// Matches the default in RenderAgentUserData; the two must not drift.
const defaultAgentPort = "8443"

// probeCertLifetime is how long a probe's client certificate is valid.
//
// Minutes, because the certificate exists only for the length of one dial and
// is then dropped. It is never written to disk, never logged and never
// registered, so a short life is free — and it bounds what a heap dump or core
// file taken mid-probe is worth.
const probeCertLifetime = 5 * time.Minute

// probeRelayName is the relay identity the probe presents in the handshake.
// It must satisfy transport.ValidName.
const probeRelayName = "console-probe"

// CAProvider hands out the console's signing CA. Implemented by *CertIssuer.
//
// Narrowed to the one thing a probe needs so the prober cannot reach the rest
// of the issuer — in particular it must not be able to register certificates,
// since a probe certificate has no business in the revocation registry.
type CAProvider interface {
	CA(ctx context.Context) (*pki.CA, error)
}

// CA returns the console CA, creating it on first use.
//
// Defined here rather than in certs.go because the prober is its only caller:
// issuance for agents goes through IssueFor, which also registers, and nothing
// outside this file should be able to take the raw CA and skip that.
func (c *CertIssuer) CA(ctx context.Context) (*pki.CA, error) {
	return c.loadOrCreateCA(ctx)
}

// LastSeenRecorder records that an agent certificate was observed in use.
// Implemented by *repository.AgentCertRepository.
type LastSeenRecorder interface {
	TouchLastSeen(ctx context.Context, fingerprint string) error
}

// CertAuthorizer decides whether a presented certificate is still allowed.
//
// Separate from LastSeenRecorder because they answer different questions and a
// prober may legitimately have one without the other: recording is bookkeeping,
// authorizing is a trust decision.
type CertAuthorizer interface {
	Authorize(ctx context.Context, fingerprint string) (*repository.AgentCert, error)
}

// agentCertCN is the common name certs.go mints for a qube's agent. Kept next
// to the verification that depends on it so the two cannot drift apart.
func agentCertCN(qubeName string) string { return "agent-" + qubeName }

// AgentProbeResult is everything one probe learned about one qube's agent.
//
// Note that this describes the AGENT, not the compute VM. A qube whose VM is
// running fine with a dead agent is genuinely running; it is the agent that is
// unhealthy, and reporting the qube as stopped would lose that distinction.
type AgentProbeResult struct {
	// Authoritative reports whether this result can be attributed to THIS qube.
	//
	// A per-qube probe is authoritative: it dials the qube's own address and
	// holds the peer certificate to that qube's name. The legacy global
	// transport is not — it is pinned to one endpoint that answers for any name
	// asked of it, so a success there says "something answered", never "this
	// qube's agent answered".
	Authoritative bool

	QubeID   string `json:"qube_id"`
	QubeName string `json:"qube_name"`
	// Address is what was dialled, empty when there was nothing to dial.
	Address string `json:"address,omitempty"`
	// Reachable is the single boolean answer: did the agent respond.
	Reachable bool             `json:"reachable"`
	Status    AgentProbeStatus `json:"status"`
	// Pong is the agent's reply, trimmed. The shipped service answers
	// "pong <remote-name> <ts>", which also names the remote that answered. It is NOT the identity control —
	// an attacker holding a valid certificate would simply return the expected
	// string. Identity is established cryptographically by the common-name check
	// in verifyAgentChain; this payload is a configuration cross-check.
	Pong string `json:"pong,omitempty"`
	// Reason explains a non-ok status in terms an operator can act on. Empty on
	// success.
	Reason string `json:"reason,omitempty"`
	// AgentCertFingerprint identifies the certificate the agent presented, so a
	// probe can be tied back to a row in the registry. Set whenever the
	// handshake got far enough to see it.
	AgentCertFingerprint string `json:"agent_cert_fingerprint,omitempty"`
	// Duration is how long the probe took, including certificate minting.
	Duration time.Duration `json:"-"`
	// LatencyMS is Duration in milliseconds, for callers that serialize this.
	LatencyMS int64     `json:"latency_ms"`
	CheckedAt time.Time `json:"checked_at"`
}

// AgentProber answers, for ONE qube: does its agent answer qubesair.Ping?
//
// It dials that qube's own address rather than a shared endpoint. The console's
// single global transport client is pinned to one configured RemoteEndpoint, so
// it cannot express "is THIS qube's agent alive" — which is exactly the
// question that went unanswered while a qube with no agent installed at all
// reported success on every console-side signal.
type AgentProber struct {
	ca    CAProvider
	certs LastSeenRecorder
	// authz refuses revoked, expired and unregistered certificates. Nil disables
	// the check, which is only correct in tests that have no registry at all.
	authz CertAuthorizer
	// port is where every agent listens, taken from the same config value that
	// told the agent what to bind, so the two cannot disagree.
	port    string
	timeout time.Duration
}

// NewAgentProber builds a prober.
//
// agentListen is the config value handed to the agent (e.g. "0.0.0.0:8443");
// only its port is used, since the host to dial is the qube's own address.
// certs may be nil, which disables last-seen bookkeeping but not probing.
func NewAgentProber(ca CAProvider, certs LastSeenRecorder, agentListen string, timeout time.Duration) *AgentProber {
	if timeout <= 0 {
		timeout = DefaultAgentProbeTimeout
	}
	// The registry is ONE object that both records and authorizes, so detect it
	// rather than making every caller pass it twice and risk passing two
	// different things. Production hands in the real AgentCertRepository and
	// therefore gets authorization; a test with no registry gets none, which is
	// the only case where skipping it is correct.
	authz, _ := certs.(CertAuthorizer)

	return &AgentProber{
		ca:      ca,
		certs:   certs,
		authz:   authz,
		port:    agentPortFrom(agentListen),
		timeout: timeout,
	}
}

// Probe checks one qube's agent and returns what it found.
//
// It deliberately returns no error. A probe that says "no answer" has succeeded
// at its job — the failure belongs to the agent, and callers must be able to
// record it without deciding whether to abort. In particular a failed probe
// must NOT fail provisioning: the VM exists and the job did its work. What must
// never happen is the failure being swallowed, so every non-ok outcome is
// logged here as well as returned.
func (p *AgentProber) Probe(ctx context.Context, qube *models.Qube) AgentProbeResult {
	started := time.Now()
	res := AgentProbeResult{
		Authoritative: true, CheckedAt: started.UTC()}
	if qube != nil {
		res.QubeID, res.QubeName = qube.ID, qube.Name
	}

	done := func(status AgentProbeStatus, format string, args ...any) AgentProbeResult {
		res.Status = status
		res.Reachable = status == AgentProbeOK
		res.Duration = time.Since(started)
		res.LatencyMS = res.Duration.Milliseconds()
		if format != "" {
			res.Reason = fmt.Sprintf(format, args...)
		}
		if status != AgentProbeOK {
			log.Printf("agentprobe: qube %q agent NOT healthy (%s): %s", res.QubeName, status, res.Reason)
		}
		return res
	}

	if qube == nil {
		return done(AgentProbeNotConfigured, "no qube given")
	}
	// A nil prober is how a console with no CA configured presents itself.
	// Reporting "unknown" rather than "unreachable" matters: an unprobed agent
	// must not be indistinguishable from one that was probed and failed.
	if p == nil || p.ca == nil {
		return done(AgentProbeNotConfigured, "no agent prober configured; agent health for %q is unknown", qube.Name)
	}

	// No address and nothing-answers are different diagnoses. The first means
	// the qube is not built yet (or terraform never reported an IP); the second
	// means it is built and the agent is broken. Sending an operator to debug
	// the agent when the VM has no address yet wastes the one signal we have.
	host := strings.TrimSpace(qube.IPAddress)
	if host == "" {
		return done(AgentProbeNoAddress,
			"qube %q has no IP address yet, so there is nothing to probe", qube.Name)
	}
	addr := net.JoinHostPort(host, p.port)
	res.Address = addr

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	// Mint a throwaway client certificate. This is what a relay does: the agent
	// trusts the console CA, so a certificate signed by it authenticates us.
	//
	// It is deliberately NOT registered in the agent_certs registry. The agent
	// authorizes on the CA signature alone (it has no database), and registering
	// a row per probe would fill the revocation registry with identities that
	// stopped existing microseconds later.
	ca, err := p.ca.CA(ctx)
	if err != nil {
		return done(AgentProbeNotConfigured, "no usable CA to mint a probe certificate: %v", err)
	}
	bundle, err := ca.IssueAgentCert(probeRelayName, probeCertLifetime)
	if err != nil {
		return done(AgentProbeNotConfigured, "could not mint a probe certificate: %v", err)
	}
	// certs.go mints agent certificates as "agent-<qube name>"; that is the
	// binding this probe holds the peer to.
	tlsCfg, err := probeTLSConfig(bundle, agentCertCN(qube.Name))
	if err != nil {
		return done(AgentProbeNotConfigured, "probe certificate is unusable: %v", err)
	}

	// Connect in two explicit steps so the failure can be named. Letting one
	// dial call do both would leave us guessing from an error string whether
	// nobody was listening or somebody rejected our certificate — the two
	// failures that need the most different responses.
	leaf, status, reason := p.connect(ctx, addr, tlsCfg)
	if leaf != nil {
		res.AgentCertFingerprint = pki.FingerprintOf(leaf)
	}
	if status != AgentProbeOK {
		return done(status, "%s", reason)
	}

	// TCP and TLS both held; now find out whether the agent SERVICE works. This
	// is the part that a "running" status derived from intent can never tell
	// you: the package can be missing, the unit dead, the service script absent,
	// and everything up to this line still succeeds.
	out, err := p.ping(ctx, addr, tlsCfg, qube.Name)
	if err != nil {
		return done(AgentProbeRPCFailed,
			"mTLS to %s succeeded but %s did not answer: %v", addr, pingService, err)
	}
	res.Pong = strings.TrimSpace(string(out))

	// Authorize against the registry, which is this codebase's stated
	// authorization primitive. Chain validity alone would let a REVOKED
	// certificate keep reporting healthy — a purged qube's agent would stay
	// green, and touchLastSeen would refresh its last_seen_at, inverting the
	// one signal that is supposed to reveal it.
	if p.authz != nil && res.AgentCertFingerprint != "" {
		if _, err := p.authz.Authorize(ctx, res.AgentCertFingerprint); err != nil {
			return done(AgentProbeTLSRejected,
				"agent at %s presented certificate %s which the registry refuses: %v",
				addr, res.AgentCertFingerprint[:16], err)
		}
	}

	p.touchLastSeen(ctx, res)
	return done(AgentProbeOK, "")
}

// connect opens TCP and then TLS, returning the agent's leaf certificate.
//
// The split is what makes classification honest: a TCP failure is the agent not
// listening, a handshake failure is a trust problem on a host that is otherwise
// up. Returns AgentProbeOK with a nil reason when both succeeded.
func (p *AgentProber) connect(ctx context.Context, addr string, tlsCfg *tls.Config) (*x509.Certificate, AgentProbeStatus, string) {
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, AgentProbeUnreachable, fmt.Sprintf(
			"nothing is listening on %s: %v (the agent unit is not running, or the port is filtered)", addr, err)
	}

	conn := tls.Client(raw, tlsCfg)
	defer func() { _ = conn.Close() }()

	if err := conn.HandshakeContext(ctx); err != nil {
		// A handshake that ran out of time is not a rejection: nothing said no,
		// the peer just never finished. Calling that a certificate problem would
		// send someone to audit the PKI when the agent is merely wedged.
		if isTimeout(err) {
			return nil, AgentProbeUnreachable, fmt.Sprintf(
				"%s accepted TCP but the TLS handshake never completed within %s: %v", addr, p.timeout, err)
		}
		return nil, AgentProbeTLSRejected, fmt.Sprintf(
			"%s is listening but the mTLS handshake failed: %v "+
				"(something IS running there — check the agent's certificate and CA, not the unit)", addr, err)
	}

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		// TLS 1.3 with a server that sent no certificate should be impossible
		// here, but a nil deref in a health check would be its own outage.
		return nil, AgentProbeTLSRejected, fmt.Sprintf("%s completed a handshake without presenting a certificate", addr)
	}
	return state.PeerCertificates[0], AgentProbeOK, ""
}

// ping runs qubesair.Ping over a tunnel to this one qube.
func (p *AgentProber) ping(ctx context.Context, addr string, tlsCfg *tls.Config, remoteName string) ([]byte, error) {
	cli := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      probeRelayName,
		RemoteName:     remoteName,
		// Tight reconnect bounds: the whole probe lives inside one timeout, so a
		// backoff measured in seconds would spend the budget waiting to retry.
		ReconnectMin: 20 * time.Millisecond,
		ReconnectMax: 200 * time.Millisecond,
		// Clone: gRPC's credentials take ownership of the config, and the same
		// one was already used for the preflight handshake.
		TLS: tlsCfg.Clone(),
	}, nil)

	// Start blocks and owns the tunnel. ctx is already bounded and cancelled on
	// return, so this goroutine cannot outlive the probe that spawned it —
	// otherwise every probe would leak a reconnect loop against a dead host.
	go func() { _ = cli.Start(ctx) }()

	// Call reports ErrNotConnected until Start's first handshake lands. Retrying
	// is not politeness: without it every probe would race tunnel setup and
	// report a healthy agent as dead.
	const retryEvery = 25 * time.Millisecond
	var lastErr error
	for {
		out, err := cli.Call(ctx, remoteName, pingService, nil)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !errors.Is(err, transportgrpc.ErrNotConnected) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("tunnel never established: %w", lastErr)
		case <-time.After(retryEvery):
		}
	}
}

// touchLastSeen records that this agent's certificate was observed working.
//
// Only on a full success, and deliberately so. A handshake alone proves the
// certificate is live, but the registry's last_seen is what tells an operator a
// credential is still in real use — and marking a qube "seen" when its agent
// cannot answer would recreate exactly the false-green signal this probe exists
// to remove. A failed probe therefore touches nothing.
func (p *AgentProber) touchLastSeen(ctx context.Context, res AgentProbeResult) {
	if p.certs == nil || res.AgentCertFingerprint == "" {
		return
	}
	// Detached from the probe's deadline: the answer already arrived, and losing
	// the bookkeeping because the budget expired one millisecond later would be
	// a worse outcome than a slightly late write.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	if err := p.certs.TouchLastSeen(ctx, res.AgentCertFingerprint); err != nil {
		// Operational visibility, never authorization — a probe that answered is
		// still a successful probe. Say it out loud rather than dropping it.
		log.Printf("agentprobe: qube %q answered but last-seen could not be recorded for %s: %v",
			res.QubeName, shortFingerprint(res.AgentCertFingerprint), err)
	}
}

// probeTLSConfig builds the mTLS config a probe dials with.
func probeTLSConfig(bundle *pki.Bundle, wantCN string) (*tls.Config, error) {
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	if err != nil {
		return nil, fmt.Errorf("load probe key pair: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(bundle.CAPEM)) {
		return nil, errors.New("CA certificate could not be parsed")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
		// Hostname verification is replaced, NOT weakened. Two facts about the
		// agent's certificate make the default path reject a perfectly good
		// agent: it carries no SAN for the address we dial (it is issued per
		// qube name, and the address is whatever DHCP handed the VM), and it is
		// issued with ExtKeyUsageClientAuth only — the same certificate the
		// agent presents as a client — so a ServerAuth check fails too.
		//
		// So the chain is verified by hand below, against this CA and this CA
		// only. An unsigned or wrongly-signed certificate is still rejected;
		// what is skipped is the name and the usage, neither of which carries
		// any trust here.
		InsecureSkipVerify: true, //nolint:gosec // chain verified in VerifyPeerCertificate/VerifyConnection
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			certs := make([]*x509.Certificate, 0, len(rawCerts))
			for _, der := range rawCerts {
				c, err := x509.ParseCertificate(der)
				if err != nil {
					return fmt.Errorf("agent certificate is unparseable: %w", err)
				}
				certs = append(certs, c)
			}
			return verifyAgentChain(pool, certs, wantCN)
		},
		// VerifyConnection as well, and not for symmetry: Go does NOT call
		// VerifyPeerCertificate on a RESUMED session, so a config that grew a
		// session cache — gRPC owns the dialling here, not us — would silently
		// stop running the check above and accept any peer at all. This callback
		// runs on every handshake, resumed or not, so the chain is verified even
		// if that happens.
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyAgentChain(pool, cs.PeerCertificates, wantCN)
		},
	}, nil
}

// verifyAgentChain is the whole of the console's trust decision about an agent:
// the certificate must chain to this CA. Name and extended key usage are not
// checked — see probeTLSConfig for why neither can be — so this must reject
// everything else, and is the only thing standing between the console and any
// peer that answers on the qube's address.
func verifyAgentChain(pool *x509.CertPool, certs []*x509.Certificate, wantCN string) error {
	if len(certs) == 0 {
		return errors.New("agent presented no certificate")
	}
	inters := x509.NewCertPool()
	for _, c := range certs[1:] {
		inters.AddCert(c)
	}
	if _, err := certs[0].Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: inters,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return fmt.Errorf("agent certificate is not signed by this console's CA: %w", err)
	}

	// Chain-to-CA answers "is this OUR fleet"; it does NOT answer "is this the
	// qube we dialled". Every qube holds a CA-signed certificate, so without
	// this check any one of them authenticates as any other.
	//
	// That gap is reachable, not theoretical: qubes share an L2 bridge, so a
	// compromised qube can ARP-spoof another's address (or claim it after a DHCP
	// lease churns), answer with its OWN valid certificate, and the console
	// records the victim as healthy. An attacker-triggerable false green is the
	// exact failure this prober exists to eliminate.
	if got := certs[0].Subject.CommonName; got != wantCN {
		return fmt.Errorf(
			"agent certificate identifies %q but this address should be serving %q; "+
				"a valid fleet certificate presented by the wrong qube", got, wantCN)
	}
	return nil
}

// agentPortFrom extracts the port the agent listens on from a listen address.
//
// Falls back rather than failing: a probe against the default port is a far
// better outcome than a console that cannot probe at all because a config
// string was malformed.
func agentPortFrom(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return defaultAgentPort
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		log.Printf("agentprobe: agent listen address %q has no usable port; probing %s instead",
			listen, defaultAgentPort)
		return defaultAgentPort
	}
	return port
}

// isTimeout reports whether err is a deadline rather than a refusal.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var nerr net.Error
	return errors.As(err, &nerr) && nerr.Timeout()
}

// shortFingerprint abbreviates a fingerprint for logs. The full value is a
// database key, not a secret, but 64 hex characters per line buries everything
// around it.
func shortFingerprint(fp string) string {
	if len(fp) <= 16 {
		return fp
	}
	return fp[:16]
}
