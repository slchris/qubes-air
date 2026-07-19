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
	"time"

	"github.com/slchris/qubes-air/console/internal/repository"
	"sync"

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
// defence in depth, not an authorization boundary.
type QrexecInvoker interface {
	Invoke(ctx context.Context, target, service string, in []byte) ([]byte, error)
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

// verifyRegisteredPeer authorizes a client certificate against the registry.
//
// Runs after the standard chain verification, so the certificate is already
// known to be CA-signed and in date; this adds "and we still permit it".
func (s *Server) verifyRegisteredPeer(rawCerts [][]byte, chains [][]*x509.Certificate) error {
	if len(chains) == 0 || len(chains[0]) == 0 {
		return fmt.Errorf("no verified certificate chain")
	}
	leaf := chains[0][0]
	fp := repository.Fingerprint(leaf)

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
func (s *Server) reauthorizeLoop(ctx context.Context, cancel context.CancelFunc, fingerprint string) {
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
// cancelled the server is gracefully stopped and Serve returns nil.
func (s *Server) Serve(ctx context.Context) error {
	if s.cfg.TLS == nil {
		return fmt.Errorf("grpc server: nil TLS config (mTLS is required)")
	}
	// Require and verify the client certificate (relay identity). mTLS proves
	// *who* connected; it is NOT authorization for a given call — that stays in
	// the local dom0 policy.
	if s.cfg.TLS.ClientAuth == tls.NoClientCert {
		s.cfg.TLS.ClientAuth = tls.RequireAndVerifyClientCert
	}

	// A CA signature alone grants PERMANENT access — there is no way to take it
	// back without a revocation mechanism. Checking the certificate against a
	// registry we own closes that: revocation is a row update this callback
	// reads on the next handshake, with no CRL to publish and no fetch that can
	// silently fail. Without a registry configured, any CA-signed certificate is
	// accepted forever, which is worth saying out loud.
	if s.cfg.CertRegistry != nil {
		s.cfg.TLS.VerifyPeerCertificate = s.verifyRegisteredPeer
	} else {
		log.Printf("grpc server: WARNING no certificate registry configured — " +
			"any CA-signed client certificate is accepted and CANNOT be revoked")
	}

	s.applyCertSource()

	lis, err := net.Listen("tcp", s.cfg.Listen)
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
func (s *Server) Tunnel(stream grpc.BidiStreamingServer[pb.Frame, pb.Frame]) error {
	ctx, cancelTunnel := context.WithCancel(stream.Context())
	defer cancelTunnel()

	// Re-authorize the peer certificate periodically for as long as the tunnel
	// lives.
	//
	// Checking only at handshake would let a revoked agent keep an established
	// connection indefinitely — and these tunnels are deliberately long-lived,
	// so "indefinitely" means until someone notices. Revocation has to reach a
	// connection that is already open, or it is not revocation.
	if s.cfg.CertRegistry != nil {
		if fp, ok := peerFingerprint(stream.Context()); ok {
			go s.reauthorizeLoop(ctx, cancelTunnel, fp)
		}
	}

	// Send is not concurrent-safe; serialize all sends through this mutex so
	// per-request goroutines can reply independently.
	var sendMu sync.Mutex
	send := func(f *pb.Frame) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(f)
	}

	// --- Handshake: first frame must be a Handshake with a matching version.
	first, err := stream.Recv()
	if err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	hs := first.GetHandshake()
	if hs == nil {
		return fmt.Errorf("grpc server: expected Handshake as first frame, got %T", first.GetKind())
	}
	if !supportsProtocol(hs.GetProtocolVersion()) {
		msg := protocolMismatchMessage(hs.GetProtocolVersion())
		// Tell the peer WHY before closing. A bare stream error is
		// indistinguishable from a network fault, which sends whoever is
		// debugging it looking at firewalls instead of versions.
		_ = send(&pb.Frame{Kind: &pb.Frame_Error{Error: &pb.CallError{
			Code:    CodeProtocolMismatch,
			Message: msg,
		}}})
		log.Printf("grpc server: rejecting relay %q (build %q): %s",
			hs.GetRelayName(), hs.GetBuildVersion(), msg)
		return fmt.Errorf("grpc server: %s", msg)
	}
	relayName := hs.GetRelayName()
	remoteName := hs.GetRemoteName()
	// Build version is observability only — logged so an operator can tell which
	// agent build is actually running out there, without it gating anything.
	log.Printf("grpc server: relay %q connected (protocol %s, build %s)",
		relayName, hs.GetProtocolVersion(), orUnknown(hs.GetBuildVersion()))
	// Acknowledge with our own Handshake frame.
	if err := send(handshakeFrame(remoteName, relayName)); err != nil {
		return err
	}

	// --- Per-request accumulation of forward request bodies.
	// Guarded by pendMu because request frames for different request_ids may
	// interleave on the stream and we accumulate their bodies here.
	type pending struct {
		header *pb.RequestHeader
		body   []byte
	}
	var pendMu sync.Mutex
	pend := make(map[string]*pending)

	// Track in-flight worker goroutines so we can wait for them on return.
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		frame, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		switch k := frame.GetKind().(type) {
		case *pb.Frame_KeepAlive:
			// Echo keepalive to keep the NAT mapping warm and prove liveness.
			if err := send(keepAliveFrame(k.KeepAlive.GetUnixMs())); err != nil {
				return err
			}

		case *pb.Frame_RequestHeader:
			reqID := frame.GetRequestId()
			hdr := k.RequestHeader
			switch hdr.GetDirection() {
			case pb.Direction_LOCAL_TO_REMOTE:
				// Forward call: begin accumulating its request body.
				pendMu.Lock()
				pend[reqID] = &pending{header: hdr}
				pendMu.Unlock()
			case pb.Direction_REMOTE_TO_LOCAL:
				// Reverse call originated remotely (e.g. remote qube → local
				// vault). The server does NOT authorize or execute it; it just
				// relays the header back to the local relay, whose side routes
				// it through LOCAL dom0 policy C (ask). Relay the frame as-is.
				if err := send(frame); err != nil {
					return err
				}
			default:
				if err := send(errorFrame(reqID, codeInvalid, "unknown direction")); err != nil {
					return err
				}
			}

		case *pb.Frame_Data:
			reqID := frame.GetRequestId()
			pendMu.Lock()
			p, ok := pend[reqID]
			pendMu.Unlock()
			if !ok {
				// Not a forward request we're accumulating: relay through (e.g.
				// reverse-call body flowing back to the local relay).
				if err := send(frame); err != nil {
					return err
				}
				break
			}
			if k.Data.GetStreamId() == streamRequest {
				pendMu.Lock()
				p.body = append(p.body, k.Data.GetPayload()...)
				pendMu.Unlock()
			}
			// Other stream_ids on a forward request are ignored on the server.

		case *pb.Frame_Eos:
			reqID := frame.GetRequestId()
			if k.Eos.GetStreamId() != streamRequest {
				// EOS for a non-request stream: relay through (reverse path).
				pendMu.Lock()
				_, isForward := pend[reqID]
				pendMu.Unlock()
				if !isForward {
					if err := send(frame); err != nil {
						return err
					}
				}
				break
			}
			// Request body complete — dispatch the forward call.
			pendMu.Lock()
			p, ok := pend[reqID]
			if ok {
				delete(pend, reqID)
			}
			pendMu.Unlock()
			if !ok {
				break
			}

			wg.Add(1)
			go func(reqID string, hdr *pb.RequestHeader, body []byte) {
				defer wg.Done()
				s.handleForward(ctx, reqID, hdr, body, send)
			}(reqID, p.header, p.body)

		case *pb.Frame_Error:
			// A call-level error reported by the peer. Relay reverse-path errors
			// back; forward-path errors just drop the pending accumulation.
			reqID := frame.GetRequestId()
			pendMu.Lock()
			_, isForward := pend[reqID]
			if isForward {
				delete(pend, reqID)
			}
			pendMu.Unlock()
			if !isForward {
				if err := send(frame); err != nil {
					return err
				}
			}

		case *pb.Frame_Handshake:
			// A second handshake is unexpected; ignore it.

		default:
			// Unknown/empty frame kind: ignore to stay tolerant.
		}
	}
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
	out, err := s.invoker.Invoke(ctx, target, service, body)
	if err != nil {
		_ = send(errorFrame(reqID, codeInternal, err.Error()))
		return
	}
	if len(out) > 0 {
		if err := send(dataFrame(reqID, streamResponse, out)); err != nil {
			return
		}
	}
	_ = send(eosFrame(reqID, streamResponse))
}
