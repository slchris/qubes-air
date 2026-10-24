//go:build linux && xpra_integration

package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/desktop"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/xpra"
	"github.com/stretchr/testify/require"
)

// The runner starts the real pinned Xpra package, launches a window through the
// installed broker, and invokes these tests in its disposable Linux container.
func requireXpraContainer(t *testing.T) {
	t.Helper()
	require.Equal(t, "1", os.Getenv("QUBESAIR_DESKTOP_TEST_CONTAINER"))
	_, err := os.Stat("/.dockerenv")
	require.NoError(t, err, "live Xpra tests require their disposable container")
}

func TestXpraLiveKernelPeer(t *testing.T) {
	requireXpraContainer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := desktop.DialXpra(ctx)
	require.NoError(t, err, "the denied-UID case must reach Xpra's own authenticator")
	frame, err := xpra.GetScreenshot(ctx, conn)
	if os.Getuid() == 1001 {
		require.ErrorIs(t, err, xpra.ErrScreenshotSession)
		require.Empty(t, frame.PNG)
		return
	}
	require.Contains(t, []int{0, 1000}, os.Getuid())
	require.NoError(t, err)
	require.Positive(t, frame.Width)
	require.Positive(t, frame.Height)
	require.Greater(t, len(frame.PNG), 100)
}

func TestXpraLiveSessionUnavailable(t *testing.T) {
	requireXpraContainer(t)
	conn, err := desktop.DialXpra(context.Background())
	require.Error(t, err)
	require.Nil(t, conn)
}

func TestXpraLiveScreenshotOverMTLS(t *testing.T) {
	requireXpraContainer(t)
	require.Zero(t, os.Getuid(), "the production agent connects as root")
	ca, err := pki.NewCA("xpra-integration", 0)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server := NewServer(ServerConfig{Listen: addr, TLS: mkServerTLSFromCA(t, ca)}, &recordingInvoker{})
	go func() { _ = server.Serve(ctx) }()
	waitDial(t, addr)
	for _, cn := range []string{pki.ConsoleDesktopCN, "relay-desktop-test", "console-probe"} {
		t.Run(cn, func(t *testing.T) {
			cli := liveXpraClient(t, ctx, ca, addr, cn)
			for _, service := range []string{DesktopStreamService, "qubesair.StreamTCP+10005", DesktopStreamService + "+path"} {
				frame, err := liveXpraCapture(ctx, cli, service)
				if service != DesktopStreamService || cn == "console-probe" {
					require.Error(t, err)
					require.Empty(t, frame.PNG)
					continue
				}
				require.NoError(t, err)
				require.Positive(t, frame.Width)
				// A fresh capture on the same tunnel succeeds after the first
				// one-shot connection has closed. It cannot reuse a consumed frame.
				next, err := liveXpraCapture(ctx, cli, service)
				require.NoError(t, err)
				require.Equal(t, frame.Width, next.Width)
			}
		})
	}
}

func liveXpraClient(t *testing.T, ctx context.Context, ca *pki.CA, addr, cn string) *Client {
	t.Helper()
	bundle, err := ca.IssueAgentCert(cn, time.Hour)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM([]byte(bundle.CAPEM)))
	cli := NewClient(ClientConfig{
		RemoteEndpoint: addr, RelayName: cn, RemoteName: "remote-dev",
		TLS: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13},
	}, nil)
	clientCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = cli.Start(clientCtx) }()
	_, err = callWhenReady(t, cli, "remote-dev", "qubesair.Ping", nil)
	require.NoError(t, err)
	return cli
}

type liveXpraStream struct {
	input  *io.PipeWriter
	output *io.PipeReader
}

func (s liveXpraStream) Read(b []byte) (int, error)  { return s.output.Read(b) }
func (s liveXpraStream) Write(b []byte) (int, error) { return s.input.Write(b) }
func (s liveXpraStream) Close() error {
	_ = s.input.Close()
	return s.output.Close()
}

func liveXpraCapture(ctx context.Context, cli *Client, service string) (xpra.Screenshot, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := cli.CallStream(ctx, "remote-dev", service, inputR, outputW)
		_ = inputR.CloseWithError(err)
		_ = outputW.CloseWithError(err)
	}()
	frame, err := xpra.GetScreenshot(ctx, liveXpraStream{inputW, outputR})
	cancel()
	<-done
	return frame, err
}
