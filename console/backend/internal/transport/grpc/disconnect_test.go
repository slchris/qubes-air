package grpc

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	"github.com/stretchr/testify/require"
)

type switchingTunnel struct {
	fakeTunnel
	afterSend func(*pb.Frame)
}

type failingSendTunnel struct {
	fakeTunnel
	failAt int
	sends  int
}

func (s *failingSendTunnel) Send(frame *pb.Frame) error {
	s.sends++
	if s.sends == s.failAt {
		return ErrNotConnected
	}
	return s.fakeTunnel.Send(frame)
}

func TestSendFailuresNeverPermitReplay(t *testing.T) {
	for _, phase := range []int{1, 2, 3} {
		cli := NewClient(ClientConfig{}, nil)
		cli.setStream(&failingSendTunnel{failAt: phase})
		_, err := cli.Call(context.Background(), "target", "qubesair.Exec", []byte("body"))
		require.ErrorIs(t, err, ErrDisconnected)
		require.NotErrorIs(t, err, ErrNotConnected)
	}
	cli := NewClient(ClientConfig{}, nil)
	cli.setStream(&failingSendTunnel{failAt: 1})
	input := strings.NewReader("unread")
	err := cli.CallStream(context.Background(), "target", "qubesair.StreamTCP+10000", input, io.Discard)
	require.ErrorIs(t, err, ErrDisconnected)
	require.NotErrorIs(t, err, ErrNotConnected)
	require.Equal(t, 6, input.Len())
}

// Even when stdin becomes readable only after a reconnect, its data and EOS
// stay on the original stream. Cancellation must drop such late input.
func TestStreamInputStaysOnOriginalTunnel(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		cli := NewClient(ClientConfig{}, nil)
		original, replacement := &fakeTunnel{}, &fakeTunnel{}
		cli.setStream(replacement)
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		}
		cli.sendStreamInput(ctx, original, "request", strings.NewReader("body"))
		cancel()
		require.Empty(t, replacement.sent)
		if canceled {
			require.Empty(t, original.sent)
		} else {
			require.Len(t, original.sent, 2)
			require.Equal(t, []byte("body"), original.sent[0].GetData().GetPayload())
			require.NotNil(t, original.sent[1].GetEos())
		}
	}
}

func (s *switchingTunnel) Send(frame *pb.Frame) error {
	if err := s.fakeTunnel.Send(frame); err != nil {
		return err
	}
	s.afterSend(frame)
	return nil
}

// Losing the tunnel between header/body/EOS must neither make an accepted
// request retryable nor send its remaining frames on a replacement tunnel.
func TestCallStaysOnOriginalTunnel(t *testing.T) {
	for _, replace := range []bool{false, true} {
		for _, phase := range []string{"header", "body"} {
			t.Run(phase+map[bool]string{false: "/down", true: "/reconnected"}[replace], func(t *testing.T) {
				cli := NewClient(ClientConfig{}, nil)
				replacement := &fakeTunnel{}
				cli.setStream(&switchingTunnel{afterSend: func(frame *pb.Frame) {
					if (phase == "header" && frame.GetRequestHeader() != nil) ||
						(phase == "body" && frame.GetData() != nil) {
						cli.clearStream(errors.New("test: lost connection"))
						if replace {
							cli.setStream(replacement)
						}
					}
				}})
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, err := cli.Call(ctx, "target", "qubesair.Exec", []byte("body"))
				require.Error(t, err)
				require.NotErrorIs(t, err, ErrNotConnected)
				require.NotErrorIs(t, err, context.DeadlineExceeded)
				require.Empty(t, replacement.sent, "request frames crossed a reconnect")
			})
		}
	}
}

func TestDisconnectedCallsAreSafeToRetryBeforeDispatch(t *testing.T) {
	cli := NewClient(ClientConfig{}, nil)
	ctx := context.Background()
	_, err := cli.Call(ctx, "target", "qubesair.Exec", nil)
	require.ErrorIs(t, err, ErrNotConnected)
	input := strings.NewReader("unread")
	err = cli.CallStream(ctx, "target", "qubesair.StreamTCP+10000", input, io.Discard)
	require.ErrorIs(t, err, ErrNotConnected)
	require.Equal(t, 6, input.Len(), "pre-dispatch retry must not consume input")
}
