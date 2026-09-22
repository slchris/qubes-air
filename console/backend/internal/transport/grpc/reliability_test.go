package grpc

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/transport"
	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	"google.golang.org/grpc"
)

// --- harness ---------------------------------------------------------------

// fakeTunnel is a hand-rolled pb.RelayTransport_TunnelClient for the tests that
// need a live-looking stream without a server. Only Send/Recv are exercised;
// the embedded nil grpc.ClientStream supplies the rest of the interface.
type fakeTunnel struct {
	grpc.ClientStream

	mu     sync.Mutex
	frames []*pb.Frame
	err    error
	sent   []*pb.Frame
	next   int
}

func (f *fakeTunnel) Send(frame *pb.Frame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, frame)
	return nil
}

func (f *fakeTunnel) Recv() (*pb.Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.next < len(f.frames) {
		frame := f.frames[f.next]
		f.next++
		return frame, nil
	}
	if f.err == nil {
		return nil, io.EOF
	}
	return nil, f.err
}

// dropDialer dials the real server and remembers every connection it opens, so
// a test can drop the live one (a network fault, seen from the client) and can
// refuse redials to hold the client in the disconnected state.
type dropDialer struct {
	mu      sync.Mutex
	conns   []net.Conn
	dials   int
	blocked atomic.Bool
}

func (d *dropDialer) dial(ctx context.Context, addr string) (net.Conn, error) {
	d.mu.Lock()
	d.dials++
	d.mu.Unlock()

	if d.blocked.Load() {
		return nil, errors.New("test dialer: dial refused (link down)")
	}
	var nd net.Dialer
	conn, err := nd.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.conns = append(d.conns, conn)
	d.mu.Unlock()
	return conn, nil
}

func (d *dropDialer) dialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials
}

// dropLive closes every connection opened so far and reports how many it closed.
func (d *dropDialer) dropLive() int {
	d.mu.Lock()
	conns := append([]net.Conn(nil), d.conns...)
	d.conns = nil
	d.mu.Unlock()

	closed := 0
	for _, c := range conns {
		if c.Close() == nil {
			closed++
		}
	}
	return closed
}

// gatedInvoker holds each forward call until its gate is opened, so a test can
// keep a call in flight and then drop the tunnel under it.
type gatedInvoker struct {
	gate    atomic.Bool
	release chan struct{}
}

func (g *gatedInvoker) Invoke(ctx context.Context, target, service string, in []byte) (transport.Result, error) {
	if g.gate.Load() {
		select {
		case <-g.release:
		case <-ctx.Done():
			return transport.Result{}, ctx.Err()
		}
	}
	return transport.Result{Stdout: []byte("handled[" + target + "/" + service + "]:" + string(in))}, nil
}

// startInvokerServer stands up a real mTLS server on a random localhost port
// with the given invoker, stopping it on test cleanup. It mirrors
// startTestServer but lets the test choose the invoker.
func startInvokerServer(t *testing.T, serverTLS *tls.Config, inv QrexecInvoker) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	srv := NewServer(ServerConfig{Listen: addr, TLS: serverTLS}, inv)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(cancel)
	waitDial(t, addr)
	return addr
}

// waitFor polls cond until it holds or the budget expires.
func waitFor(t *testing.T, budget time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", budget, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// testClientConfig is the fast-backoff config the reliability tests dial with.
func testClientConfig(addr string, tlsCfg *tls.Config) ClientConfig {
	return ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      "sys-relay-test",
		RemoteName:     "remote-test",
		KeepAlive:      100 * time.Millisecond,
		ReconnectMin:   20 * time.Millisecond,
		ReconnectMax:   100 * time.Millisecond,
		TLS:            tlsCfg,
	}
}

// connected reports whether the client currently holds a live tunnel.
func (c *Client) connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stream != nil
}

// inflightCount and streamCount read the request registries a reconnect has to
// drain. They take the client mutex like every production accessor does.
func (c *Client) inflightCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inflight)
}

func (c *Client) streamCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.streams)
}

// --- (a) disconnect / reconnect -------------------------------------------

// TestRecvLoopReturnsOnTunnelDrop pins the drop half of the reconnect contract:
// when the stream fails, recvLoop must return that error so Start can back off
// and redial. A recvLoop that swallowed the error would leave the client
// "connected" to a dead tunnel, with every later Call waiting on a frame that
// can never arrive.
func TestRecvLoopReturnsOnTunnelDrop(t *testing.T) {
	cli := NewClient(ClientConfig{}, nil)
	dropErr := errors.New("transport is closing")
	stream := &fakeTunnel{err: dropErr}

	done := make(chan error, 1)
	go func() { done <- cli.recvLoop(context.Background(), stream) }()

	select {
	case err := <-done:
		if !errors.Is(err, dropErr) {
			t.Fatalf("recvLoop returned %v, want the stream's drop error %v", err, dropErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recvLoop did not return after the stream dropped")
	}
}

// TestClearStreamFailsInflightCallAndPendingStream pins the teardown a
// reconnect depends on: dropping the tunnel must fail every in-flight forward
// call and end every streaming call, so no caller is left hanging across the
// redial (client.go clearStream).
func TestClearStreamFailsInflightCallAndPendingStream(t *testing.T) {
	cli := NewClient(ClientConfig{}, nil)
	pc := &pendingCall{done: make(chan callResult, 1)}
	ps := &pendingStream{recv: make(chan []byte, 1)}

	cli.mu.Lock()
	cli.inflight["req-call"] = pc
	cli.streams["req-stream"] = ps
	cli.stream = &fakeTunnel{}
	cli.mu.Unlock()

	cli.clearStream(ErrNotConnected)

	if cli.connected() {
		t.Error("clearStream left the dead stream published")
	}
	select {
	case res := <-pc.done:
		if !errors.Is(res.err, ErrNotConnected) {
			t.Errorf("in-flight call result err = %v, want ErrNotConnected", res.err)
		}
	default:
		t.Error("clearStream did not fail the in-flight call: it would wait across the reconnect")
	}
	if _, ok := <-ps.recv; ok {
		t.Error("clearStream did not close the pending stream's recv channel")
	}
	if !errors.Is(ps.err, ErrNotConnected) {
		t.Errorf("pending stream err = %v, want ErrNotConnected", ps.err)
	}
	if n := cli.inflightCount(); n != 0 {
		t.Errorf("inflight map kept %d entries after the drop", n)
	}
	if n := cli.streamCount(); n != 0 {
		t.Errorf("streams map kept %d entries after the drop", n)
	}
	if err := cli.send(&pb.Frame{}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("send on a dropped tunnel = %v, want ErrNotConnected", err)
	}
}

// TestClientReconnectsAfterConnectionDrop drives the whole loop against a real
// mTLS server: the client must notice the dropped connection, fail an in-flight
// call instead of hanging it, fail fast while it is down, and then redial and
// serve calls again once the link is back.
func TestClientReconnectsAfterConnectionDrop(t *testing.T) {
	caCert, caKey := mkCA(t)
	serverTLS := mkServerTLS(t, caCert, caKey)
	clientTLS := mkClientTLS(t, caCert, caKey)

	inv := &gatedInvoker{release: make(chan struct{})}
	addr := startInvokerServer(t, serverTLS, inv)

	dialer := &dropDialer{}
	cfg := testClientConfig(addr, clientTLS)
	cfg.Dialer = dialer.dial
	cli := NewClient(cfg, nil)
	cliCtx, cliCancel := context.WithCancel(context.Background())
	t.Cleanup(cliCancel)
	go func() { _ = cli.Start(cliCtx) }()

	call := func(budget time.Duration) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		out, err := cli.Call(ctx, "remote-gpu", "qubesair.Ping", []byte("ping"))
		return string(out), err
	}

	// 1) The tunnel comes up and serves a call.
	waitFor(t, 5*time.Second, "the first tunnel", cli.connected)
	out, err := call(2 * time.Second)
	if err != nil {
		t.Fatalf("call over the first tunnel failed: %v", err)
	}
	if want := "handled[remote-gpu/qubesair.Ping]:ping"; out != want {
		t.Fatalf("response = %q, want %q", out, want)
	}
	firstDials := dialer.dialCount()
	if firstDials < 1 {
		t.Fatalf("dialer was used %d times, want at least 1", firstDials)
	}

	// 2) Hold a call in flight, then drop the link and refuse redials.
	inv.gate.Store(true)
	pending := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, callErr := cli.Call(ctx, "remote-gpu", "qubesair.Ping", []byte("in-flight"))
		pending <- callErr
	}()
	waitFor(t, 2*time.Second, "the in-flight call to be registered", func() bool {
		return cli.inflightCount() == 1
	})

	dialer.blocked.Store(true)
	if n := dialer.dropLive(); n == 0 {
		t.Fatal("no live connection to drop")
	}

	select {
	case callErr := <-pending:
		if callErr == nil {
			t.Error("an in-flight call must fail when the tunnel drops, not complete")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight call hung across the tunnel drop")
	}

	// 3) While the link is down every call must fail fast, not hang.
	waitFor(t, 3*time.Second, "the client to observe the drop", func() bool {
		_, callErr := call(200 * time.Millisecond)
		return errors.Is(callErr, ErrNotConnected)
	})
	if n := cli.inflightCount(); n != 0 {
		t.Errorf("inflight map kept %d entries after the drop", n)
	}

	// 4) The link is back: Start must redial and serve calls again.
	dialer.blocked.Store(false)
	inv.gate.Store(false)
	waitFor(t, 5*time.Second, "the reconnect", cli.connected)
	out, err = call(2 * time.Second)
	if err != nil {
		t.Fatalf("call after reconnect failed: %v", err)
	}
	if want := "handled[remote-gpu/qubesair.Ping]:ping"; out != want {
		t.Fatalf("response after reconnect = %q, want %q", out, want)
	}
	if n := dialer.dialCount(); n <= firstDials {
		t.Errorf("dialer used %d times, want more than %d: Start must redial after a drop", n, firstDials)
	}
}

// --- (b) cancellation -----------------------------------------------------

// TestCallCancellationReturnsCtxErrorAndDrainsInflight pins the cancellation
// contract for a buffered Call: canceling the caller's context must surface
// context.Canceled promptly and must not leave the request registered as
// in-flight — that registry is exactly what a reconnect walks to fail callers.
func TestCallCancellationReturnsCtxErrorAndDrainsInflight(t *testing.T) {
	caCert, caKey := mkCA(t)
	serverTLS := mkServerTLS(t, caCert, caKey)
	clientTLS := mkClientTLS(t, caCert, caKey)

	inv := &gatedInvoker{release: make(chan struct{})}
	inv.gate.Store(true) // every forward call stays pending
	addr := startInvokerServer(t, serverTLS, inv)

	cli := NewClient(testClientConfig(addr, clientTLS), nil)
	cliCtx, cliCancel := context.WithCancel(context.Background())
	t.Cleanup(cliCancel)
	go func() { _ = cli.Start(cliCtx) }()
	waitFor(t, 5*time.Second, "the tunnel", cli.connected)

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		start := time.Now()
		go func() {
			_, callErr := cli.Call(ctx, "remote-gpu", "qubesair.Ping", []byte("blocked"))
			done <- callErr
		}()
		waitFor(t, 2*time.Second, "the call to reach the wire", func() bool {
			return cli.inflightCount() == 1
		})
		cancel()

		select {
		case callErr := <-done:
			if !errors.Is(callErr, context.Canceled) {
				t.Fatalf("iteration %d: Call error = %v, want context.Canceled", i, callErr)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("iteration %d: cancellation took %s", i, elapsed)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("iteration %d: Call did not return after context cancellation", i)
		}
		if n := cli.inflightCount(); n != 0 {
			t.Fatalf("iteration %d: %d in-flight entries survived cancellation", i, n)
		}
	}
}

// TestCallStreamCancellationReturnsAndStopsStdinPump pins cancellation for the
// streaming path: ctx cancellation ends CallStream with context.Canceled,
// releases its pending-stream registry entry, and leaves no goroutine behind
// once the caller closes the stdin it owns.
func TestCallStreamCancellationReturnsAndStopsStdinPump(t *testing.T) {
	cli := NewClient(ClientConfig{}, nil)
	cli.setStream(&fakeTunnel{})

	stdinR, stdinW := io.Pipe()
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- cli.CallStream(ctx, "remote-gpu", streamServicePrefix+"10005", stdinR, io.Discard)
	}()
	waitFor(t, 2*time.Second, "the pending stream to be registered", func() bool {
		return cli.streamCount() == 1
	})

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CallStream error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CallStream did not return after context cancellation")
	}
	if n := cli.streamCount(); n != 0 {
		t.Fatalf("%d pending streams survived cancellation", n)
	}

	// The caller owns stdin: closing it must let the stdin pump return, so the
	// canceled call leaves no goroutine running.
	_ = stdinW.Close()
	waitFor(t, 3*time.Second, "the stdin pump to exit", func() bool {
		return runtime.NumGoroutine() <= before
	})
}

// --- (c) timeout / restart -------------------------------------------------

// TestWithDefaultsReconnectBackoffBounds pins the backoff contract the reconnect
// loop is built on: unset values become the documented 20s keepalive and
// 500ms → 30s backoff, and an explicit value — including one exactly on either
// bound — is left alone.
func TestWithDefaultsReconnectBackoffBounds(t *testing.T) {
	tests := []struct {
		name     string
		in       ClientConfig
		wantKeep time.Duration
		wantMin  time.Duration
		wantMax  time.Duration
	}{
		{
			name:     "unset values get the documented defaults",
			in:       ClientConfig{},
			wantKeep: 20 * time.Second,
			wantMin:  500 * time.Millisecond,
			wantMax:  30 * time.Second,
		},
		{
			name:     "negative values count as unset",
			in:       ClientConfig{KeepAlive: -time.Second, ReconnectMin: -time.Second, ReconnectMax: -time.Second},
			wantKeep: 20 * time.Second,
			wantMin:  500 * time.Millisecond,
			wantMax:  30 * time.Second,
		},
		{
			name:     "explicit bounds are preserved",
			in:       ClientConfig{KeepAlive: time.Second, ReconnectMin: 500 * time.Millisecond, ReconnectMax: 30 * time.Second},
			wantKeep: time.Second,
			wantMin:  500 * time.Millisecond,
			wantMax:  30 * time.Second,
		},
		{
			name:     "values away from the bounds are preserved",
			in:       ClientConfig{KeepAlive: 45 * time.Second, ReconnectMin: time.Millisecond, ReconnectMax: 2 * time.Second},
			wantKeep: 45 * time.Second,
			wantMin:  time.Millisecond,
			wantMax:  2 * time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := tc.in
			got := tc.in.withDefaults()
			if got.KeepAlive != tc.wantKeep {
				t.Errorf("KeepAlive = %s, want %s", got.KeepAlive, tc.wantKeep)
			}
			if got.ReconnectMin != tc.wantMin {
				t.Errorf("ReconnectMin = %s, want %s", got.ReconnectMin, tc.wantMin)
			}
			if got.ReconnectMax != tc.wantMax {
				t.Errorf("ReconnectMax = %s, want %s", got.ReconnectMax, tc.wantMax)
			}
			// withDefaults takes a value receiver: the caller's config must not
			// be filled in under it.
			if tc.in.KeepAlive != original.KeepAlive ||
				tc.in.ReconnectMin != original.ReconnectMin ||
				tc.in.ReconnectMax != original.ReconnectMax {
				t.Errorf("withDefaults mutated the caller's config: %+v, want %+v", tc.in, original)
			}
		})
	}
}

// TestJitterStaysInsideBackoffEnvelope pins jitter's bounds at both ends of the
// reconnect range: it may never retry sooner than the backoff floor it was
// given (a reconnect storm) and never more than 10% later (the documented
// "±20% to avoid thundering-herd reconnects" formula, whose realized spread is
// [d, 1.1d)), and a non-positive duration stays 0.
func TestJitterStaysInsideBackoffEnvelope(t *testing.T) {
	if got := jitter(0); got != 0 {
		t.Errorf("jitter(0) = %s, want 0", got)
	}
	if got := jitter(-time.Second); got != 0 {
		t.Errorf("jitter(-1s) = %s, want 0", got)
	}

	for _, d := range []time.Duration{500 * time.Millisecond, 30 * time.Second} {
		sawSpread := false
		for i := 0; i < 2000; i++ {
			got := jitter(d)
			if got < d {
				t.Fatalf("jitter(%s) = %s, below the backoff floor", d, got)
			}
			if got > d+d/10 {
				t.Fatalf("jitter(%s) = %s, above the backoff ceiling %s", d, got, d+d/10)
			}
			if got > d {
				sawSpread = true
			}
		}
		if !sawSpread {
			t.Errorf("jitter(%s) never spread over 2000 draws: it is not jittering", d)
		}
	}
}

// TestTLSProviderRefetchedOnReconnect pins the rotation seam: the provider must
// be consulted on every connection attempt, including the one that restores the
// tunnel, so a relay cert rotated in the vault takes effect on the next
// reconnect without a restart (client.go runOnce → resolveTLS).
func TestTLSProviderRefetchedOnReconnect(t *testing.T) {
	caCert, caKey := mkCA(t)
	serverTLS := mkServerTLS(t, caCert, caKey)

	addr := startInvokerServer(t, serverTLS, tagInvoker{})

	dialer := &dropDialer{}
	var provCalls atomic.Int32
	cfg := testClientConfig(addr, nil)
	cfg.Dialer = dialer.dial
	cfg.TLSProvider = func() (*tls.Config, error) {
		provCalls.Add(1)
		return mkClientTLS(t, caCert, caKey), nil
	}

	cli := NewClient(cfg, nil)
	cliCtx, cliCancel := context.WithCancel(context.Background())
	t.Cleanup(cliCancel)
	go func() { _ = cli.Start(cliCtx) }()

	waitFor(t, 5*time.Second, "the first tunnel", cli.connected)
	first := provCalls.Load()
	if first == 0 {
		t.Fatal("TLSProvider was not consulted for the first connection")
	}

	// Drop the link and refuse redials: the client must still resolve TLS on
	// every attempt it makes while reconnecting.
	dialer.blocked.Store(true)
	if n := dialer.dropLive(); n == 0 {
		t.Fatal("no live connection to drop")
	}
	waitFor(t, 3*time.Second, "the client to observe the drop", func() bool {
		return !cli.connected()
	})
	// Start backs off before redialing, so the next provider call lands a
	// backoff after the drop. Every dial in this window is refused, so a
	// provider call observed here can only come from a reconnect attempt.
	waitFor(t, 5*time.Second, "TLSProvider to be consulted while reconnecting", func() bool {
		return provCalls.Load() > first
	})

	// Restore the link. The tunnel must come back, and every attempt behind it
	// — including the one that succeeds — must have resolved TLS afresh, which
	// is what makes a rotated relay cert take effect on reconnect. Asserted
	// against the first connection's count, not a mid-attempt snapshot: a
	// single attempt may resolve once and then complete after the link returns.
	dialer.blocked.Store(false)
	waitFor(t, 5*time.Second, "the reconnect", cli.connected)
	if after := provCalls.Load(); after <= first {
		t.Fatalf("TLSProvider calls stayed at %d across the reconnect, want a fresh fetch per connect", after)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := cli.Call(ctx, "remote-gpu", "qubesair.Ping", []byte("ping"))
	if err != nil {
		t.Fatalf("call over the reconnected tunnel failed: %v", err)
	}
	if want := "handled[remote-gpu/qubesair.Ping]:ping"; string(out) != want {
		t.Fatalf("response after reconnect = %q, want %q", out, want)
	}
}
