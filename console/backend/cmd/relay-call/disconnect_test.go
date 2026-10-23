package main

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// disconnectAgent accepts a request over real mTLS, then loses the response.
// The caller cannot know whether a side effect happened and must not replay it.
type disconnectAgent struct {
	pb.UnimplementedRelayTransportServer
	calls atomic.Int32
	drop  atomic.Bool
}

func (s *disconnectAgent) Tunnel(stream pb.RelayTransport_TunnelServer) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		if header := frame.GetRequestHeader(); header != nil {
			s.calls.Add(1)
			if strings.HasPrefix(header.GetQrexecService(), "qubesair.StreamTCP+") && s.drop.Load() {
				return status.Error(codes.Unavailable, "test: stream lost after dispatch")
			}
		}
		if frame.GetEos() == nil {
			continue
		}
		if s.drop.Load() {
			return status.Error(codes.Unavailable, "test: response lost after dispatch")
		}
		if err := stream.Send(&pb.Frame{RequestId: frame.GetRequestId(), Kind: &pb.Frame_Eos{
			Eos: &pb.EndOfStream{StreamId: 1},
		}}); err != nil {
			return err
		}
	}
}

func startDisconnectAgent(t *testing.T, ca *pki.CA, agent *disconnectAgent) string {
	t.Helper()
	bundle, err := ca.IssueAgentCert(pki.AgentCommonName("target"), time.Hour)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	require.NoError(t, err)
	_, pool := mintFromCA(ca)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{pair}, ClientCAs: pool,
		ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13,
	})))
	pb.RegisterRelayTransportServer(srv, agent)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func TestDispatchedCallsAreNotRetriedAfterDisconnect(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "buffered"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			ca := newTestCA(t)
			pair, pool := mintFromCA(ca)
			agent := &disconnectAgent{}
			agent.drop.Store(true)
			addr := startDisconnectAgent(t, ca, agent)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var err error
			if streaming {
				stdin, writer, pipeErr := os.Pipe()
				require.NoError(t, pipeErr)
				defer stdin.Close()
				defer writer.Close()
				original := os.Stdin
				os.Stdin = stdin
				defer func() { os.Stdin = original }()
				err = dialAndStream(ctx, pair, pool, addr, "target", "qubesair.StreamTCP+10000")
			} else {
				_, err = dialAndCall(ctx, pair, pool, addr, "target", "qubesair.Exec", []byte("side effect"))
			}
			require.Error(t, err)
			require.NotErrorIs(t, err, transportgrpc.ErrNotConnected, "already dispatched: retry is unsafe")
			require.NotErrorIs(t, err, context.DeadlineExceeded, "disconnect must return without waiting for the deadline")
			require.Equal(t, int32(1), agent.calls.Load(), "an accepted call must not be replayed")

			// A new, explicit call can connect after the failed one.
			agent.drop.Store(false)
			freshCtx, freshCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer freshCancel()
			_, err = dialAndCall(freshCtx, pair, pool, addr, "target", "qubesair.Ping", nil)
			require.NoError(t, err)
			require.Equal(t, int32(2), agent.calls.Load())
		})
	}
}
