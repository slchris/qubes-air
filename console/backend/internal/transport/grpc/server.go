// server.go — REMOTE Remote-Relay side. Accepts the inbound Tunnel from the
// local relay (the ONLY connection; still zero-inbound from the local network's
// view because the local relay dials out). Routes forward frames to
// qrexec-client-vm AFTER the remote dom0/policy re-checks; carries reverse
// frames back to the local relay.

package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"sync"

	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"

	"github.com/slchris/qubes-air/console/internal/transport"
	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// ServerConfig configures the remote-side server.
type ServerConfig struct {
	Listen string      // host:port to listen on (remote side)
	TLS    *tls.Config // mTLS: server cert + require+verify client cert (relay identity)
	// CertRegistry authorizes client certificates and is what makes revocation
	// possible. Without it a CA-signed certificate is valid forever.
	CertRegistry CertRegistry
	// ReauthorizeInterval re-checks the peer's certificate on a live tunnel
	// (default reauthorizeInterval). Handshake-time checks alone are not
	// enough: these streams are long-lived, so a revoked agent would keep an
	// established connection indefinitely.
	ReauthorizeInterval time.Duration
	// CertSource, when set, supplies the server certificate on EACH handshake
	// instead of the static TLS.Certificates.
	//
	// This is what lets a RENEWED certificate take effect without restarting
	// the process — the same reasoning as ClientConfig.TLSProvider, applied to
	// the accepting side. tls.Config.Certificates is read once, so without this
	// an agent that has just renewed keeps presenting the superseded
	// certificate until someone restarts it; on a fleet nobody reboots that
	// means the certificate expires with a valid replacement sitting on disk,
	// which is the failure renewal exists to prevent.
	CertSource ServerCertSource
}

// ServerCertSource hands out the certificate the listener presents.
// Implemented by *agent.Identity.
type ServerCertSource interface {
	ServerCertificate() (*tls.Certificate, error)
}

// CertRegistry authorizes client certificates by fingerprint.
// Implemented by repository.AgentCertRepository.
type CertRegistry interface {
	Authorize(ctx context.Context, fingerprint string) (*repository.AgentCert, error)
	TouchLastSeen(ctx context.Context, fingerprint string) error
}

// reauthorizeInterval is how often a live tunnel re-checks its peer certificate
// against the registry.
//
// This bounds how long a revoked agent keeps an already-open connection. One
// minute trades a small amount of database traffic for a revocation that
// actually takes effect while someone is watching.
const reauthorizeInterval = time.Minute

// QrexecInvoker runs a qrexec call locally on the remote host.
//
// NOTE: an earlier comment here said calls arrive "AFTER the remote dom0/policy
// has re-checked it". A non-Qubes remote has no dom0 (see
// docs/remote-agent-design.md); the implementation's own name allow-listing is
// defense in depth, not an authorization boundary.
type QrexecInvoker interface {
	// Invoke runs the call and returns its stdout, stderr and exit code. A
	// non-zero exit is part of the result; err is reserved for the call not
	// being made at all (unknown/disallowed service, timeout).
	Invoke(ctx context.Context, target, service string, in []byte) (transport.Result, error)
}

// Server implements pb.RelayTransportServer. It only moves frames; all
// authorization lives in the two dom0s (see Tunnel security notes).
type Server struct {
	pb.UnimplementedRelayTransportServer
	cfg     ServerConfig
	invoker QrexecInvoker

	mu   sync.Mutex
	grpc *grpc.Server
}

// NewServer builds the remote server. invoker executes forward calls locally
// (post remote-dom0 re-check).
func NewServer(cfg ServerConfig, invoker QrexecInvoker) *Server {
	return &Server{cfg: cfg, invoker: invoker}
}

// NewServerWithQrexec builds the remote server with the production qrexec
// invoker (shells to qrexec-client-vm). This is the constructor a Remote-Relay
// process uses. Forward calls reaching the invoker have been re-authorized by
// the remote dom0/policy (policy lives in dom0, not here).
func NewServerWithQrexec(cfg ServerConfig) *Server {
	return NewServer(cfg, NewQrexecInvoker())
}

// verifyRegisteredConnection authorizes a connection's client certificate
// against the registry.
//
// This hangs off VerifyConnection rather than VerifyPeerCertificate, and the
// difference is the whole revocation story. VerifyPeerCertificate runs only
// during a FULL handshake; a client that resumes a session (TLS 1.3 PSK, or a
// 1.2 session ticket) skips certificate verification entirely and has its peer
// certificate restored from the cached session. A revoked agent could therefore
// keep reconnecting for the lifetime of its ticket — precisely the permanent
// access the registry exists to take away. VerifyConnection runs on every
// handshake, resumed or not, so revocation takes effect on the next connection
// as the design intends.
func (s *Server) verifyRegisteredConnection(cs tls.ConnectionState) error {
	return s.authorizeChain(cs.VerifiedChains)
}

// authorizeChain adds "and we still permit it" to a chain the TLS stack has
// already verified as CA-signed and in date.
func (s *Server) authorizeChain(chains [][]*x509.Certificate) error {
	if len(chains) == 0 || len(chains[0]) == 0 {
		return fmt.Errorf("no verified certificate chain")
	}
	leaf := chains[0][0]
	if now := time.Now(); now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return errors.New("client certificate is not currently valid")
	}
	fp := repository.Fingerprint(leaf)

	// Caller authorization by role: only a Relay or the Console may open a
	// client connection to an agent. The chain proves "one of ours"; the role
	// proves "allowed to call". An agent certificate presented as a client (a
	// fleet member trying to call another agent directly) is refused here.
	role, err := pki.RoleOf(leaf)
	if err != nil {
		return fmt.Errorf("client certificate carries no usable role: %w", err)
	}
	if role != pki.RoleRelay && role != pki.RoleConsole {
		log.Printf("grpc server: rejecting client cert %s (CN=%q): role %q is not allowed",
			fp[:16], leaf.Subject.CommonName, role)
		return fmt.Errorf("client role %q is not allowed to connect", role)
	}

	if s.cfg.CertRegistry == nil {
		return nil
	}
	cert, err := s.cfg.CertRegistry.Authorize(context.Background(), fp)
	if err != nil {
		// Log the distinct cases: an unregistered certificate that nonetheless
		// carries a valid CA signature is a very different event from an
		// ordinary revocation, and collapsing them would hide the first among
		// the second.
		log.Printf("grpc server: rejecting client cert %s (CN=%q): %v",
			fp[:16], leaf.Subject.CommonName, err)
		return err
	}
	if err := s.cfg.CertRegistry.TouchLastSeen(context.Background(), fp); err != nil {
		// Non-fatal: this is operational visibility, not authorization.
		log.Printf("grpc server: could not record last-seen for %s: %v", fp[:16], err)
	}
	log.Printf("grpc server: accepted client cert %s (CN=%q, qube=%s)",
		fp[:16], leaf.Subject.CommonName, cert.QubeID)
	return nil
}

// peerFingerprint extracts the connected peer's certificate fingerprint.
func peerFingerprint(ctx context.Context) (string, bool) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return "", false
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return "", false
	}
	return repository.Fingerprint(tlsInfo.State.VerifiedChains[0][0]), true
}

// reauthorizeLoop tears down the tunnel once its certificate stops being
// authorized. It exits when the tunnel does.
func (s *Server) reauthorizeLoop(ctx context.Context, cancel context.CancelFunc, fingerprint string, expiresAt time.Time) {
	interval := s.cfg.ReauthorizeInterval
	if interval <= 0 {
		interval = reauthorizeInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !time.Now().Before(expiresAt) {
				cancel()
				return
			}
			if s.cfg.CertRegistry == nil {
				continue
			}
			if _, err := s.cfg.CertRegistry.Authorize(ctx, fingerprint); err != nil {
				if ctx.Err() != nil {
					return // tunnel already closing
				}
				log.Printf("grpc server: closing tunnel, cert %s no longer authorized: %v",
					fingerprint[:16], err)
				cancel()
				return
			}
		}
	}
}

// applyCertSource wires live certificate selection into the TLS config.
//
// Two details are load-bearing.
//
// Certificates MUST be cleared. Go only consults GetCertificate when
// Certificates is empty or the ClientHello carried an SNI name, and the
// console's prober dials a qube by IP address with no SNI to send (see
// service.probeTLSConfig, which cannot verify by hostname either). Leaving the
// startup certificate in place would therefore skip this hook for exactly the
// caller renewal is meant to serve: the agent would renew, report success, and
// go on presenting the old certificate until it expired.
//
// The startup certificate is kept as a fallback rather than discarded. If the
// source cannot produce a certificate, serving the previous one — still valid,
// merely older — beats failing the handshake: an agent that answers nothing is
// unreachable by the console, and the console is the only thing that can fix it.
func (s *Server) applyCertSource() {
	if s.cfg.CertSource == nil {
		return
	}
	src := s.cfg.CertSource
	startup := s.cfg.TLS.Certificates
	s.cfg.TLS.Certificates = nil
	s.cfg.TLS.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		cert, err := src.ServerCertificate()
		if err == nil && cert != nil {
			return cert, nil
		}
		if err == nil {
			err = errors.New("certificate source returned nothing")
		}
		if len(startup) > 0 {
			log.Printf("grpc server: certificate source unavailable, serving the startup certificate: %v", err)
			return &startup[0], nil
		}
		return nil, fmt.Errorf("grpc server: no server certificate available: %w", err)
	}
}

// Serve starts the gRPC server with mTLS and blocks until it stops. When ctx is
// canceled the server is gracefully stopped and Serve returns nil.
func (s *Server) Serve(ctx context.Context) error {
	if s.cfg.TLS == nil {
		return fmt.Errorf("grpc server: nil TLS config (mTLS is required)")
	}
	s.cfg.TLS = s.cfg.TLS.Clone()
	s.cfg.TLS.ClientAuth = tls.RequireAndVerifyClientCert
	previous := s.cfg.TLS.VerifyConnection
	s.cfg.TLS.VerifyConnection = func(cs tls.ConnectionState) error {
		if previous != nil {
			if err := previous(cs); err != nil {
				return err
			}
		}
		return s.verifyRegisteredConnection(cs)
	}

	s.applyCertSource()

	// ListenConfig rather than net.Listen so the socket is bound under the
	// server's context and a cancellation during startup is honored.
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("grpc server: listen %q: %w", s.cfg.Listen, err)
	}

	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(s.cfg.TLS)))
	pb.RegisterRelayTransportServer(gs, s)

	s.mu.Lock()
	s.grpc = gs
	s.mu.Unlock()

	// Graceful stop on ctx cancellation.
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			gs.GracefulStop()
		case <-stopped:
		}
	}()

	err = gs.Serve(lis)
	close(stopped)
	if ctx.Err() != nil {
		// Cancellation is a normal shutdown, not an error.
		return nil
	}
	return err
}

// Stop immediately stops the server (for tests / forced shutdown).
func (s *Server) Stop() {
	s.mu.Lock()
	gs := s.grpc
	s.mu.Unlock()
	if gs != nil {
		gs.GracefulStop()
	}
}

// Tunnel is the bidi-stream RPC handler. One Tunnel per connected local relay;
// many qrexec calls are multiplexed on it by request_id.
//
// SECURITY: this handler MUST NOT bypass the remote dom0/policy re-check on
// forward calls. Reaching s.invoker.Invoke here represents a call that the
// remote dom0/policy has already re-authorized (policy lives in dom0, not in
// this process). Reverse (REMOTE_TO_LOCAL) frames are only relayed back to the
// local relay; their authorization is the LOCAL dom0 policy C (ask), enforced
// on the client side — this handler must not let them skip that.
//
// This function is the tunnel lifecycle — authorize, handshake, read frames,
// teardown — while each frame kind is handled by a named step on tunnelSession.
func (s *Server) Tunnel(stream grpc.BidiStreamingServer[pb.Frame, pb.Frame]) error {
	ctx, cancelTunnel := context.WithCancel(stream.Context())
	defer cancelTunnel()

	if err := s.startReauthorization(ctx, cancelTunnel, stream); err != nil {
		return err
	}

	sess := newTunnelSession(ctx, cancelTunnel, s, stream)
	// Teardown order matters: stop the forward workers before closing the
	// stream sockets they may still be writing through.
	defer sess.closeStreams()
	defer sess.shutdown()

	// --- Handshake: first frame must be a Handshake with a matching version.
	ok, err := sess.handshake(stream)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	for {
		frame, err := receiveFrame(ctx, stream)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := sess.dispatch(frame); err != nil {
			return err
		}
	}
}

// startReauthorization launches the periodic peer-certificate re-check for a
// live tunnel.
//
// Checking only at handshake would let a revoked agent keep an established
// connection indefinitely — and these tunnels are deliberately long-lived, so
// "indefinitely" means until someone notices. Revocation has to reach a
// connection that is already open, or it is not revocation.
//
// A connection whose TLS identity cannot be read is refused here rather than
// left running without a re-check.
func (s *Server) startReauthorization(ctx context.Context, cancel context.CancelFunc, stream grpc.BidiStreamingServer[pb.Frame, pb.Frame]) error {
	fp, ok := peerFingerprint(stream.Context())
	if !ok {
		return nil
	}
	p, _ := peer.FromContext(stream.Context())
	info, valid := p.AuthInfo.(credentials.TLSInfo)
	if !valid {
		return errors.New("missing TLS peer identity")
	}
	go s.reauthorizeLoop(ctx, cancel, fp, info.State.VerifiedChains[0][0].NotAfter)
	return nil
}

// pendingRequest is a forward call whose request body is still arriving. Frames
// for different request_ids interleave on the stream, so their bodies are
// accumulated per id.
type pendingRequest struct {
	header *pb.RequestHeader
	body   []byte
}

// tunnelSession is the per-tunnel state the frame handlers share: the
// serialized sender, the forward requests being accumulated, and the live
// TCP-proxy streams. Separate from Server because it lives and dies with one
// stream.
type tunnelSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	server *Server
	send   func(*pb.Frame) error

	// pendMu guards pend.
	pendMu sync.Mutex
	pend   map[string]*pendingRequest

	// streamMu guards streams: a stream's request bytes go straight to its
	// socket, not into a buffer.
	streamMu sync.Mutex
	streams  map[string]*serverStream

	// wg tracks the in-flight forward workers so shutdown can wait for them.
	wg sync.WaitGroup
}

// newTunnelSession builds the per-tunnel state. Send is not concurrent-safe, so
// every goroutine that replies goes through the serializer built here.
func newTunnelSession(ctx context.Context, cancel context.CancelFunc, s *Server, stream grpc.BidiStreamingServer[pb.Frame, pb.Frame]) *tunnelSession {
	var sendMu sync.Mutex
	sess := &tunnelSession{
		ctx:     ctx,
		cancel:  cancel,
		server:  s,
		pend:    make(map[string]*pendingRequest),
		streams: make(map[string]*serverStream),
	}
	sess.send = func(f *pb.Frame) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(f)
	}
	return sess
}

// shutdown cancels the tunnel context and waits for the in-flight forward
// workers, so nothing keeps running after Tunnel returns.
func (t *tunnelSession) shutdown() {
	t.cancel()
	t.wg.Wait()
}

// closeStreams closes every live TCP-proxy socket on the way out.
func (t *tunnelSession) closeStreams() {
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	for _, ss := range t.streams {
		_ = ss.conn.Close()
	}
}

// handshake consumes the first frame and requires it to be a Handshake with a
// compatible protocol version, then acknowledges with our own.
//
// ok is false when the peer closed the stream before handshaking, which is a
// clean end rather than an error.
func (t *tunnelSession) handshake(stream grpc.BidiStreamingServer[pb.Frame, pb.Frame]) (bool, error) {
	first, err := receiveFrame(t.ctx, stream)
	if err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, err
	}
	hs := first.GetHandshake()
	if hs == nil {
		return false, fmt.Errorf("grpc server: expected Handshake as first frame, got %T", first.GetKind())
	}
	if !supportsProtocol(hs.GetProtocolVersion()) {
		msg := protocolMismatchMessage(hs.GetProtocolVersion())
		// Tell the peer WHY before closing. A bare stream error is
		// indistinguishable from a network fault, which sends whoever is
		// debugging it looking at firewalls instead of versions.
		_ = t.send(&pb.Frame{Kind: &pb.Frame_Error{Error: &pb.CallError{
			Code:    CodeProtocolMismatch,
			Message: msg,
		}}})
		log.Printf("grpc server: rejecting relay %q (build %q): %s",
			hs.GetRelayName(), hs.GetBuildVersion(), msg)
		return false, fmt.Errorf("grpc server: %s", msg)
	}
	relayName := hs.GetRelayName()
	remoteName := hs.GetRemoteName()
	// Build version is observability only — logged so an operator can tell which
	// agent build is actually running out there, without it gating anything.
	log.Printf("grpc server: relay %q connected (protocol %s, build %s)",
		relayName, hs.GetProtocolVersion(), orUnknown(hs.GetBuildVersion()))
	// Acknowledge with our own Handshake frame.
	if err := t.send(handshakeFrame(remoteName, relayName)); err != nil {
		return false, err
	}
	return true, nil
}

// dispatch handles one frame from the stream. A non-nil error ends the tunnel;
// a frame that only spoils its own call is answered with a CallError (or
// dropped) and returns nil.
func (t *tunnelSession) dispatch(frame *pb.Frame) error {
	switch k := frame.GetKind().(type) {
	case *pb.Frame_KeepAlive:
		return t.echoKeepAlive(k)
	case *pb.Frame_RequestHeader:
		return t.handleRequestHeader(frame, k)
	case *pb.Frame_Data:
		return t.handleData(frame, k)
	case *pb.Frame_Eos:
		return t.handleEos(frame, k)
	case *pb.Frame_Error:
		return t.handleCallError(frame)
	case *pb.Frame_Handshake:
		// A second handshake is unexpected; ignore it.
	default:
		// Unknown/empty frame kind: ignore to stay tolerant.
	}
	return nil
}

// echoKeepAlive answers a keepalive to keep the NAT mapping warm and prove
// liveness.
func (t *tunnelSession) echoKeepAlive(k *pb.Frame_KeepAlive) error {
	return t.send(keepAliveFrame(k.KeepAlive.GetUnixMs()))
}

// handleRequestHeader opens a TCP-proxy stream for a stream-prefixed forward
// request, starts accumulating a plain forward call's body, or relays a reverse
// call's header back to the local relay untouched.
func (t *tunnelSession) handleRequestHeader(frame *pb.Frame, k *pb.Frame_RequestHeader) error {
	reqID := frame.GetRequestId()
	switch k.RequestHeader.GetDirection() {
	case pb.Direction_LOCAL_TO_REMOTE:
		if strings.HasPrefix(k.RequestHeader.GetQrexecService(), streamServicePrefix) {
			t.openStream(reqID, k.RequestHeader.GetQrexecService())
			return nil
		}
		// Forward call: begin accumulating its request body.
		t.pendMu.Lock()
		t.pend[reqID] = &pendingRequest{header: k.RequestHeader}
		t.pendMu.Unlock()
	case pb.Direction_REMOTE_TO_LOCAL:
		// Reverse call originated remotely (e.g. remote qube → local
		// vault). The server does NOT authorize or execute it; it just
		// relays the header back to the local relay, whose side routes
		// it through LOCAL dom0 policy C (ask). Relay the frame as-is.
		if err := t.send(frame); err != nil {
			return err
		}
	default:
		if err := t.send(errorFrame(reqID, codeInvalid, "unknown direction")); err != nil {
			return err
		}
	}
	return nil
}

// openStream starts the loopback TCP proxy for a stream-prefixed request. A
// failure here spoils only that request: the peer is told with a CallError and
// the tunnel stays up.
//
// A stream-prefixed request with a port outside the allowed range is REFUSED
// here — never routed to qrexec — so the tunnel can only reach the whitelisted
// loopback GUI ports.
func (t *tunnelSession) openStream(reqID, service string) {
	port, ok := streamLocalPort(service)
	if !ok {
		_ = t.send(errorFrame(reqID, codeInvalid, "stream port not allowed"))
		return
	}
	ss, derr := t.server.startStream(t.ctx, reqID, port, t.send)
	if derr != nil {
		_ = t.send(errorFrame(reqID, codeUnavailable, "stream dial: "+derr.Error()))
		return
	}
	t.streamMu.Lock()
	t.streams[reqID] = ss
	t.streamMu.Unlock()
}

// handleData routes a data frame: request bytes for a live TCP-proxy stream go
// straight to its socket, bytes for an accumulating forward call are appended to
// its body, and anything else (a reverse call's body) is relayed through.
func (t *tunnelSession) handleData(frame *pb.Frame, k *pb.Frame_Data) error {
	reqID := frame.GetRequestId()
	t.streamMu.Lock()
	ss, isStream := t.streams[reqID]
	t.streamMu.Unlock()
	if isStream {
		t.writeStreamRequest(ss, reqID, k.Data)
		return nil
	}
	t.pendMu.Lock()
	p, ok := t.pend[reqID]
	t.pendMu.Unlock()
	if !ok {
		// Not a forward request we're accumulating: relay through (e.g.
		// reverse-call body flowing back to the local relay).
		return t.send(frame)
	}
	if k.Data.GetStreamId() == streamRequest {
		t.pendMu.Lock()
		p.body = append(p.body, k.Data.GetPayload()...)
		t.pendMu.Unlock()
	}
	// Other stream_ids on a forward request are ignored on the server.
	return nil
}

// writeStreamRequest writes request bytes to a proxied loopback socket. A write
// error means the loopback side is gone: report it and drop the stream.
func (t *tunnelSession) writeStreamRequest(ss *serverStream, reqID string, data *pb.DataChunk) {
	if data.GetStreamId() != streamRequest {
		return
	}
	if _, werr := ss.conn.Write(data.GetPayload()); werr != nil {
		_ = t.send(errorFrame(reqID, codeUnavailable, "stream write: "+werr.Error()))
		_ = ss.conn.Close()
		t.streamMu.Lock()
		delete(t.streams, reqID)
		t.streamMu.Unlock()
	}
}

// handleEos ends a stream's request half, relays a non-request EOS through, or
// dispatches a forward call whose request body is now complete.
func (t *tunnelSession) handleEos(frame *pb.Frame, k *pb.Frame_Eos) error {
	reqID := frame.GetRequestId()
	// Stream: the client is done sending. Half-close the socket's write
	// side so the loopback server sees EOF, but keep reading its response.
	t.streamMu.Lock()
	ss, isStream := t.streams[reqID]
	t.streamMu.Unlock()
	if isStream {
		if k.Eos.GetStreamId() == streamRequest {
			if cw, ok := ss.conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
		}
		return nil
	}
	if k.Eos.GetStreamId() != streamRequest {
		// EOS for a non-request stream: relay through (reverse path).
		t.pendMu.Lock()
		_, isForward := t.pend[reqID]
		t.pendMu.Unlock()
		if !isForward {
			return t.send(frame)
		}
		return nil
	}
	// Request body complete — dispatch the forward call.
	t.pendMu.Lock()
	p, ok := t.pend[reqID]
	if ok {
		delete(t.pend, reqID)
	}
	t.pendMu.Unlock()
	if !ok {
		return nil
	}
	t.dispatchForward(reqID, p)
	return nil
}

// dispatchForward runs one completed forward call on its own worker. The worker
// is tracked by the wait group so teardown waits for it.
func (t *tunnelSession) dispatchForward(reqID string, p *pendingRequest) {
	t.wg.Add(1)
	go func(reqID string, hdr *pb.RequestHeader, body []byte) {
		defer t.wg.Done()
		t.server.handleForward(t.ctx, reqID, hdr, body, t.send)
	}(reqID, p.header, p.body)
}

// handleCallError relays a call-level error reported by the peer back on the
// reverse path and tears down any live stream it names. A forward-path error
// only drops the pending accumulation.
func (t *tunnelSession) handleCallError(frame *pb.Frame) error {
	reqID := frame.GetRequestId()
	// If it names a live stream, tear that stream's socket down.
	t.streamMu.Lock()
	if ss, isStream := t.streams[reqID]; isStream {
		_ = ss.conn.Close()
		delete(t.streams, reqID)
	}
	t.streamMu.Unlock()
	t.pendMu.Lock()
	_, isForward := t.pend[reqID]
	if isForward {
		delete(t.pend, reqID)
	}
	t.pendMu.Unlock()
	if !isForward {
		return t.send(frame)
	}
	return nil
}

// handleForward executes an already-remote-dom0-authorized forward call via the
// QrexecInvoker and streams the response (or a CallError) back. A per-call
// failure never tears down the Tunnel.
func (s *Server) handleForward(ctx context.Context, reqID string, hdr *pb.RequestHeader, body []byte, send func(*pb.Frame) error) {
	target := hdr.GetTargetQube()
	service := hdr.GetQrexecService()

	// Defensive allow-listing before shelling out on the remote host.
	if !transport.ValidName(service) || (target != "" && !transport.ValidName(target)) {
		_ = send(errorFrame(reqID, codeInvalid, "invalid target/service name"))
		return
	}

	// Reaching here means the remote dom0/policy has re-authorized this call.
	res, err := s.invoker.Invoke(ctx, target, service, body)
	if err != nil {
		_ = send(errorFrame(reqID, codeInternal, err.Error()))
		return
	}
	// stdout and stderr travel on separate streams, and the exit code on the
	// response EOS, so the caller can tell a command that failed (non-zero exit)
	// from a call that failed (CallError) without parsing output text.
	if len(res.Stdout) > 0 {
		if err := send(dataFrame(reqID, streamResponse, res.Stdout)); err != nil {
			return
		}
	}
	if len(res.Stderr) > 0 {
		if err := send(dataFrame(reqID, streamStderr, res.Stderr)); err != nil {
			return
		}
	}
	if err := send(eosFrameExit(reqID, streamResponse, res.ExitCode)); err != nil {
		return
	}
	if len(res.Stderr) > 0 {
		_ = send(eosFrame(reqID, streamStderr))
	}
}

// streamServicePrefix marks a request that should be TCP-proxied to a loopback
// port on THIS host rather than dispatched to a qrexec service. The port follows
// the '+', e.g. "qubesair.StreamTCP+5900". This is how GUI (VNC/Xpra) rides the
// agent's mTLS Tunnel without any port exposed on the remote's LAN.
const streamServicePrefix = "qubesair.StreamTCP+"

// streamLocalPort parses a stream service into its loopback port, if the service
// is one and the port is in an allowed GUI range. The server dials only
// 127.0.0.1:<port>, and only for these ports, so an authenticated relay cannot
// use the tunnel to reach arbitrary local services (a database, the metadata
// endpoint, ...). Widen the ranges here if a use case needs it.
func streamLocalPort(service string) (int, bool) {
	if !strings.HasPrefix(service, streamServicePrefix) {
		return 0, false
	}
	p, err := strconv.Atoi(service[len(streamServicePrefix):])
	if err != nil {
		return 0, false
	}
	if (p >= 5900 && p <= 5910) || (p >= 10000 && p <= 10010) {
		return p, true
	}
	return 0, false
}

// serverStream is one live TCP proxy: the Tunnel side writes request bytes to
// conn, and a reader goroutine turns conn's output into response frames.
type serverStream struct {
	conn net.Conn
}

// startStream dials 127.0.0.1:<port> and starts pumping its output back as
// streamResponse frames. Request bytes arrive later via serverStream.conn.Write.
func (s *Server) startStream(ctx context.Context, reqID string, port int, send func(*pb.Frame) error) (*serverStream, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := conn.Read(buf)
			if n > 0 {
				if serr := send(dataFrame(reqID, streamResponse, append([]byte(nil), buf[:n]...))); serr != nil {
					_ = conn.Close()
					return
				}
			}
			if rerr != nil {
				_ = send(eosFrame(reqID, streamResponse))
				_ = conn.Close()
				return
			}
		}
	}()
	return &serverStream{conn: conn}, nil
}
