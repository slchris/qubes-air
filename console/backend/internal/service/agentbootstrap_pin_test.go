package service

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/slchris/qubes-air/console/internal/agent"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/transport"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

// fixedCA hands out one CA, so the console's client certificate and the CA the
// pending agent trusts are the same one.
type fixedCA struct{ ca *pki.CA }

func (f fixedCA) CA(context.Context) (*pki.CA, error) { return f.ca, nil }

// staticPin answers every pin lookup with the same result.
type staticPin struct {
	pin string
	err error
}

func (s staticPin) PendingPlaceholderSPKIFingerprint(context.Context, string, string, time.Time) (string, error) {
	return s.pin, s.err
}

// signingIssuer stands in for BootstrapIssuer: it records every token it is
// asked to redeem and signs the CSR for qubeName with the CA.
type signingIssuer struct {
	ca       *pki.CA
	qubeName string

	mu     sync.Mutex
	tokens []string
}

func (s *signingIssuer) IssueFirstCertificate(_ context.Context, token, csrPEM string) (*IssuedBootstrapCert, error) {
	s.mu.Lock()
	s.tokens = append(s.tokens, token)
	s.mu.Unlock()
	signed, err := s.ca.SignAgentCSR(csrPEM, AgentCommonName(s.qubeName), time.Hour)
	if err != nil {
		return nil, err
	}
	return &IssuedBootstrapCert{
		QubeName: s.qubeName, CertPEM: signed.CertPEM, CAPEM: signed.CAPEM,
		Fingerprint: signed.Fingerprint, SubjectCN: AgentCommonName(s.qubeName), NotAfter: signed.NotAfter,
	}, nil
}

func (s *signingIssuer) redeemed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tokens...)
}

// recordingAgentInvoker records every service a pending agent was asked to run
// before delegating to the real one.
type recordingAgentInvoker struct {
	inner transportgrpc.QrexecInvoker

	mu    sync.Mutex
	calls []string
}

func (r *recordingAgentInvoker) Invoke(ctx context.Context, target, service string, in []byte) (transport.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, service)
	r.mu.Unlock()
	return r.inner.Invoke(ctx, target, service, in)
}

func (r *recordingAgentInvoker) served() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// startPendingAgent runs a real agent that has only its cloud-init CA and the
// given token, listening on loopback, and returns its port.
func startPendingAgent(t *testing.T, ca *pki.CA, qubeName, token string) (string, *recordingAgentInvoker) {
	t.Helper()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}), 0o600))
	identity, err := agent.NewPendingIdentity(filepath.Join(dir, "agent.pem"), filepath.Join(dir, "agent-key.pem"), caPath)
	require.NoError(t, err)

	local := agent.NewLocalInvoker(qubeName, nil)
	boot, err := agent.NewBootstrapService(identity, qubeName, token, nil)
	require.NoError(t, err)
	require.NoError(t, boot.RegisterBuiltins(local))
	inv := &recordingAgentInvoker{inner: local}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	srv := transportgrpc.NewServer(transportgrpc.ServerConfig{
		Listen: addr, TLS: identity.ServerTLSConfig(), CertSource: boot,
	}, inv)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx) }()
	waitForListener(t, addr)

	_, port := hostPort(t, addr)
	return port, inv
}

// The console dials a genuine pending agent with the pin minted for its token,
// and the whole exchange completes over the pinned session.
func TestBootstrapCompletesAgainstThePinnedAgent(t *testing.T) {
	ca := newCA(t)
	const qubeName, token = "remote-dev", "token-minted-for-remote-dev"
	port, served := startPendingAgent(t, ca, qubeName, token)
	pin, err := pki.BootstrapPlaceholderSPKIFingerprint(token, qubeName)
	require.NoError(t, err)

	issuer := &signingIssuer{ca: ca, qubeName: qubeName}
	b := NewAgentBootstrapper(fixedCA{ca}, issuer, "0.0.0.0:"+port, 10*time.Second).
		WithBootstrapPeerPinProvider(staticPin{pin: pin})
	res := b.Bootstrap(context.Background(), &models.Qube{ID: "qube-1", Name: qubeName, IPAddress: "127.0.0.1"})

	require.Equal(t, BootstrapOK, res.Status, res.Reason)
	assert.Equal(t, []string{token}, issuer.redeemed())
	assert.Equal(t, []string{agent.ServiceBeginBootstrap, agent.ServiceCompleteBootstrap}, served.served())
}

// A listener at the qube's address that holds a placeholder derived from some
// OTHER token — an impostor, or a guest provisioned with another qube's
// user-data — fails the handshake. The console sends it nothing: it never runs
// a single service, so it is never asked for (and never hands over) a token,
// and nothing reaches the issuer to redeem.
func TestBootstrapNeverTalksToAPlaceholderMintedFromAnotherToken(t *testing.T) {
	ca := newCA(t)
	const qubeName = "remote-dev"
	port, served := startPendingAgent(t, ca, qubeName, "attacker-held-token")
	pin, err := pki.BootstrapPlaceholderSPKIFingerprint("token-minted-for-remote-dev", qubeName)
	require.NoError(t, err)

	issuer := &signingIssuer{ca: ca, qubeName: qubeName}
	b := NewAgentBootstrapper(fixedCA{ca}, issuer, "0.0.0.0:"+port, time.Second).
		WithBootstrapPeerPinProvider(staticPin{pin: pin})
	logs := captureConcurrentLog(t)
	res := b.Bootstrap(context.Background(), &models.Qube{ID: "qube-1", Name: qubeName, IPAddress: "127.0.0.1"})

	assert.Equal(t, BootstrapUnreachable, res.Status, res.Reason)
	assert.Empty(t, served.served(), "the impostor must not receive a single call")
	assert.Empty(t, issuer.redeemed(), "no token may reach the issuer from an unpinned peer")
	out := logs.String()
	assert.Contains(t, out, "does not match the pin", "the operator must be told why the listener was refused")
	assert.Equal(t, 1, strings.Count(out, "refusing the listener"), "one line per attempt, not one per handshake retry")
}

// captureConcurrentLog redirects the standard logger for the rest of the test
// into a buffer that the tunnel client's goroutines may write concurrently.
func captureConcurrentLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	return buf
}

// syncBuffer is a bytes.Buffer safe for the logger's concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// End to end: the listener serves the placeholder derived from this qube's
// own token (so the pinned handshake passes), but its BeginBootstrap answers
// with a token minted for another qube. The console must refuse before the
// issuer ever sees that token, and must deliver nothing.
func TestBootstrapRefusesAStolenTokenOverThePinnedSession(t *testing.T) {
	ca := newCA(t)
	const qubeName, ownToken = "remote-dev", "token-minted-for-remote-dev"

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}), 0o600))
	identity, err := agent.NewPendingIdentity(filepath.Join(dir, "agent.pem"), filepath.Join(dir, "agent-key.pem"), caPath)
	require.NoError(t, err)
	// The placeholder comes from the qube's own token ...
	own, err := agent.NewBootstrapService(identity, qubeName, ownToken, nil)
	require.NoError(t, err)
	// ... but the bootstrap calls answer with a token stolen from another qube.
	thief, err := agent.NewBootstrapService(identity, qubeName, "token-minted-for-remote-other", nil)
	require.NoError(t, err)
	local := agent.NewLocalInvoker(qubeName, nil)
	require.NoError(t, thief.RegisterBuiltins(local))
	served := &recordingAgentInvoker{inner: local}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	srv := transportgrpc.NewServer(transportgrpc.ServerConfig{Listen: addr, TLS: identity.ServerTLSConfig(), CertSource: own}, served)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx) }()
	waitForListener(t, addr)
	_, port := hostPort(t, addr)

	pin, err := pki.BootstrapPlaceholderSPKIFingerprint(ownToken, qubeName)
	require.NoError(t, err)
	issuer := &signingIssuer{ca: ca, qubeName: qubeName}
	b := NewAgentBootstrapper(fixedCA{ca}, issuer, "0.0.0.0:"+port, 10*time.Second).
		WithBootstrapPeerPinProvider(staticPin{pin: pin})
	res := b.Bootstrap(context.Background(), &models.Qube{ID: "qube-1", Name: qubeName, IPAddress: "127.0.0.1"})

	assert.Equal(t, BootstrapRefused, res.Status, res.Reason)
	assert.Empty(t, issuer.redeemed(), "the stolen token was redeemed")
	assert.Equal(t, []string{agent.ServiceBeginBootstrap}, served.served(), "CompleteBootstrap must never be sent")
	assert.False(t, identity.HasCertificate(), "no certificate may reach the guest")
}

func TestBootstrapRefusesToDialWithoutAPeerPin(t *testing.T) {
	for name, tc := range map[string]struct {
		pins BootstrapPeerPinProvider
		want string
	}{
		"no provider wired": {pins: nil, want: "no bootstrap peer pin provider"},
		"no live token": {pins: staticPin{err: fmt.Errorf("%w: qube \"remote-dev\" has no unredeemed, unexpired bootstrap token",
			repository.ErrNoBootstrapPin)}, want: "no unredeemed"},
		"token from before pinning": {pins: staticPin{err: fmt.Errorf("%w: predates peer pinning; re-provision it",
			repository.ErrNoBootstrapPin)}, want: "predates peer pinning"},
	} {
		t.Run(name, func(t *testing.T) {
			issuer := &fakeIssuer{}
			b := NewAgentBootstrapper(stubCA{}, issuer, "", 0)
			if tc.pins != nil {
				b = b.WithBootstrapPeerPinProvider(tc.pins)
			}
			res := b.Bootstrap(context.Background(), testBootstrapQube())

			assert.Equal(t, BootstrapNotConfigured, res.Status)
			assert.False(t, res.Status.AgentAnswered())
			assert.Contains(t, res.Reason, tc.want)
			assert.Contains(t, res.Reason, "cannot authenticate first contact")
			assert.Empty(t, issuer.gotToken)
		})
	}
}

// A pin that is not a SHA-256 digest can only come from a damaged row. It must
// stop the dial — not build a verifier that silently rejects every peer — and
// be reported as the console's fault, not as an unreachable VM.
func TestBootstrapRefusesAMalformedPin(t *testing.T) {
	for _, pin := range []string{"not-a-digest", "abcd", strings.Repeat("zz", 32)} {
		issuer := &fakeIssuer{}
		b := NewAgentBootstrapper(stubCA{}, issuer, "", 0).
			WithBootstrapPeerPinProvider(staticPin{pin: pin})
		res := b.Bootstrap(context.Background(), testBootstrapQube())

		assert.Equal(t, BootstrapConsoleFailed, res.Status, pin)
		assert.True(t, res.Status.AgentAnswered(), "a console-side fault must not be recorded against the VM")
		assert.Contains(t, res.Reason, "stored bootstrap peer pin is damaged")
		assert.Empty(t, issuer.gotToken)
	}
}

// A lookup that fails for a reason other than "no pinned token" — the database
// is locked, closed, or broken — says nothing about whether the qube can be
// bootstrapped. It is the console's failure and is retried, not reported as
// "this console cannot bootstrap at all".
func TestBootstrapTreatsAPinLookupFailureAsAConsoleFault(t *testing.T) {
	t.Run("provider error", func(t *testing.T) {
		b := NewAgentBootstrapper(stubCA{}, &fakeIssuer{}, "", 0).
			WithBootstrapPeerPinProvider(staticPin{err: errors.New("database is locked")})
		res := b.Bootstrap(context.Background(), testBootstrapQube())

		assert.Equal(t, BootstrapConsoleFailed, res.Status)
		assert.Contains(t, res.Reason, "could not load the bootstrap peer pin: database is locked")
	})
	t.Run("real repository on a closed database", func(t *testing.T) {
		cfg := database.DefaultConfig()
		cfg.DSN = filepath.Join(t.TempDir(), "closed.db")
		db, err := database.New(cfg)
		require.NoError(t, err)
		tokens := repository.NewBootstrapTokenRepository(db)
		require.NoError(t, db.Close())

		b := NewAgentBootstrapper(stubCA{}, &fakeIssuer{}, "", 0).WithBootstrapPeerPinProvider(tokens)
		res := b.Bootstrap(context.Background(), testBootstrapQube())

		assert.Equal(t, BootstrapConsoleFailed, res.Status, res.Reason)
		assert.Contains(t, res.Reason, "could not load the bootstrap peer pin")
	})
	t.Run("real repository with no token stays not_configured", func(t *testing.T) {
		cfg := database.DefaultConfig()
		cfg.DSN = filepath.Join(t.TempDir(), "empty.db")
		db, err := database.New(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })

		b := NewAgentBootstrapper(stubCA{}, &fakeIssuer{}, "", 0).
			WithBootstrapPeerPinProvider(repository.NewBootstrapTokenRepository(db))
		res := b.Bootstrap(context.Background(), testBootstrapQube())

		assert.Equal(t, BootstrapNotConfigured, res.Status, res.Reason)
		assert.Contains(t, res.Reason, "re-provision it")
	})
}
