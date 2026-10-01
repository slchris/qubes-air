package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startIdentityAgent runs a real mTLS agent like startAgent, and records the
// client certificate each connection presented.
func startIdentityAgent(t *testing.T, ca *pki.CA, serverCN string) (addr string, seen func() []*x509.Certificate) {
	t.Helper()
	bundle, err := ca.IssueAgentCert(serverCN, time.Hour)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM([]byte(bundle.CAPEM)))

	var mu sync.Mutex
	var peers []*x509.Certificate
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr = lis.Addr().String()
	require.NoError(t, lis.Close())
	srv := transportgrpc.NewServer(transportgrpc.ServerConfig{
		Listen: addr,
		TLS: &tls.Config{
			Certificates: []tls.Certificate{pair},
			ClientCAs:    pool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS13,
			VerifyConnection: func(cs tls.ConnectionState) error {
				mu.Lock()
				defer mu.Unlock()
				if len(cs.PeerCertificates) > 0 {
					peers = append(peers, cs.PeerCertificates[0])
				}
				return nil
			},
		},
	}, &fakeInvoker{resp: []byte("pong")})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(cancel)
	waitForListener(t, addr)
	return addr, func() []*x509.Certificate {
		mu.Lock()
		defer mu.Unlock()
		return append([]*x509.Certificate(nil), peers...)
	}
}

// The streamer reaches the qube's own agent with a short-lived
// console-desktop certificate and nothing else.
func TestAgentDesktopStreamerPresentsTheDesktopIdentity(t *testing.T) {
	ca := newCA(t)
	addr, seen := startIdentityAgent(t, ca, "agent-desk-qube")
	host, port := hostPort(t, addr)
	streamer := NewAgentDesktopStreamer(staticCA{ca: ca}, "0.0.0.0:"+port)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relay, err := streamer.OpenDesktopTransport(ctx, &models.Qube{Name: "desk-qube", IPAddress: host})
	require.NoError(t, err)
	cli, ok := relay.(*transportgrpc.Client)
	require.True(t, ok, "the desktop transport is the gRPC client")
	out, err := callWhenConnected(ctx, cli, "desk-qube", pingService, nil)
	require.NoError(t, err)
	assert.Contains(t, string(out), "pong")

	peers := seen()
	require.NotEmpty(t, peers)
	leaf := peers[0]
	assert.Equal(t, pki.ConsoleDesktopCN, leaf.Subject.CommonName)
	role, err := pki.RoleOf(leaf)
	require.NoError(t, err)
	assert.Equal(t, pki.RoleConsole, role)
	assert.LessOrEqual(t, time.Until(leaf.NotAfter), desktopCertLifetime)
}

// A listener at the qube's address that is not that qube's agent never gets
// a working tunnel.
func TestAgentDesktopStreamerRefusesAnotherQubesAgent(t *testing.T) {
	ca := newCA(t)
	addr, _ := startIdentityAgent(t, ca, "agent-some-other-qube")
	host, port := hostPort(t, addr)
	streamer := NewAgentDesktopStreamer(staticCA{ca: ca}, "0.0.0.0:"+port)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	relay, err := streamer.OpenDesktopTransport(ctx, &models.Qube{Name: "desk-qube", IPAddress: host})
	require.NoError(t, err)
	err = callDesktopStreamWhenConnected(ctx, relay, "desk-qube", eofReader{}, io.Discard)
	require.ErrorContains(t, err, "agent tunnel never established")
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

func TestAgentDesktopStreamerFailsClosed(t *testing.T) {
	ctx := context.Background()
	_, err := (*AgentDesktopStreamer)(nil).OpenDesktopTransport(ctx, &models.Qube{Name: "q", IPAddress: "192.0.2.1"})
	require.ErrorIs(t, err, ErrDesktopTransportUnavailable)

	noCA := NewAgentDesktopStreamer(staticCA{err: errors.New("credential store locked")}, "0.0.0.0:8443")
	_, err = noCA.OpenDesktopTransport(ctx, &models.Qube{Name: "q", IPAddress: "192.0.2.1"})
	require.ErrorIs(t, err, ErrDesktopTransportUnavailable)

	// Without an address there is nothing to dial, and no certificate is minted.
	noAddress := NewAgentDesktopStreamer(unreachedCA{t: t}, "0.0.0.0:8443")
	_, err = noAddress.OpenDesktopTransport(ctx, &models.Qube{Name: "q"})
	require.ErrorIs(t, err, ErrDesktopNotReady)
	_, err = noAddress.OpenDesktopTransport(ctx, nil)
	require.ErrorIs(t, err, ErrDesktopNotReady)
}

// echoStream is a StreamTransport that copies stdin to stdout, optionally
// reporting ErrNotConnected for the first few attempts.
type echoStream struct {
	notConnected atomic.Int32
	attempts     atomic.Int32
	err          error
}

func (e *echoStream) CallStream(ctx context.Context, _, _ string, stdin io.Reader, stdout io.Writer) error {
	e.attempts.Add(1)
	if e.notConnected.Add(-1) >= 0 {
		return transportgrpc.ErrNotConnected
	}
	if e.err != nil {
		return e.err
	}
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdout, stdin)
		copied <- err
	}()
	select {
	case err := <-copied:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestXpraStreamRetriesUntilTheTunnelIsUpWithoutLosingBytes(t *testing.T) {
	relay := &echoStream{}
	relay.notConnected.Store(3)
	stream := newXpraStream(context.Background(), relay, "q")
	defer func() { _ = stream.Close(); _ = stream.wait() }()

	go func() { _, _ = stream.Write([]byte("hello")) }()
	got := make([]byte, 5)
	_, err := io.ReadFull(stream, got)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
	assert.Equal(t, int32(4), relay.attempts.Load())
}

// Close from another goroutine unblocks a Read in progress, and is safe to
// repeat concurrently: GetScreenshot's deadline callback and its deferred
// close can both reach it.
func TestXpraStreamCloseUnblocksAPendingRead(t *testing.T) {
	relay := &echoStream{}
	stream := newXpraStream(context.Background(), relay, "q")
	readErr := make(chan error, 1)
	go func() {
		_, err := stream.Read(make([]byte, 16))
		readErr <- err
	}()
	time.Sleep(20 * time.Millisecond)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = stream.Close() }()
	}
	wg.Wait()
	select {
	case err := <-readErr:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not unblock the pending Read")
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- stream.wait() }()
	select {
	case err := <-waitErr:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("the stream goroutine did not finish after Close")
	}
	_, err := stream.Write([]byte("x"))
	require.Error(t, err, "a closed stream refuses writes")
}

// A stream that fails reports its cause to the reader and to wait.
func TestXpraStreamSurfacesTheStreamError(t *testing.T) {
	refused := errors.New("remote: DENIED: restricted")
	stream := newXpraStream(context.Background(), &echoStream{err: refused}, "q")
	_, err := stream.Read(make([]byte, 1))
	require.ErrorIs(t, err, refused)
	require.ErrorIs(t, stream.wait(), refused)
	require.NoError(t, stream.Close())
}

// Waiting for a tunnel that never comes up ends with the context.
func TestXpraStreamGivesUpWhenTheTunnelNeverComesUp(t *testing.T) {
	relay := &echoStream{}
	relay.notConnected.Store(1 << 30)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	stream := newXpraStream(ctx, relay, "q")
	err := stream.wait()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "agent tunnel never established")
}
