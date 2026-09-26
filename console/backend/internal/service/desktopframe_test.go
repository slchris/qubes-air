package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/desktopaccess"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/transport"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
	"github.com/slchris/qubes-air/console/internal/xpra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeXpraServer is a StreamTransport that plays the Xpra side of the
// one-shot exchange: it reads the client's hello and answers with reply, or
// blocks until the stream is canceled when block is set.
type fakeXpraServer struct {
	mu       sync.Mutex
	targets  []string
	services []string
	reply    []any
	err      error
	block    bool
	started  chan struct{}
	canceled chan struct{}
}

func (f *fakeXpraServer) CallStream(ctx context.Context, target, service string, stdin io.Reader, stdout io.Writer) error {
	f.mu.Lock()
	f.targets = append(f.targets, target)
	f.services = append(f.services, service)
	f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if _, err := xpra.ReadRecord(stdin); err != nil {
		return err
	}
	if f.block {
		close(f.started)
		<-ctx.Done()
		close(f.canceled)
		return ctx.Err()
	}
	if err := xpra.WriteRecord(stdout, f.reply); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeXpraServer) calls() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.targets...), append([]string(nil), f.services...)
}

// fakeOpener hands out one transport and records which qube it was asked for.
type fakeOpener struct {
	relay transport.StreamTransport
	err   error
	asked atomic.Int32
	qube  atomic.Pointer[models.Qube]
}

func (o *fakeOpener) OpenDesktopTransport(_ context.Context, qube *models.Qube) (transport.StreamTransport, error) {
	o.asked.Add(1)
	o.qube.Store(qube)
	return o.relay, o.err
}

// fakeLease is a DesktopLease the test ends by closing done.
type fakeLease struct {
	done  chan struct{}
	ended atomic.Bool
}

func newFakeLease() *fakeLease { return &fakeLease{done: make(chan struct{})} }

func (l *fakeLease) Done() <-chan struct{} { return l.done }
func (l *fakeLease) Valid() bool           { return !l.ended.Load() }
func (l *fakeLease) end() {
	l.ended.Store(true)
	close(l.done)
}

func pngFrame(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))))
	return buf.Bytes()
}

func screenshotReply(t *testing.T) []any {
	t.Helper()
	return []any{"screenshot", int64(2), int64(1), "png", int64(8), pngFrame(t, 2, 1)}
}

// desktopQube builds a qube service whose qube is running, healthy and
// addressable, with opener as its desktop streamer. general is the service's
// ordinary transport, which frame capture must never use.
func desktopQube(t *testing.T, opener DesktopTransportOpener, general transport.Transport) (*QubeServiceImpl, string) {
	t.Helper()
	if general == nil {
		general = transport.NoopTransport{}
	}
	svc, id, cleanup := setupWithTransport(t, general)
	t.Cleanup(cleanup)
	impl := svc.(*QubeServiceImpl)
	impl.desktop = opener
	ctx := context.Background()
	require.NoError(t, impl.qubeRepo.UpdateStatus(ctx, id, models.QubeStatusRunning))
	require.NoError(t, impl.qubeRepo.UpdateIPAddress(ctx, id, "192.0.2.10"))
	require.NoError(t, impl.qubeRepo.UpdateAgentHealth(ctx, id, models.AgentHealthHealthy, time.Now(), ""))
	return impl, id
}

func TestCaptureDesktopFrameReadsOneFrameOverTheDesktopStream(t *testing.T) {
	server := &fakeXpraServer{reply: screenshotReply(t)}
	opener := &fakeOpener{relay: server}
	svc, id := desktopQube(t, opener, nil)
	require.True(t, svc.DesktopFrameAvailable())

	frame, err := svc.CaptureDesktopFrame(context.Background(), id, newFakeLease())
	require.NoError(t, err)
	assert.Equal(t, uint16(2), frame.Width)
	assert.Equal(t, uint16(1), frame.Height)
	assert.NotEmpty(t, frame.PNG)

	targets, services := server.calls()
	assert.Equal(t, []string{"reach-qube"}, targets)
	assert.Equal(t, []string{transportgrpc.DesktopStreamService}, services)
	assert.Equal(t, "reach-qube", opener.qube.Load().Name)
}

// The general relay transport can stream too, but it carries the relay's
// identity and is pinned to one endpoint: frame capture must refuse rather
// than fall back to it.
func TestCaptureDesktopFrameNeverFallsBackToTheGeneralTransport(t *testing.T) {
	general := &streamingTransport{}
	svc, id := desktopQube(t, nil, general)
	require.False(t, svc.DesktopFrameAvailable())

	_, err := svc.CaptureDesktopFrame(context.Background(), id, newFakeLease())
	require.ErrorIs(t, err, ErrDesktopTransportUnavailable)
	assert.Zero(t, general.streams.Load(), "the general transport must not carry desktop frames")
}

type streamingTransport struct{ streams atomic.Int32 }

func (s *streamingTransport) Call(context.Context, string, string, []byte) ([]byte, error) {
	return nil, errors.New("unexpected call")
}

func (s *streamingTransport) CallStream(context.Context, string, string, io.Reader, io.Writer) error {
	s.streams.Add(1)
	return errors.New("unexpected stream")
}

func TestCaptureDesktopFrameRefusesBeforeOpeningAStream(t *testing.T) {
	ended := newFakeLease()
	ended.end()
	for _, tc := range []struct {
		name  string
		lease DesktopLease
		setup func(t *testing.T, svc *QubeServiceImpl, id string)
		want  error
	}{
		{name: "ended lease", lease: ended, want: ErrDesktopGrantEnded},
		{name: "nil lease", lease: nil, want: ErrDesktopGrantEnded},
		{name: "stopped qube", lease: newFakeLease(), want: ErrDesktopNotReady, setup: func(t *testing.T, svc *QubeServiceImpl, id string) {
			require.NoError(t, svc.qubeRepo.UpdateStatus(context.Background(), id, models.QubeStatusStopped))
		}},
		{name: "unhealthy agent", lease: newFakeLease(), want: ErrDesktopNotReady, setup: func(t *testing.T, svc *QubeServiceImpl, id string) {
			require.NoError(t, svc.qubeRepo.UpdateAgentHealth(context.Background(), id, models.AgentHealthUnreachable, time.Now(), "down"))
		}},
		{name: "no address", lease: newFakeLease(), want: ErrDesktopNotReady, setup: func(t *testing.T, svc *QubeServiceImpl, id string) {
			require.NoError(t, svc.qubeRepo.UpdateIPAddress(context.Background(), id, ""))
		}},
		{name: "disconnected zone", lease: newFakeLease(), want: ErrZoneDisconnected, setup: func(t *testing.T, svc *QubeServiceImpl, id string) {
			qube, err := svc.qubeRepo.GetByID(context.Background(), id)
			require.NoError(t, err)
			zone, err := svc.zoneRepo.GetByID(context.Background(), qube.ZoneID)
			require.NoError(t, err)
			zone.Status = models.ZoneStatusDisconnected
			require.NoError(t, svc.zoneRepo.Update(context.Background(), zone))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opener := &fakeOpener{relay: &fakeXpraServer{reply: screenshotReply(t)}}
			svc, id := desktopQube(t, opener, nil)
			if tc.setup != nil {
				tc.setup(t, svc, id)
			}
			_, err := svc.CaptureDesktopFrame(context.Background(), id, tc.lease)
			require.ErrorIs(t, err, tc.want)
			assert.Zero(t, opener.asked.Load(), "no stream may be opened")
		})
	}

	opener := &fakeOpener{}
	svc, _ := desktopQube(t, opener, nil)
	_, err := svc.CaptureDesktopFrame(context.Background(), "no-such-qube", newFakeLease())
	require.ErrorIs(t, err, ErrQubeNotFound)
	assert.Zero(t, opener.asked.Load())
}

// An operator's Stop ends the stream mid-capture, and nothing is returned.
func TestCaptureDesktopFrameStopsTheStreamWhenTheGrantIsStopped(t *testing.T) {
	server := &fakeXpraServer{block: true, started: make(chan struct{}), canceled: make(chan struct{})}
	svc, id := desktopQube(t, &fakeOpener{relay: server}, nil)
	store := desktopaccess.NewStore()
	lease, request := acquireFrameLease(t, store, id)

	done := make(chan error, 1)
	go func() {
		_, err := svc.CaptureDesktopFrame(context.Background(), id, lease)
		done <- err
	}()
	waitClosed(t, server.started, "the stream never started")
	_, err := store.Stop(request.ID)
	require.NoError(t, err)
	waitClosed(t, server.canceled, "Stop did not cancel the stream")
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrDesktopGrantEnded)
	case <-time.After(3 * time.Second):
		t.Fatal("capture kept running after Stop")
	}
}

// The grant running out ends the stream the same way.
func TestCaptureDesktopFrameStopsTheStreamWhenTheGrantExpires(t *testing.T) {
	server := &fakeXpraServer{block: true, started: make(chan struct{}), canceled: make(chan struct{})}
	svc, id := desktopQube(t, &fakeOpener{relay: server}, nil)
	lease := newFakeLease()

	done := make(chan error, 1)
	go func() {
		_, err := svc.CaptureDesktopFrame(context.Background(), id, lease)
		done <- err
	}()
	waitClosed(t, server.started, "the stream never started")
	lease.end()
	waitClosed(t, server.canceled, "expiry did not cancel the stream")
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrDesktopGrantEnded)
	case <-time.After(3 * time.Second):
		t.Fatal("capture kept running after the grant expired")
	}
}

func TestCaptureDesktopFrameReportsWhyTheFrameFailed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opener *fakeOpener
		want   error
	}{
		{name: "agent refused the stream", opener: &fakeOpener{relay: &fakeXpraServer{err: errors.New("remote: DENIED: restricted")}}, want: ErrDesktopUnreachable},
		{name: "server did not honor the request", opener: &fakeOpener{relay: &fakeXpraServer{reply: []any{"hello", map[string]any{}}}}, want: xpra.ErrScreenshotUnsupported},
		{name: "no window to capture", opener: &fakeOpener{relay: &fakeXpraServer{reply: []any{"screenshot", int64(0), int64(0), "png", int64(0), []byte{}}}}, want: xpra.ErrEmptyScreenshot},
		{name: "identity could not be minted", opener: &fakeOpener{err: ErrDesktopTransportUnavailable}, want: ErrDesktopTransportUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, id := desktopQube(t, tc.opener, nil)
			_, err := svc.CaptureDesktopFrame(context.Background(), id, newFakeLease())
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// A caller deadline bounds a capture whose server never answers.
func TestCaptureDesktopFrameHonorsTheCallerDeadline(t *testing.T) {
	server := &fakeXpraServer{block: true, started: make(chan struct{}), canceled: make(chan struct{})}
	svc, id := desktopQube(t, &fakeOpener{relay: server}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := svc.CaptureDesktopFrame(ctx, id, newFakeLease())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	waitClosed(t, server.canceled, "the stream outlived the capture")
}

func acquireFrameLease(t *testing.T, store *desktopaccess.Store, qubeID string) (*desktopaccess.Lease, desktopaccess.Request) {
	t.Helper()
	request, err := store.Request("mcp-client", qubeID, desktopaccess.OperationFrame)
	require.NoError(t, err)
	type delivered struct {
		secret string
		err    error
	}
	got := make(chan delivered, 1)
	go func() {
		secret, _, err := store.WaitDecision(context.Background(), request.ID, "mcp-client")
		got <- delivered{secret, err}
	}()
	require.Eventually(t, func() bool {
		_, err := store.Approve(request.ID)
		return err == nil
	}, 3*time.Second, time.Millisecond)
	d := <-got
	require.NoError(t, d.err)
	lease, err := store.Acquire(d.secret, "mcp-client", qubeID, desktopaccess.OperationFrame)
	require.NoError(t, err)
	return lease, request
}

func waitClosed(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal(msg)
	}
}
