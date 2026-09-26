// desktopstream.go — the connection a desktop frame is read over.
//
// A frame is read from the Xpra server inside the qube, through the agent's
// stream proxy (qubesair.StreamTCP+10005). That needs a tunnel to THIS qube's
// agent, which the global transport cannot give (it is pinned to one
// configured endpoint), so each capture dials the qube itself, the way the
// data-disk unlocker does: a short-lived client certificate, the agent
// pinned to agent-<qube>, one gRPC client that lives only as long as the
// capture.
//
// The certificate carries the console-desktop name. The agent opens the
// desktop port only to that console identity (and to relays), so a probe or
// renewal certificate cannot read the screen, and this one can do nothing the
// agent reserves for another identity.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/transport"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

// desktopCertLifetime bounds the console-desktop certificate. It covers one
// capture (desktopFrameTimeout) with room for clock skew, and nothing more.
const desktopCertLifetime = 2 * time.Minute

// DesktopTransportOpener opens a stream transport to one qube's agent under
// the console-desktop identity. The transport lives until ctx ends.
type DesktopTransportOpener interface {
	OpenDesktopTransport(ctx context.Context, qube *models.Qube) (transport.StreamTransport, error)
}

// AgentDesktopStreamer is the production DesktopTransportOpener.
type AgentDesktopStreamer struct {
	ca     CAProvider
	dialer AgentDialer
}

// NewAgentDesktopStreamer builds a streamer that reaches agents on the
// configured agent listen port.
func NewAgentDesktopStreamer(ca CAProvider, agentListen string) *AgentDesktopStreamer {
	return &AgentDesktopStreamer{ca: ca, dialer: NewDirectDialer(agentListen)}
}

// OpenDesktopTransport mints the certificate, starts a client pinned to the
// qube's agent and returns it once it is dialing. The caller waits for the
// tunnel through the stream it opens (callDesktopStreamWhenConnected).
func (d *AgentDesktopStreamer) OpenDesktopTransport(ctx context.Context, qube *models.Qube) (transport.StreamTransport, error) {
	if d == nil || d.ca == nil {
		return nil, ErrDesktopTransportUnavailable
	}
	if qube == nil || strings.TrimSpace(qube.IPAddress) == "" {
		return nil, fmt.Errorf("%w: the qube has no address", ErrDesktopNotReady)
	}
	ca, err := d.ca.CA(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: no usable CA: %v", ErrDesktopTransportUnavailable, err)
	}
	bundle, err := ca.IssueAgentCert(pki.ConsoleDesktopCN, desktopCertLifetime)
	if err != nil {
		return nil, fmt.Errorf("%w: mint desktop client certificate: %v", ErrDesktopTransportUnavailable, err)
	}
	// The frame must come from THIS qube's agent, not whatever answers at its
	// address: the same agent-<qube> pin the prober and unlocker enforce.
	tlsCfg, err := probeTLSConfig(bundle, AgentCommonName(qube.Name))
	if err != nil {
		return nil, fmt.Errorf("%w: desktop client certificate unusable: %v", ErrDesktopTransportUnavailable, err)
	}
	cli := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: d.dialer.Address(qube),
		RelayName:      pki.ConsoleDesktopCN,
		RemoteName:     qube.Name,
		Dialer:         dialFuncFor(d.dialer, qube),
		ReconnectMin:   20 * time.Millisecond,
		ReconnectMax:   200 * time.Millisecond,
		TLS:            tlsCfg,
	}, nil)
	go func() { _ = cli.Start(ctx) }()
	return cli, nil
}

// xpraStream adapts one CallStream to the io.ReadWriteCloser the Xpra client
// reads and writes: Write feeds the stream's stdin, Read drains its stdout.
//
// Close may be called from any goroutine, while a Read or Write is blocked,
// and more than once: xpra.GetScreenshot closes the stream from a
// context.AfterFunc callback when its deadline passes mid-read. It never
// blocks; wait is what the owner calls, after canceling the transport, to
// know the stream goroutine has finished.
type xpraStream struct {
	input     *io.PipeWriter
	output    *io.PipeReader
	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}
	err       error // CallStream's result; read only after done is closed
}

// newXpraStream opens the desktop stream to target over relay.
func newXpraStream(parent context.Context, relay transport.StreamTransport, target string) *xpraStream {
	ctx, cancel := context.WithCancel(parent)
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	s := &xpraStream{input: stdinW, output: stdoutR, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		s.err = callDesktopStreamWhenConnected(ctx, relay, target, stdinR, stdoutW)
		// Unblock both sides: a Read sees the stream's end (or its error), and
		// the transport's stdin pump sees EOF and stops.
		_ = stdoutW.CloseWithError(s.err)
		_ = stdinR.CloseWithError(s.err)
	}()
	return s
}

func (s *xpraStream) Read(p []byte) (int, error)  { return s.output.Read(p) }
func (s *xpraStream) Write(p []byte) (int, error) { return s.input.Write(p) }

// Close ends the stream: it cancels the call and closes both pipes, which
// fails any Read or Write blocked on them. Safe to call concurrently and
// repeatedly.
func (s *xpraStream) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		_ = s.input.Close()
		_ = s.output.Close()
	})
	return nil
}

// wait blocks until the stream goroutine has returned and reports how the
// call ended. It returns promptly once the stream's context is done.
func (s *xpraStream) wait() error {
	<-s.done
	return s.err
}

// callDesktopStreamWhenConnected opens the desktop stream, retrying only while
// the tunnel is still coming up. CallStream reports ErrNotConnected before it
// reads any stdin, so a retry never loses bytes the Xpra client already wrote.
func callDesktopStreamWhenConnected(
	ctx context.Context, relay transport.StreamTransport, target string, stdin io.Reader, stdout io.Writer,
) error {
	const retryEvery = 25 * time.Millisecond
	for {
		err := relay.CallStream(ctx, target, transportgrpc.DesktopStreamService, stdin, stdout)
		if !errors.Is(err, transportgrpc.ErrNotConnected) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent tunnel never established: %w", ctx.Err())
		case <-time.After(retryEvery):
		}
	}
}
