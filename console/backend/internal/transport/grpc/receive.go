package grpc

import (
	"context"

	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	"google.golang.org/grpc"
)

// receiveFrame lets revocation end an idle stream. Returning from the handler
// cancels gRPC's Recv; the buffered result lets its worker exit after return.
func receiveFrame(ctx context.Context, stream grpc.BidiStreamingServer[pb.Frame, pb.Frame]) (*pb.Frame, error) {
	type result struct {
		frame *pb.Frame
		err   error
	}
	done := make(chan result, 1)
	go func() { frame, err := stream.Recv(); done <- result{frame, err} }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return value.frame, value.err
	}
}
