package xpra

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"sync"
	"time"
)

// Bounds on one screenshot session. The packet's width and height are checked
// against the dimension and pixel caps, and the PNG header must declare that
// same size before the image is decoded, so decoding allocates for at most
// MaxScreenshotPixels pixels (up to 8 bytes each for 16-bit RGBA, plus a
// second copy for an interlaced PNG).
const (
	// ScreenshotTimeout caps the whole session, including a caller context
	// with a later or no deadline; on expiry the stream is closed.
	ScreenshotTimeout = 10 * time.Second
	// MaxScreenshotBytes caps the encoded PNG.
	MaxScreenshotBytes = 2 << 20
	// MaxScreenshotPixels caps width * height.
	MaxScreenshotPixels = 4 << 20
	// MaxScreenshotDimension caps width and height individually.
	MaxScreenshotDimension = 8192
	xpraClientVersion      = "6.6"
)

// Packet names in the one-shot screenshot exchange. Released 5.x and 6.x
// servers call the image packet "screenshot" and the close packet
// "disconnect". Master (7.0) uses "display-screenshot" and "connection-close"
// only when XPRA_BACKWARDS_COMPATIBLE is off; it is on by default.
const (
	packetHello             = "hello"
	packetScreenshot        = "screenshot"
	packetDisplayScreenshot = "display-screenshot"
	packetChallenge         = "challenge"
	packetSSLUpgrade        = "ssl-upgrade"
	packetDisconnect        = "disconnect"
	packetConnectionClose   = "connection-close"
)

var (
	// ErrScreenshotSession reports a protocol, I/O or image failure.
	ErrScreenshotSession = errors.New("xpra screenshot session failed")
	// ErrAuthenticationRequired reports a server challenge. The client has no
	// credentials to answer one and fails closed.
	ErrAuthenticationRequired = errors.New("xpra screenshot requires unsupported authentication")
	// ErrUnsupportedUpgrade reports a server request to switch to TLS.
	ErrUnsupportedUpgrade = errors.New("xpra screenshot requested an unsupported transport upgrade")
	// ErrScreenshotUnsupported reports a server that answered with its own
	// hello. Upstream servers only do that when they have no handler for the
	// screenshot request and set the connection up as a full client instead.
	ErrScreenshotUnsupported = errors.New("xpra server did not honor the screenshot request")
	// ErrEmptyScreenshot reports the 0x0 image a server sends when it has no
	// window to capture.
	ErrEmptyScreenshot = errors.New("xpra screenshot is empty")
)

// Screenshot is a PNG frame returned by the Xpra one-shot screenshot request.
type Screenshot struct {
	Width     uint16
	Height    uint16
	RowStride uint32
	PNG       []byte
}

// GetScreenshot asks an Xpra server for one PNG of its display and closes the
// stream. The exchange is a single client hello carrying request=screenshot,
// answered by a single image packet. Upstream servers from 6.2 answer that
// request from their hello handler with the image alone and send no server
// hello (read from the v6.2, v6.3, v6.4 and master sources; 5.x does not
// handle the request; nothing has been run against a live server). The
// client reads exactly one record: a challenge, ssl-upgrade, disconnect,
// server hello or any other packet fails the session. It never attaches as a
// desktop client and sends no window, input or clipboard traffic. The stream
// is closed exactly once, whether the session ends normally or the context is
// done while a read or write is blocked.
func GetScreenshot(ctx context.Context, stream io.ReadWriteCloser) (Screenshot, error) {
	return getScreenshot(ctx, stream, ScreenshotTimeout)
}

// getScreenshot is GetScreenshot with the session cap as a parameter, so the
// cap can be tested without waiting for ScreenshotTimeout.
func getScreenshot(ctx context.Context, stream io.ReadWriteCloser, limit time.Duration) (Screenshot, error) {
	if ctx == nil || stream == nil {
		return Screenshot{}, fmt.Errorf("%w: missing context or stream", ErrScreenshotSession)
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var closeOnce sync.Once
	closeStream := func() { closeOnce.Do(func() { _ = stream.Close() }) }
	stop := context.AfterFunc(ctx, closeStream)
	defer stop()
	defer closeStream()
	if err := ctx.Err(); err != nil {
		return Screenshot{}, err
	}
	if err := WriteRecord(stream, screenshotHello()); err != nil {
		return Screenshot{}, sessionIOError(ctx, "send hello", err)
	}
	response, err := ReadRecord(stream)
	if err != nil {
		return Screenshot{}, sessionIOError(ctx, "read response", err)
	}
	return decodeResponse(response)
}

// screenshotHello builds the request. Servers up to 6.4 choose the packet
// encoder from a boolean capability named after it, and master also reads the
// "encoders" list, so both are sent. compression_level 0 selects no
// compressor. chunks=false, which servers honor from 6.3, makes them inline
// the PNG rather than send it as a raw chunk, which ReadRecord rejects; 5.x
// and 6.2 ignore it and send any PNG over 32 KiB as a raw chunk. 5.x does not
// know request=screenshot at all and sets the connection up as a client;
// ui_client=false keeps that from displacing an attached desktop client
// before this side sees the server hello and gives up. There is no
// "packet-types" list: servers read it only to decide whether to offer
// ssl-upgrade, which this client cannot accept.
func screenshotHello() []any {
	caps := map[string]any{
		"version":           xpraClientVersion,
		"request":           "screenshot",
		"ui_client":         false,
		"encoders":          []any{"rencodeplus"},
		"rencodeplus":       true,
		"compressors":       []any{},
		"compression_level": int64(0),
		"chunks":            false,
	}
	return []any{packetHello, caps}
}

func decodeResponse(packet []any) (Screenshot, error) {
	kind, _ := packetType(packet)
	switch kind {
	case packetScreenshot, packetDisplayScreenshot:
		return decodeScreenshotPacket(packet)
	case packetChallenge:
		return Screenshot{}, ErrAuthenticationRequired
	case packetSSLUpgrade:
		return Screenshot{}, ErrUnsupportedUpgrade
	case packetDisconnect, packetConnectionClose:
		return Screenshot{}, fmt.Errorf("%w: server closed the connection", ErrScreenshotSession)
	case packetHello:
		return Screenshot{}, ErrScreenshotUnsupported
	default:
		return Screenshot{}, fmt.Errorf("%w: unexpected packet instead of a screenshot", ErrScreenshotSession)
	}
}

// decodeScreenshotPacket accepts
// [name, width, height, "png", rowstride, png-bytes].
func decodeScreenshotPacket(packet []any) (Screenshot, error) {
	if len(packet) != 6 {
		return Screenshot{}, fmt.Errorf("%w: malformed screenshot packet", ErrScreenshotSession)
	}
	if isEmptyScreenshot(packet) {
		return Screenshot{}, ErrEmptyScreenshot
	}
	return decodeScreenshotFields(packet)
}

// isEmptyScreenshot matches the upstream "no windows" reply:
// [name, 0, 0, "png", 0, b""].
func isEmptyScreenshot(packet []any) bool {
	width, okWidth := unsignedField(packet[1], 0)
	height, okHeight := unsignedField(packet[2], 0)
	data, okData := packet[5].([]byte)
	return okWidth && okHeight && width == 0 && height == 0 && okData && len(data) == 0
}

func decodeScreenshotFields(packet []any) (Screenshot, error) {
	width, okWidth := unsignedField(packet[1], 0xffff)
	height, okHeight := unsignedField(packet[2], 0xffff)
	encoding, okEncoding := packet[3].(string)
	rowStride, okStride := unsignedField(packet[4], 0xffffffff)
	data, okData := packet[5].([]byte)
	if !validScreenshotFields(width, height, encoding, data, okWidth, okHeight, okEncoding, okStride, okData) {
		return Screenshot{}, fmt.Errorf("%w: screenshot fields out of range", ErrScreenshotSession)
	}
	width16 := uint16(width)   // #nosec G115 -- validScreenshotFields bounds width to 8192.
	height16 := uint16(height) // #nosec G115 -- validScreenshotFields bounds height to 8192.
	// The PNG header must declare the size just checked against the caps, so
	// the decode below cannot allocate for a larger image.
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != int(width16) || config.Height != int(height16) {
		return Screenshot{}, fmt.Errorf("%w: invalid screenshot PNG dimensions", ErrScreenshotSession)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return Screenshot{}, fmt.Errorf("%w: invalid screenshot PNG data", ErrScreenshotSession)
	}
	return Screenshot{
		Width: width16, Height: height16,
		RowStride: uint32(rowStride), // #nosec G115 -- unsignedField bounds this to uint32 above.
		PNG:       append([]byte(nil), data...),
	}, nil
}

func validScreenshotFields(width, height uint64, encoding string, data []byte, okWidth, okHeight, okEncoding, okStride, okData bool) bool {
	return okWidth && okHeight && okEncoding && okStride && okData &&
		width > 0 && height > 0 && width <= MaxScreenshotDimension && height <= MaxScreenshotDimension &&
		width*height <= MaxScreenshotPixels && encoding == "png" && len(data) > 0 && len(data) <= MaxScreenshotBytes
}

func packetType(packet []any) (string, bool) {
	if len(packet) == 0 {
		return "", false
	}
	kind, ok := packet[0].(string)
	return kind, ok && kind != ""
}

func unsignedField(value any, max uint64) (uint64, bool) {
	switch number := value.(type) {
	case int64:
		if number < 0 || uint64(number) > max {
			return 0, false
		}
		return uint64(number), true
	case uint64:
		if number > max {
			return 0, false
		}
		return number, true
	default:
		return 0, false
	}
}

func sessionIOError(ctx context.Context, phase string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %s: %v", ErrScreenshotSession, phase, err)
}
