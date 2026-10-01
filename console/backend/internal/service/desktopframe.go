package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/xpra"
)

// Desktop frame errors. The API maps each to a fixed message; the wrapped
// detail is for the log.
var (
	// ErrDesktopTransportUnavailable means this console cannot open a desktop
	// stream at all: no streamer is configured or it cannot mint its identity.
	ErrDesktopTransportUnavailable = errors.New("desktop frame transport is unavailable")
	// ErrDesktopNotReady means the qube is not running with a healthy agent
	// and an address, so there is no desktop to read.
	ErrDesktopNotReady = errors.New("qube must be running with a healthy agent")
	// ErrDesktopUnreachable means the stream to the qube's desktop failed: the
	// agent refused it, nothing listens on the desktop port, or the tunnel
	// dropped.
	ErrDesktopUnreachable = errors.New("qube desktop could not be reached")
	// ErrDesktopGrantEnded means the capture's lease was stopped or expired
	// before a frame could be returned.
	ErrDesktopGrantEnded = errors.New("desktop access grant ended")
)

// desktopFrameTimeout bounds one capture end to end: minting the identity,
// dialing, the handshake, waiting for the tunnel and the Xpra exchange (which
// caps itself at xpra.ScreenshotTimeout). The grant lifetime bounds it too.
const desktopFrameTimeout = 20 * time.Second

// DesktopLease is the part of a consumed grant a capture runs under: Done
// closes when the grant is stopped or expires, and Valid is checked before a
// frame is returned.
type DesktopLease interface {
	Done() <-chan struct{}
	Valid() bool
}

// DesktopFrameCapturer is what the desktop-access API needs from the qube
// service once it has consumed a frame grant.
type DesktopFrameCapturer interface {
	DesktopFrameAvailable() bool
	CaptureDesktopFrame(ctx context.Context, qubeID string, lease DesktopLease) (xpra.Screenshot, error)
}

var _ DesktopFrameCapturer = (*QubeServiceImpl)(nil)

// DesktopFrameAvailable reports whether frame capture has a dedicated stream
// to use. The general transport does not count.
func (s *QubeServiceImpl) DesktopFrameAvailable() bool { return s.desktop != nil }

// CaptureDesktopFrame reads one PNG frame of the qube's desktop under lease.
// The stream is torn down as soon as the lease ends, and a frame read after
// that is discarded rather than returned.
func (s *QubeServiceImpl) CaptureDesktopFrame(ctx context.Context, qubeID string, lease DesktopLease) (xpra.Screenshot, error) {
	if lease == nil || !lease.Valid() {
		return xpra.Screenshot{}, ErrDesktopGrantEnded
	}
	if s.desktop == nil {
		return xpra.Screenshot{}, ErrDesktopTransportUnavailable
	}
	qube, err := s.desktopTarget(ctx, qubeID)
	if err != nil {
		return xpra.Screenshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, desktopFrameTimeout)
	defer cancel()
	stopWatching := cancelWhenDone(lease.Done(), cancel)
	defer stopWatching()

	frame, err := s.captureFrom(ctx, cancel, qube)
	if !lease.Valid() {
		return xpra.Screenshot{}, ErrDesktopGrantEnded
	}
	if err != nil {
		return xpra.Screenshot{}, err
	}
	return frame, nil
}

// desktopTarget loads the qube and refuses one with no desktop to read.
func (s *QubeServiceImpl) desktopTarget(ctx context.Context, qubeID string) (*models.Qube, error) {
	qube, err := s.qubeRepo.GetByID(ctx, qubeID)
	if err != nil {
		return nil, ErrQubeNotFound
	}
	if qube.Status != models.QubeStatusRunning || qube.AgentHealth != models.AgentHealthHealthy ||
		strings.TrimSpace(qube.IPAddress) == "" {
		return nil, ErrDesktopNotReady
	}
	if err := s.verifyZoneConnected(ctx, qube.ZoneID); err != nil {
		return nil, err
	}
	return qube, nil
}

// captureFrom opens the stream and runs the one-shot Xpra exchange on it.
// cancel ends the capture context; it is called before waiting for the stream
// goroutine, which is what makes that wait bounded.
func (s *QubeServiceImpl) captureFrom(ctx context.Context, cancel context.CancelFunc, qube *models.Qube) (xpra.Screenshot, error) {
	relay, err := s.desktop.OpenDesktopTransport(ctx, qube)
	if err != nil {
		return xpra.Screenshot{}, err
	}
	stream := newXpraStream(ctx, relay, qube.Name)
	frame, err := xpra.GetScreenshot(ctx, stream)
	cancel()
	streamErr := stream.wait()
	if err == nil {
		return frame, nil
	}
	// The stream failing first (agent refusal, nothing on the port, tunnel
	// gone) surfaces in the Xpra client as a read error; report the cause.
	if streamErr != nil && !errors.Is(streamErr, context.Canceled) && !errors.Is(streamErr, context.DeadlineExceeded) {
		return xpra.Screenshot{}, fmt.Errorf("%w: %v", ErrDesktopUnreachable, streamErr)
	}
	return xpra.Screenshot{}, fmt.Errorf("capture desktop frame of %q: %w", qube.Name, err)
}

// cancelWhenDone calls cancel once done closes. The returned stop ends the
// watch and returns after the watching goroutine has exited.
func cancelWhenDone(done <-chan struct{}, cancel context.CancelFunc) (stop func()) {
	quit := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-done:
			cancel()
		case <-quit:
		}
	}()
	return func() {
		close(quit)
		<-exited
	}
}
