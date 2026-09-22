package grpc

import (
	"context"
	"io"
	"testing"

	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	"google.golang.org/grpc"
)

// fakeTunnelStream stands in for a live bidi stream: it records the frames the
// server sends and never produces one itself. Only Send/Recv are reached by the
// frame handlers here, so the embedded interface satisfies grpc.ServerStream.
type fakeTunnelStream struct {
	grpc.ServerStream
	sent []*pb.Frame
}

func (f *fakeTunnelStream) Send(frame *pb.Frame) error {
	f.sent = append(f.sent, frame)
	return nil
}

func (f *fakeTunnelStream) Recv() (*pb.Frame, error) { return nil, io.EOF }

// TestTunnelKeepAliveEcho proves the server answers a keepalive with the peer's
// own timestamp. That echo is what keeps the NAT mapping warm and lets the peer
// see liveness; dropping it turns a healthy tunnel into an apparently dead one.
func TestTunnelKeepAliveEcho(t *testing.T) {
	stream := &fakeTunnelStream{}
	sess := newTunnelSession(context.Background(), func() {}, NewServer(ServerConfig{}, &fakeInvoker{}), stream)

	const sentAt = int64(1700000000123)
	if err := sess.dispatch(keepAliveFrame(sentAt)); err != nil {
		t.Fatalf("dispatch keepalive: %v", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("sent %d frames, want 1 keepalive echo", len(stream.sent))
	}
	echo := stream.sent[0].GetKeepAlive()
	if echo == nil {
		t.Fatalf("echo kind = %T, want keepalive", stream.sent[0].GetKind())
	}
	if echo.GetUnixMs() != sentAt {
		t.Fatalf("echo unix_ms = %d, want %d", echo.GetUnixMs(), sentAt)
	}
}

// TestTunnelHandlesPeerCallError proves both halves of the error-frame branch:
// an error naming a forward call only drops the request body being accumulated,
// while an error on the reverse path is relayed to the local relay. Either way
// the Tunnel itself stays up.
func TestTunnelHandlesPeerCallError(t *testing.T) {
	stream := &fakeTunnelStream{}
	sess := newTunnelSession(context.Background(), func() {}, NewServer(ServerConfig{}, &fakeInvoker{}), stream)

	sess.pend["fwd-1"] = &pendingRequest{header: &pb.RequestHeader{QrexecService: "qubesair.Ping"}}
	forwardErr := &pb.Frame{RequestId: "fwd-1", Kind: &pb.Frame_Error{Error: &pb.CallError{
		Code: codeInternal, Message: "remote call failed",
	}}}
	if err := sess.dispatch(forwardErr); err != nil {
		t.Fatalf("dispatch forward-path error: %v", err)
	}
	if _, pending := sess.pend["fwd-1"]; pending {
		t.Fatal("forward request body kept after the peer reported an error")
	}
	if len(stream.sent) != 0 {
		t.Fatalf("relayed %d frames for a forward-path error, want 0", len(stream.sent))
	}

	reverseErr := &pb.Frame{RequestId: "rev-1", Kind: &pb.Frame_Error{Error: &pb.CallError{
		Code: codeInternal, Message: "local target failed",
	}}}
	if err := sess.dispatch(reverseErr); err != nil {
		t.Fatalf("dispatch reverse-path error: %v", err)
	}
	if len(stream.sent) != 1 || stream.sent[0] != reverseErr {
		t.Fatalf("reverse-path error not relayed to the local relay: sent %d frames", len(stream.sent))
	}
}
