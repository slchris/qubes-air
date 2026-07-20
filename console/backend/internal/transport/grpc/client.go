// client.go — LOCAL sys-relay side. Dials OUTBOUND to the remote Remote-Relay,
// keeps one long-lived bidi Tunnel, multiplexes qrexec calls by request_id,
// reconnects on drop. Implements transport.Transport.
//
// SECURITY: the transport only moves frames; authorization always lives in the
// two dom0s. A forward Call has already passed local dom0 policy A/B before it
// reaches here. A reverse (REMOTE_TO_LOCAL) request is routed to c.reverse,
// which hands it to the local dom0 (policy C: ask) — this file never decides
// authorization, it only carries frames.

package grpc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slchris/qubes-air/console/internal/transport"
	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ClientConfig configures the outbound relay client.
type ClientConfig struct {
	RemoteEndpoint string        // host:port of the remote Remote-Relay
	RelayName      string        // local relay identity (sys-relay-<zone>)
	RemoteName     string        // target remote_name (RemoteVM attr)
	KeepAlive      time.Duration // heartbeat interval
	ReconnectMin   time.Duration // backoff floor
	ReconnectMax   time.Duration // backoff ceiling
	TLS            *tls.Config   // mTLS: client cert (from vault), CA, server verify
	// TLSProvider, if set, is called on EACH (re)connect to obtain the current
	// mTLS config. This is how certificate ROTATION takes effect without a
	// restart: after vault rotates the relay cert, the next reconnect fetches the
	// new one. When nil, the static TLS above is used on every connect.
	TLSProvider func() (*tls.Config, error)
	// Dialer, if set, replaces the default TCP dial with a caller-supplied one.
	//
	// This is the hook for reaching a remote that has no route to it — GCP IAP
	// or AWS SSM forwarding open a per-connection tunnel to a private instance
	// with no inbound rule (see service/agentdial.go). Those are not addresses
	// anything can dial, so RemoteEndpoint stays a label and this does the work.
	//
	// Nil keeps gRPC's own dialing, byte for byte: the option below is only
	// applied when this is set, so the routed path is untouched by the seam.
	Dialer func(ctx context.Context, addr string) (net.Conn, error)
}

// withDefaults returns a copy of cfg with sane defaults filled in.
func (cfg ClientConfig) withDefaults() ClientConfig {
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = 20 * time.Second
	}
	if cfg.ReconnectMin <= 0 {
		cfg.ReconnectMin = 500 * time.Millisecond
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = 30 * time.Second
	}
	return cfg
}

// Client is the local relay's outbound gRPC transport. It maintains one Tunnel
// and satisfies transport.Transport.
type Client struct {
	cfg     ClientConfig
	reverse transport.ReverseHandler // handles REMOTE_TO_LOCAL frames → local dom0 (policy C: ask)

	mu       sync.Mutex
	inflight map[string]*pendingCall        // request_id → waiter (forward calls we originated)
	stream   pb.RelayTransport_TunnelClient // the live bidi stream (nil when disconnected)
	sendMu   sync.Mutex                     // serializes Send: gRPC streams forbid concurrent Send
}

// pendingCall accumulates a forward call's response until EOS/error.
type pendingCall struct {
	buf  []byte
	done chan callResult
}

type callResult struct {
	out []byte
	err error
}

// compile-time check: *Client satisfies transport.Transport.
var _ transport.Transport = (*Client)(nil)

// ErrNotConnected is returned by Call when there is no live tunnel.
var ErrNotConnected = errors.New("transport/grpc: tunnel not connected")

// NewClient builds the client. reverse routes reverse calls to the local dom0;
// it must never decide authorization itself.
func NewClient(cfg ClientConfig, reverse transport.ReverseHandler) *Client {
	return &Client{
		cfg:      cfg.withDefaults(),
		reverse:  reverse,
		inflight: make(map[string]*pendingCall),
	}
}

// Start dials outbound and keeps the Tunnel alive (reconnect + keepalive) until
// ctx is canceled. Blocks; run it in its own goroutine. It returns ctx.Err()
// when the context is canceled.
func (c *Client) Start(ctx context.Context) error {
	backoff := c.cfg.ReconnectMin
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// runOnce returned due to a connection error; back off and redial.
		_ = err // connection errors are transient; loop and reconnect.

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter(backoff)):
		}
		backoff *= 2
		if backoff > c.cfg.ReconnectMax {
			backoff = c.cfg.ReconnectMax
		}
	}
}

// runOnce dials, opens the Tunnel, sends the handshake, and pumps frames until
// the connection drops or ctx is canceled. It always tears down inflight calls
// before returning so no caller is left hanging.
// resolveTLS returns the mTLS config for a connection attempt: the TLSProvider
// (fresh, for rotation) if set, else the static TLS. mTLS is mandatory — a nil
// result is an error rather than an insecure dial.
func (c *Client) resolveTLS() (*tls.Config, error) {
	if c.cfg.TLSProvider != nil {
		t, err := c.cfg.TLSProvider()
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, errors.New("transport/grpc: TLSProvider returned nil config")
		}
		return t, nil
	}
	if c.cfg.TLS == nil {
		return nil, errors.New("transport/grpc: nil TLS config (mTLS required)")
	}
	return c.cfg.TLS, nil
}

func (c *Client) runOnce(ctx context.Context) error {
	// Resolve mTLS for THIS connection. With a TLSProvider, each reconnect picks
	// up the current cert — this is how rotation takes effect without a restart.
	tlsCfg, err := c.resolveTLS()
	if err != nil {
		return fmt.Errorf("resolve mTLS: %w", err)
	}
	creds := credentials.NewTLS(tlsCfg)
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	if c.cfg.Dialer != nil {
		opts = append(opts, grpc.WithContextDialer(c.cfg.Dialer))
	}
	conn, err := grpc.NewClient(c.cfg.RemoteEndpoint, opts...)
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.cfg.RemoteEndpoint, err)
	}
	defer conn.Close()

	// Scope the stream to a child context so we can cancel recv/keepalive on exit.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := pb.NewRelayTransportClient(conn).Tunnel(streamCtx)
	if err != nil {
		return fmt.Errorf("open tunnel: %w", err)
	}

	// Publish the live stream; clear it (and fail inflight) on the way out.
	c.setStream(stream)
	defer c.clearStream(ErrNotConnected)

	if err := c.send(handshakeFrame(c.cfg.RelayName, c.cfg.RemoteName)); err != nil {
		return fmt.Errorf("send handshake: %w", err)
	}

	// Keepalive ticker runs alongside recvLoop; both stop when streamCtx is done.
	go c.keepAliveLoop(streamCtx)

	// recvLoop blocks until the stream errors (drop) or ctx cancels.
	return c.recvLoop(streamCtx, stream)
}

func (c *Client) keepAliveLoop(ctx context.Context) {
	t := time.NewTicker(c.cfg.KeepAlive)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.send(keepAliveFrame(time.Now().UnixMilli())); err != nil {
				return // stream is dead; recvLoop will observe it too.
			}
		}
	}
}

// Call implements transport.Transport: send a forward (LOCAL_TO_REMOTE) qrexec
// call over the Tunnel and wait for the response. The call has ALREADY passed
// local dom0 policy before reaching here.
func (c *Client) Call(ctx context.Context, target, service string, in []byte) ([]byte, error) {
	if !transport.ValidName(target) || !transport.ValidName(service) {
		return nil, transport.ErrInvalidName
	}

	reqID := uuid.NewString()
	pc := &pendingCall{done: make(chan callResult, 1)}

	c.mu.Lock()
	if c.stream == nil {
		c.mu.Unlock()
		return nil, ErrNotConnected
	}
	c.inflight[reqID] = pc
	c.mu.Unlock()

	// Always clean up our inflight entry when we leave.
	defer func() {
		c.mu.Lock()
		delete(c.inflight, reqID)
		c.mu.Unlock()
	}()

	var deadlineMs int64
	if dl, ok := ctx.Deadline(); ok {
		deadlineMs = dl.UnixMilli()
	}

	// Header + request body + EOS(request). Sends are serialized by c.send.
	if err := c.send(requestHeaderFrame(reqID, pb.Direction_LOCAL_TO_REMOTE, service, c.cfg.RelayName, target, deadlineMs)); err != nil {
		return nil, fmt.Errorf("send header: %w", err)
	}
	if len(in) > 0 {
		if err := c.send(dataFrame(reqID, streamRequest, in)); err != nil {
			return nil, fmt.Errorf("send body: %w", err)
		}
	}
	if err := c.send(eosFrame(reqID, streamRequest)); err != nil {
		return nil, fmt.Errorf("send eos: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-pc.done:
		return res.out, res.err
	}
}

// recvLoop reads frames and dispatches by request_id: forward responses wake
// the inflight waiter; REMOTE_TO_LOCAL requests go to c.reverse (→ local dom0).
// It returns when the stream errors (drop) or ctx is canceled.
// A dispatch over frame types. Flat by nature; each arm is independent.
//
//nolint:gocyclo // frame-type dispatch
func (c *Client) recvLoop(ctx context.Context, stream pb.RelayTransport_TunnelClient) error {
	// reverse accumulates inbound reverse-request bodies by request_id.
	reverseBuf := make(map[string]*reverseCall)

	for {
		frame, err := stream.Recv()
		if err != nil {
			return err // connection dropped; Start will reconnect.
		}
		reqID := frame.GetRequestId()

		switch {
		case frame.GetHandshake() != nil:
			// Server ack; nothing to do.

		case frame.GetKeepAlive() != nil:
			// Heartbeat; liveness only.

		case frame.GetRequestHeader() != nil:
			hdr := frame.GetRequestHeader()
			if hdr.GetDirection() == pb.Direction_REMOTE_TO_LOCAL {
				// A reverse call originated remotely: start accumulating its body.
				reverseBuf[reqID] = &reverseCall{service: hdr.GetQrexecService()}
			}
			// LOCAL_TO_REMOTE headers echoed back are unexpected on the client; ignore.

		case frame.GetData() != nil:
			d := frame.GetData()
			switch d.GetStreamId() {
			case streamResponse:
				// Response body for a forward call we originated.
				c.appendForward(reqID, d.GetPayload())
			case streamRequest:
				// Body of an inbound reverse request.
				if rc, ok := reverseBuf[reqID]; ok {
					rc.buf = append(rc.buf, d.GetPayload()...)
				}
			}

		case frame.GetEos() != nil:
			eos := frame.GetEos()
			switch eos.GetStreamId() {
			case streamResponse:
				// Forward call complete.
				c.completeForward(reqID, nil)
			case streamRequest:
				// Inbound reverse request fully received → route to local dom0.
				if rc, ok := reverseBuf[reqID]; ok {
					delete(reverseBuf, reqID)
					go c.handleReverse(ctx, reqID, rc)
				}
			}

		case frame.GetError() != nil:
			ce := frame.GetError()
			// Error can terminate either a forward call or an in-progress reverse.
			delete(reverseBuf, reqID)
			c.completeForward(reqID, fmt.Errorf("remote: %s: %s", ce.GetCode(), ce.GetMessage()))
		}
	}
}

// reverseCall accumulates an inbound reverse (remote → local) request.
type reverseCall struct {
	service string
	buf     []byte
}

// handleReverse routes a completed inbound reverse request to the local dom0 via
// c.reverse (policy C: ask), then streams the result back on the same request_id.
// It NEVER decides authorization — c.reverse does that through dom0.
func (c *Client) handleReverse(ctx context.Context, reqID string, rc *reverseCall) {
	if c.reverse == nil {
		_ = c.send(errorFrame(reqID, codeInternal, "no reverse handler"))
		return
	}
	out, err := c.reverse(ctx, rc.service, rc.buf)
	if err != nil {
		_ = c.send(errorFrame(reqID, codeInternal, err.Error()))
		return
	}
	if len(out) > 0 {
		if err := c.send(dataFrame(reqID, streamResponse, out)); err != nil {
			return
		}
	}
	_ = c.send(eosFrame(reqID, streamResponse))
}

// appendForward buffers a response chunk for a forward call.
func (c *Client) appendForward(reqID string, payload []byte) {
	c.mu.Lock()
	if pc := c.inflight[reqID]; pc != nil {
		pc.buf = append(pc.buf, payload...)
	}
	c.mu.Unlock()
}

// completeForward delivers the final result (or error) to a forward-call waiter.
func (c *Client) completeForward(reqID string, err error) {
	c.mu.Lock()
	pc := c.inflight[reqID]
	if pc != nil {
		delete(c.inflight, reqID)
	}
	c.mu.Unlock()
	if pc == nil {
		return
	}
	if err != nil {
		pc.done <- callResult{err: err}
	} else {
		pc.done <- callResult{out: pc.buf}
	}
}

// setStream publishes the live stream.
func (c *Client) setStream(stream pb.RelayTransport_TunnelClient) {
	c.mu.Lock()
	c.stream = stream
	c.mu.Unlock()
}

// clearStream drops the live stream and fails every inflight forward call so no
// caller hangs across a reconnect.
func (c *Client) clearStream(cause error) {
	c.mu.Lock()
	c.stream = nil
	pending := c.inflight
	c.inflight = make(map[string]*pendingCall)
	c.mu.Unlock()
	for _, pc := range pending {
		pc.done <- callResult{err: fmt.Errorf("%s: %w", codeUnavailable, cause)}
	}
}

// send serializes writes to the live stream (gRPC forbids concurrent Send).
func (c *Client) send(frame *pb.Frame) error {
	c.mu.Lock()
	stream := c.stream
	c.mu.Unlock()
	if stream == nil {
		return ErrNotConnected
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return stream.Send(frame)
}

// jitter returns d ± up to 20% to avoid thundering-herd reconnects.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	delta := time.Duration(rand.Int63n(int64(d) / 5)) //nolint:gosec // reconnect jitter, not security-sensitive
	return d - (delta / 2) + delta
}
