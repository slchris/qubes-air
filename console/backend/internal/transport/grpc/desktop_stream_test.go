package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// The console asks for the desktop port by the service name and the agent
// guards it by the port number; both must describe the same port.
func TestDesktopStreamServiceNamesTheGuardedPort(t *testing.T) {
	port, ok := streamLocalPort(DesktopStreamService)
	require.True(t, ok, "the desktop service must be an allowed stream port")
	assert.Equal(t, DesktopStreamPort, port)
	assert.Equal(t, streamServicePrefix+strconv.Itoa(DesktopStreamPort), DesktopStreamService)
}

func TestAuthorizeStreamCaller(t *testing.T) {
	desktop := serviceCallerCert(pki.ConsoleDesktopCN, "console")
	tests := []struct {
		name string
		port int
		ctx  context.Context
		want string // "" means allowed
	}{
		{name: "desktop identity on the desktop port", port: DesktopStreamPort, ctx: verifiedPeer(desktop)},
		{name: "relay on the desktop port", port: DesktopStreamPort, ctx: verifiedPeer(serviceCallerCert("relay-dom0", "relay"))},
		{name: "other GUI ports keep their rule", port: 5900, ctx: context.Background()},
		{name: "adjacent port is not the desktop port", port: DesktopStreamPort + 1, ctx: verifiedPeer(serviceCallerCert("console-probe", "console"))},

		{name: "probe identity refused", port: DesktopStreamPort, ctx: verifiedPeer(serviceCallerCert("console-probe", "console")), want: "restricted"},
		{name: "unlock identity refused", port: DesktopStreamPort, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleUnlockCN, "console")), want: "restricted"},
		{name: "bootstrap identity refused", port: DesktopStreamPort, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleBootstrapCN, "console")), want: "restricted"},
		{name: "desktop name with the agent role", port: DesktopStreamPort, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleDesktopCN, "agent")), want: "restricted"},
		{name: "desktop name with no role", port: DesktopStreamPort, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleDesktopCN)), want: "restricted"},
		{name: "desktop name with two roles", port: DesktopStreamPort, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleDesktopCN, "console", "relay")), want: "restricted"},
		{name: "missing peer", port: DesktopStreamPort, ctx: context.Background(), want: "requires a verified client certificate"},
		{name: "peer without a verified chain", port: DesktopStreamPort,
			ctx:  peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{}}),
			want: "requires a verified client certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := authorizeStreamCaller(tt.ctx, tt.port)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.want)
		})
	}
}

// listenDesktopPort binds the desktop port on loopback, or skips when another
// process holds it. handle serves each accepted connection.
func listenDesktopPort(t *testing.T, handle func(net.Conn)) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(DesktopStreamPort))
	if err != nil {
		t.Skipf("cannot bind the desktop port: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
}

// A refused desktop stream is answered with a denial and never dials the port.
func TestOpenStreamRefusesDesktopPortBeforeDialing(t *testing.T) {
	var accepted atomic.Int32
	listenDesktopPort(t, func(c net.Conn) {
		accepted.Add(1)
		_ = c.Close()
	})

	var sent []*pb.Frame
	ctx, cancel := context.WithCancel(verifiedPeer(serviceCallerCert("console-probe", "console")))
	defer cancel()
	sess := &tunnelSession{ctx: ctx, server: &Server{}, streams: map[string]*serverStream{},
		send: func(f *pb.Frame) error { sent = append(sent, f); return nil }}
	sess.openStream("request-1", DesktopStreamService)

	require.Len(t, sent, 1)
	assert.Equal(t, codeDenied, sent[0].GetError().GetCode())
	assert.Empty(t, sess.streams)
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, accepted.Load(), "a refused caller must not reach the desktop port")
}

// Over real mTLS: the handshake fills VerifiedChains, so the check sees the
// certificate the console actually minted.
func TestDesktopStreamOverMTLSNeedsTheDesktopIdentity(t *testing.T) {
	listenDesktopPort(t, func(c net.Conn) { _, _ = io.Copy(c, c); _ = c.Close() })

	ca, err := pki.NewCA("qubes-air-console", 0)
	require.NoError(t, err)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	srv := NewServer(ServerConfig{Listen: addr, TLS: mkServerTLSFromCA(t, ca)}, &recordingInvoker{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	waitDial(t, addr)

	stream := func(cn string) ([]byte, error) {
		bundle, err := ca.IssueAgentCert(cn, time.Hour)
		require.NoError(t, err)
		pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
		require.NoError(t, err)
		pool := x509.NewCertPool()
		require.True(t, pool.AppendCertsFromPEM([]byte(bundle.CAPEM)))
		cli := NewClient(ClientConfig{
			RemoteEndpoint: addr, RelayName: cn, RemoteName: "remote-dev",
			ReconnectMin: 20 * time.Millisecond, ReconnectMax: 200 * time.Millisecond,
			TLS: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13},
		}, nil)
		cliCtx, stop := context.WithCancel(ctx)
		defer stop()
		go func() { _ = cli.Start(cliCtx) }()
		_, err = callWhenReady(t, cli, "remote-dev", "qubesair.Ping", nil)
		require.NoError(t, err)

		stdinR, stdinW := io.Pipe()
		stdoutR, stdoutW := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = stdoutW.CloseWithError(cli.CallStream(cliCtx, "remote-dev", DesktopStreamService, stdinR, stdoutW))
		}()
		go func() { _, _ = stdinW.Write([]byte("frame?")) }()
		got := make([]byte, len("frame?"))
		_, readErr := io.ReadFull(stdoutR, got)
		_ = stdinW.Close()
		stop()
		<-done
		return got, readErr
	}

	_, err = stream("console-probe")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restricted to relays and the console-desktop")

	got, err := stream(pki.ConsoleDesktopCN)
	require.NoError(t, err)
	assert.Equal(t, "frame?", string(got))

	got, err = stream("relay-dom0")
	require.NoError(t, err, "a relay's GUI path to the desktop port is unchanged")
	assert.Equal(t, "frame?", string(got))
}
