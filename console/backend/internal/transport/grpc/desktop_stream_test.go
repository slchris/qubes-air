package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
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

func TestDesktopStreamIsAFixedService(t *testing.T) {
	require.Equal(t, "qubesair.StreamDesktop", DesktopStreamService)
	require.True(t, validStreamService(DesktopStreamService))
	for _, service := range []string{DesktopStreamService + "+path", DesktopStreamService + "Extra", "qubesair.StreamTCP+10005", "qubesair.StreamTCP+22"} {
		require.False(t, validStreamService(service), service)
	}
}

func TestInvalidDesktopStreamNeverFallsThroughToQrexec(t *testing.T) {
	for _, service := range []string{DesktopStreamService + "+path", DesktopStreamService + "Extra", "qubesair.StreamTCP+10005"} {
		var sent []*pb.Frame
		sess := &tunnelSession{ctx: context.Background(),
			send: func(f *pb.Frame) error { sent = append(sent, f); return nil }}
		err := sess.handleRequestHeader(&pb.Frame{RequestId: "invalid"}, &pb.Frame_RequestHeader{
			RequestHeader: &pb.RequestHeader{Direction: pb.Direction_LOCAL_TO_REMOTE, QrexecService: service},
		})
		require.NoError(t, err)
		require.Len(t, sent, 1, service)
		require.Equal(t, codeInvalid, sent[0].GetError().GetCode(), service)
		require.Empty(t, sess.streams)
	}
}

func TestAuthorizeStreamCaller(t *testing.T) {
	desktop := serviceCallerCert(pki.ConsoleDesktopCN, "console")
	tests := []struct {
		name    string
		service string
		ctx     context.Context
		want    string // "" means allowed
	}{
		{name: "desktop identity on the desktop socket", service: DesktopStreamService, ctx: verifiedPeer(desktop)},
		{name: "relay on the desktop socket", service: DesktopStreamService, ctx: verifiedPeer(serviceCallerCert("relay-dom0", "relay"))},
		{name: "other GUI ports keep their rule", service: "qubesair.StreamTCP+5900", ctx: context.Background()},
		{name: "adjacent port is not the desktop socket", service: "qubesair.StreamTCP+10006", ctx: verifiedPeer(serviceCallerCert("console-probe", "console"))},

		{name: "probe identity refused", service: DesktopStreamService, ctx: verifiedPeer(serviceCallerCert("console-probe", "console")), want: "restricted"},
		{name: "unlock identity refused", service: DesktopStreamService, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleUnlockCN, "console")), want: "restricted"},
		{name: "bootstrap identity refused", service: DesktopStreamService, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleBootstrapCN, "console")), want: "restricted"},
		{name: "desktop name with the agent role", service: DesktopStreamService, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleDesktopCN, "agent")), want: "restricted"},
		{name: "desktop name with no role", service: DesktopStreamService, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleDesktopCN)), want: "restricted"},
		{name: "desktop name with two roles", service: DesktopStreamService, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleDesktopCN, "console", "relay")), want: "restricted"},
		{name: "missing peer", service: DesktopStreamService, ctx: context.Background(), want: "requires a verified client certificate"},
		{name: "peer without a verified chain", service: DesktopStreamService,
			ctx:  peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{}}),
			want: "requires a verified client certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := authorizeStreamCaller(tt.ctx, tt.service)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func echoDesktopStream(context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	go func() { _, _ = io.Copy(server, server); _ = server.Close() }()
	return client, nil
}

// A refused desktop stream is answered with a denial and never dials the socket.
func TestOpenStreamRefusesDesktopBeforeDialing(t *testing.T) {
	var accepted atomic.Int32
	srv := &Server{desktopDialer: func(context.Context) (net.Conn, error) {
		accepted.Add(1)
		return nil, nil
	}}

	var sent []*pb.Frame
	ctx, cancel := context.WithCancel(verifiedPeer(serviceCallerCert("console-probe", "console")))
	defer cancel()
	sess := &tunnelSession{ctx: ctx, server: srv, streams: map[string]*serverStream{},
		send: func(f *pb.Frame) error { sent = append(sent, f); return nil }}
	sess.openStream("request-1", DesktopStreamService)

	require.Len(t, sent, 1)
	assert.Equal(t, codeDenied, sent[0].GetError().GetCode())
	assert.Empty(t, sess.streams)
	assert.Zero(t, accepted.Load(), "a refused caller must not reach the desktop socket")
}

// Over real mTLS: the handshake fills VerifiedChains, so the check sees the
// certificate the console actually minted.
func TestDesktopUnixStreamOverMTLSNeedsTheDesktopIdentity(t *testing.T) {
	ca, err := pki.NewCA("qubes-air-console", 0)
	require.NoError(t, err)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	srv := NewServer(ServerConfig{Listen: addr, TLS: mkServerTLSFromCA(t, ca)}, &recordingInvoker{})
	srv.desktopDialer = echoDesktopStream
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
	require.NoError(t, err, "a relay's GUI path to the desktop socket is unchanged")
	assert.Equal(t, "frame?", string(got))
}
