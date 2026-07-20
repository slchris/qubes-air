package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles -----------------------------------------------------------

// staticCA hands out one CA, standing in for *CertIssuer.
type staticCA struct {
	ca  *pki.CA
	err error
}

func (s staticCA) CA(context.Context) (*pki.CA, error) { return s.ca, s.err }

// recordingCerts captures TouchLastSeen calls so a test can assert on whether
// the registry was told anything at all.
type recordingCerts struct {
	mu   sync.Mutex
	seen []string
	err  error
}

func (r *recordingCerts) TouchLastSeen(_ context.Context, fp string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, fp)
	return r.err
}

func (r *recordingCerts) touched() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// fakeInvoker stands in for the agent's local qrexec executor.
type fakeInvoker struct {
	resp []byte
	err  error
}

func (f *fakeInvoker) Invoke(_ context.Context, target, service string, _ []byte) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append(append([]byte(nil), f.resp...), []byte(" "+target+" "+service)...), nil
}

// --- helpers ----------------------------------------------------------------

// startAgent runs a real mTLS gRPC agent on localhost, presenting a certificate
// issued by serverCA, and returns its address plus that certificate.
//
// The agent's certificate is minted with IssueAgentCert exactly as a real one
// is — ExtKeyUsageClientAuth only, no SAN for the dial address. That is the
// whole reason the prober verifies the chain by hand, so a test that used a
// conventional server certificate would prove nothing about production.
// startAgent runs an in-process mTLS agent. serverCN is the identity its
// certificate claims — an explicit parameter because the prober now binds the
// certificate to the qube it dialed, so which name the agent presents is part
// of what each test is asserting, not an incidental detail.
func startAgent(t *testing.T, serverCA *pki.CA, clientCA *pki.CA, serverCN string, inv transportgrpc.QrexecInvoker) (addr string, agentFingerprint string) {
	t.Helper()

	bundle, err := serverCA.IssueAgentCert(serverCN, time.Hour)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	require.NoError(t, err)

	clientPool := x509.NewCertPool()
	clientCAPEM, _, err := clientCA.MarshalCA()
	require.NoError(t, err)
	require.True(t, clientPool.AppendCertsFromPEM([]byte(clientCAPEM)))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr = lis.Addr().String()
	require.NoError(t, lis.Close()) // Serve re-listens on the same address

	srv := transportgrpc.NewServer(transportgrpc.ServerConfig{
		Listen: addr,
		TLS: &tls.Config{
			Certificates: []tls.Certificate{pair},
			ClientCAs:    clientPool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS13,
		},
	}, inv)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(cancel)

	waitForListener(t, addr)
	return addr, bundle.Fingerprint
}

// waitForListener blocks until something accepts on addr.
func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing ever listened on %s", addr)
}

// hostPort splits an address a test just built.
func hostPort(t *testing.T, addr string) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	return host, port
}

// newCA mints a fresh CA for a test.
func newCA(t *testing.T) *pki.CA {
	t.Helper()
	ca, err := pki.NewCA("test-console", time.Hour)
	require.NoError(t, err)
	return ca
}

// closedPort returns an address on localhost with nothing listening.
func closedPort(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	return addr
}

// --- tests ------------------------------------------------------------------

// TestProbe_AgentAnswers drives the whole path against a real mTLS agent: mint
// a client certificate, dial, handshake, tunnel, Ping. This is the signal that
// did not exist when a qube with no agent installed reported healthy.
func TestProbe_AgentAnswers(t *testing.T) {
	ca := newCA(t)
	inv := &fakeInvoker{resp: []byte("pong")}
	addr, agentFP := startAgent(t, ca, ca, "agent-probe-qube", inv)
	host, port := hostPort(t, addr)

	certs := &recordingCerts{}
	p := NewAgentProber(staticCA{ca: ca}, certs, "0.0.0.0:"+port, 10*time.Second)

	res := p.Probe(context.Background(), &models.Qube{
		ID: "q1", Name: "probe-qube", IPAddress: host,
	})

	require.True(t, res.Reachable, "reason: %s", res.Reason)
	assert.Equal(t, AgentProbeOK, res.Status)
	assert.Empty(t, res.Reason)
	assert.Contains(t, res.Pong, "pong")
	// The reply must come back from the service the prober actually asked for.
	assert.Contains(t, res.Pong, pingService)
	assert.Contains(t, res.Pong, "probe-qube")
	assert.Equal(t, addr, res.Address)
	assert.Positive(t, res.Duration)

	// The fingerprint must identify the AGENT's certificate — the one in the
	// registry — not the throwaway client certificate the probe minted.
	assert.Equal(t, agentFP, res.AgentCertFingerprint)
	assert.Equal(t, []string{agentFP}, certs.touched())
}

// TestProbe_NoAddressIsItsOwnDiagnosis — a qube with no IP must not look like a
// qube whose agent is dead. One is waiting on terraform, the other is broken.
func TestProbe_NoAddressIsItsOwnDiagnosis(t *testing.T) {
	ca := newCA(t)
	certs := &recordingCerts{}
	p := NewAgentProber(staticCA{ca: ca}, certs, "0.0.0.0:8443", time.Second)

	res := p.Probe(context.Background(), &models.Qube{ID: "q1", Name: "no-ip", IPAddress: "  "})

	assert.False(t, res.Reachable)
	assert.Equal(t, AgentProbeNoAddress, res.Status)
	assert.NotEqual(t, AgentProbeUnreachable, res.Status)
	assert.Empty(t, res.Address, "nothing was dialed, so nothing should be reported as dialed")
	assert.NotEmpty(t, res.Reason)
	assert.Empty(t, certs.touched(), "a probe that never ran must not touch the registry")
}

// TestProbe_NothingListening — the address exists but the agent unit is not
// running. Distinct from a certificate problem, which needs a different fix.
func TestProbe_NothingListening(t *testing.T) {
	ca := newCA(t)
	addr := closedPort(t)
	host, port := hostPort(t, addr)

	certs := &recordingCerts{}
	p := NewAgentProber(staticCA{ca: ca}, certs, "0.0.0.0:"+port, 3*time.Second)

	res := p.Probe(context.Background(), &models.Qube{ID: "q1", Name: "dead-agent", IPAddress: host})

	assert.False(t, res.Reachable)
	assert.Equal(t, AgentProbeUnreachable, res.Status)
	assert.Equal(t, addr, res.Address)
	assert.NotEmpty(t, res.Reason)
	assert.Empty(t, certs.touched())
}

// TestProbe_WrongCAIsATrustProblem — an agent presenting a certificate from a
// different CA must be reported as a trust failure, not as a down agent.
// Restarting the unit would not fix this, and saying "unreachable" would send
// an operator to do exactly that.
func TestProbe_WrongCAIsATrustProblem(t *testing.T) {
	consoleCA := newCA(t)
	strangerCA := newCA(t)

	// The agent serves a certificate from the stranger CA but still trusts the
	// console CA for clients, so the failure is unambiguously about the SERVER's
	// certificate rather than about ours being refused.
	addr, _ := startAgent(t, strangerCA, consoleCA, "agent-probe-qube", &fakeInvoker{resp: []byte("pong")})
	host, port := hostPort(t, addr)

	certs := &recordingCerts{}
	p := NewAgentProber(staticCA{ca: consoleCA}, certs, "0.0.0.0:"+port, 5*time.Second)

	res := p.Probe(context.Background(), &models.Qube{ID: "q1", Name: "bad-cert", IPAddress: host})

	assert.False(t, res.Reachable)
	assert.Equal(t, AgentProbeTLSRejected, res.Status)
	assert.NotEmpty(t, res.Reason)
	assert.Empty(t, certs.touched(), "a rejected handshake proves nothing about liveness")
}

// TestProbe_RejectsUnsignedPeer is the other half of the trust check: the
// hand-rolled verification must actually reject, not merely log. A plain TLS
// server with a self-signed certificate stands in for an impostor on the qube's
// address.
func TestProbe_RejectsUnsignedPeer(t *testing.T) {
	ca := newCA(t)
	impostor := newCA(t)
	bundle, err := impostor.IssueAgentCert("impostor", time.Hour)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	require.NoError(t, err)

	lis, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS13,
	})
	require.NoError(t, err)
	defer func() { _ = lis.Close() }()
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake() }()
		}
	}()

	host, port := hostPort(t, lis.Addr().String())
	certs := &recordingCerts{}
	p := NewAgentProber(staticCA{ca: ca}, certs, "0.0.0.0:"+port, 5*time.Second)

	res := p.Probe(context.Background(), &models.Qube{ID: "q1", Name: "impostor", IPAddress: host})

	assert.False(t, res.Reachable, "a certificate from an unknown CA must never be accepted")
	assert.Equal(t, AgentProbeTLSRejected, res.Status)
	assert.Empty(t, certs.touched())
}

// TestProbe_AgentUpButServiceFails is the case the whole change exists for: the
// VM is running, the unit is up, mTLS works — and the service still does not
// answer. Every signal short of this one reports success.
func TestProbe_AgentUpButServiceFails(t *testing.T) {
	ca := newCA(t)
	inv := &fakeInvoker{err: errors.New("no such qrexec service")}
	addr, agentFP := startAgent(t, ca, ca, "agent-broken-svc", inv)
	host, port := hostPort(t, addr)

	certs := &recordingCerts{}
	p := NewAgentProber(staticCA{ca: ca}, certs, "0.0.0.0:"+port, 5*time.Second)

	res := p.Probe(context.Background(), &models.Qube{ID: "q1", Name: "broken-svc", IPAddress: host})

	assert.False(t, res.Reachable)
	assert.Equal(t, AgentProbeRPCFailed, res.Status)
	assert.NotEmpty(t, res.Reason)
	// The handshake succeeded, so the agent's certificate was seen even though
	// the probe failed — but seeing it is not the same as it working.
	assert.Equal(t, agentFP, res.AgentCertFingerprint)
	assert.Empty(t, certs.touched(),
		"an agent that cannot answer must not be recorded as seen; that is the false-green this probe removes")
}

// TestProbe_SilentPeerHitsTheTimeout — a host that accepts TCP and then says
// nothing must not wedge the caller. An unbounded wait here would hang the
// provisioning worker on a single sick qube.
func TestProbe_SilentPeerHitsTheTimeout(t *testing.T) {
	ca := newCA(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = lis.Close() }()

	held := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				close(held)
				return
			}
			held <- c // accepted, never spoken to
		}
	}()

	host, port := hostPort(t, lis.Addr().String())
	timeout := 400 * time.Millisecond
	p := NewAgentProber(staticCA{ca: ca}, &recordingCerts{}, "0.0.0.0:"+port, timeout)

	start := time.Now()
	res := p.Probe(context.Background(), &models.Qube{ID: "q1", Name: "silent", IPAddress: host})
	elapsed := time.Since(start)

	assert.False(t, res.Reachable)
	// A hung handshake is not a rejection: nothing said no.
	assert.Equal(t, AgentProbeUnreachable, res.Status)
	assert.Less(t, elapsed, 5*time.Second, "the probe must return on its own deadline, not hang")
	assert.GreaterOrEqual(t, elapsed, timeout/2)
}

// TestProbe_CallerDeadlineIsHonoured — a caller's context must be able to cut a
// probe short even when the prober's own timeout is generous.
func TestProbe_CallerDeadlineIsHonoured(t *testing.T) {
	ca := newCA(t)
	addr := closedPort(t)
	host, port := hostPort(t, addr)
	p := NewAgentProber(staticCA{ca: ca}, nil, "0.0.0.0:"+port, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	res := p.Probe(ctx, &models.Qube{ID: "q1", Name: "canceled", IPAddress: host})

	assert.False(t, res.Reachable)
	assert.Less(t, time.Since(start), 10*time.Second)
}

// TestProbe_NoCAIsUnknownNotUnhealthy — a console that cannot mint a client
// certificate has learned nothing about the agent, and must not claim it did.
func TestProbe_NoCAIsUnknownNotUnhealthy(t *testing.T) {
	certs := &recordingCerts{}
	p := NewAgentProber(staticCA{err: errors.New("credential store is empty")}, certs, "0.0.0.0:8443", time.Second)

	res := p.Probe(context.Background(), &models.Qube{ID: "q1", Name: "no-ca", IPAddress: "10.0.0.9"})

	assert.False(t, res.Reachable)
	assert.Equal(t, AgentProbeNotConfigured, res.Status)
	assert.Contains(t, res.Reason, "credential store is empty",
		"the real error must survive; a generic message here is what hid the original bug")
	assert.Empty(t, certs.touched())
}

// TestProbe_NilProberReportsUnknown — a console wired without a prober must
// still answer, and must say "unknown" rather than "unreachable".
func TestProbe_NilProberReportsUnknown(t *testing.T) {
	var p *AgentProber
	res := p.Probe(context.Background(), &models.Qube{ID: "q1", Name: "unwired", IPAddress: "10.0.0.9"})

	assert.False(t, res.Reachable)
	assert.Equal(t, AgentProbeNotConfigured, res.Status)
	assert.NotEmpty(t, res.Reason)
}

// TestProbe_TouchFailureDoesNotDemoteASuccess — bookkeeping is not the answer.
// An agent that replied is healthy even if the registry write fails.
func TestProbe_TouchFailureDoesNotDemoteASuccess(t *testing.T) {
	ca := newCA(t)
	addr, _ := startAgent(t, ca, ca, "agent-probe-qube", &fakeInvoker{resp: []byte("pong")})
	host, port := hostPort(t, addr)

	certs := &recordingCerts{err: errors.New("database is locked")}
	p := NewAgentProber(staticCA{ca: ca}, certs, "0.0.0.0:"+port, 10*time.Second)

	res := p.Probe(context.Background(), &models.Qube{ID: "q1", Name: "probe-qube", IPAddress: host})

	assert.True(t, res.Reachable, "reason: %s", res.Reason)
	assert.Equal(t, AgentProbeOK, res.Status)
}

// TestProbe_ProbeCertIsNotRegistered — the throwaway client certificate must
// never reach the revocation registry. One row per probe would bury the agent
// identities that revocation actually cares about.
func TestProbe_ProbeCertIsNotRegistered(t *testing.T) {
	ca := newCA(t)
	addr, agentFP := startAgent(t, ca, ca, "agent-probe-qube", &fakeInvoker{resp: []byte("pong")})
	host, port := hostPort(t, addr)

	certs := &recordingCerts{}
	p := NewAgentProber(staticCA{ca: ca}, certs, "0.0.0.0:"+port, 10*time.Second)

	require.True(t, p.Probe(context.Background(),
		&models.Qube{ID: "q1", Name: "probe-qube", IPAddress: host}).Reachable)

	touched := certs.touched()
	require.Len(t, touched, 1)
	assert.Equal(t, agentFP, touched[0], "only the agent's own certificate may be recorded")
}

// TestAgentPortFrom covers the port the probe dials, including the malformed
// config that must degrade to the default rather than disabling probing.
func TestAgentPortFrom(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:8443":   "8443",
		"127.0.0.1:9000": "9000",
		"[::]:8443":      "8443",
		"":               defaultAgentPort,
		"   ":            defaultAgentPort,
		"not-an-address": defaultAgentPort,
		"host:":          defaultAgentPort,
	}
	for in, want := range cases {
		assert.Equal(t, want, agentPortFrom(in), "input %q", in)
	}
}

// TestProbeTLSConfig_VerifiesRatherThanSkips guards the security property
// directly: InsecureSkipVerify is set, so the hand-rolled callback is the ONLY
// thing standing between the console and any peer at all. It must reject.
func TestProbeTLSConfig_VerifiesRatherThanSkips(t *testing.T) {
	ca := newCA(t)
	bundle, err := ca.IssueAgentCert("probe", time.Minute)
	require.NoError(t, err)
	cfg, err := probeTLSConfig(bundle, "agent-probe-qube")
	require.NoError(t, err)

	require.NotNil(t, cfg.VerifyPeerCertificate, "verification must not be skipped outright")
	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)

	// Signed by this CA: accepted, despite carrying no SAN and only ClientAuth.
	ours, err := ca.IssueAgentCert("agent-probe-qube", time.Minute)
	require.NoError(t, err)
	assert.NoError(t, cfg.VerifyPeerCertificate(derOf(t, ours.CertPEM), nil))

	// Signed by another CA: rejected.
	other, err := newCA(t).IssueAgentCert("agent-x", time.Minute)
	require.NoError(t, err)
	assert.Error(t, cfg.VerifyPeerCertificate(derOf(t, other.CertPEM), nil))

	// Nothing presented at all: rejected.
	assert.Error(t, cfg.VerifyPeerCertificate(nil, nil))
	// Garbage: rejected, not panicked.
	assert.Error(t, cfg.VerifyPeerCertificate([][]byte{[]byte("not a certificate")}, nil))
}

// TestProbeTLSConfig_ResumedSessionsAreVerifiedToo closes a bypass that is easy
// to miss: Go skips VerifyPeerCertificate entirely on a resumed session. If
// VerifyConnection were absent, a config that acquired a session cache would go
// on accepting any peer while every test above still passed.
func TestProbeTLSConfig_ResumedSessionsAreVerifiedToo(t *testing.T) {
	ca := newCA(t)
	bundle, err := ca.IssueAgentCert("probe", time.Minute)
	require.NoError(t, err)
	cfg, err := probeTLSConfig(bundle, "agent-probe-qube")
	require.NoError(t, err)

	require.NotNil(t, cfg.VerifyConnection,
		"without this, a resumed session skips the only certificate check there is")

	parse := func(certPEM string) *x509.Certificate {
		block, _ := pem.Decode([]byte(certPEM))
		require.NotNil(t, block)
		c, err := x509.ParseCertificate(block.Bytes)
		require.NoError(t, err)
		return c
	}

	ours, err := ca.IssueAgentCert("agent-probe-qube", time.Minute)
	require.NoError(t, err)
	assert.NoError(t, cfg.VerifyConnection(tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{parse(ours.CertPEM)},
	}))

	stranger, err := newCA(t).IssueAgentCert("agent-x", time.Minute)
	require.NoError(t, err)
	assert.Error(t, cfg.VerifyConnection(tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{parse(stranger.CertPEM)},
	}), "a resumed session must not be a way past the CA check")

	assert.Error(t, cfg.VerifyConnection(tls.ConnectionState{}))
}

// derOf converts a PEM certificate into the raw DER form the TLS stack hands to
// VerifyPeerCertificate.
func derOf(t *testing.T, certPEM string) [][]byte {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	require.NotNil(t, block)
	return [][]byte{block.Bytes}
}

// TestForeignQubeCertIsRejected — chain-to-CA answers "is this our fleet", NOT
// "is this the qube we dialed". Every qube holds a CA-signed certificate, so
// without a name binding any one of them authenticates as any other.
//
// Reachable, not theoretical: qubes share an L2 bridge, so a compromised qube
// can ARP-spoof another's address and answer with its own valid certificate.
// The console would then record the VICTIM as healthy — an attacker-triggerable
// false green, which is precisely what this prober exists to eliminate.
func TestForeignQubeCertIsRejected(t *testing.T) {
	ca := newCA(t)
	// The server is a legitimate fleet member — its certificate is signed by the
	// same CA. It simply is not the qube we dialed.
	addr, _ := startAgent(t, ca, ca, "agent-other-qube", &fakeInvoker{resp: []byte("pong")})
	host, port := hostPort(t, addr)

	p := NewAgentProber(staticCA{ca: ca}, nil, "0.0.0.0:"+port, 10*time.Second)
	res := p.Probe(context.Background(), &models.Qube{
		ID: "victim", Name: "victim", IPAddress: host,
	})

	assert.False(t, res.Reachable, "a valid certificate from the WRONG qube must not pass")
	assert.Equal(t, AgentProbeTLSRejected, res.Status)
	assert.Contains(t, res.Reason, "agent-victim",
		"the reason must name which identity was expected")
}
