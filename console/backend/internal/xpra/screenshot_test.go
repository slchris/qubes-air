package xpra

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The server side of these tests follows the upstream exchange read from the
// Xpra v6.2-v6.4 and master sources: the reply to a hello carrying
// request=screenshot is the image packet alone, with no server hello.

type screenshotStream struct {
	reader  *bytes.Reader
	written bytes.Buffer
	closes  atomic.Int32
}

func newScreenshotStream(t testing.TB, packets ...[]any) *screenshotStream {
	t.Helper()
	return &screenshotStream{reader: bytes.NewReader(screenshotRecords(t, packets...))}
}

func (s *screenshotStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *screenshotStream) Write(p []byte) (int, error) { return s.written.Write(p) }
func (s *screenshotStream) Close() error {
	s.closes.Add(1)
	return nil
}

// imagePacket builds [name, width, height, "png", width*4, data], the shape of
// upstream make_screenshot_packet_from_regions.
func imagePacket(name string, width, height int64, data []byte) []any {
	return []any{name, width, height, "png", width * 4, data}
}

func TestGetScreenshotSendsOneRequestAndDecodesTheReply(t *testing.T) {
	pngData := encodePNG(t, 2, 3)
	// Released 6.x servers name the packet "screenshot"; master may use
	// "display-screenshot". Both are accepted.
	for _, name := range []string{packetScreenshot, packetDisplayScreenshot} {
		t.Run(name, func(t *testing.T) {
			stream := newScreenshotStream(t, imagePacket(name, 2, 3, pngData))
			got, err := GetScreenshot(context.Background(), stream)
			if err != nil {
				t.Fatalf("GetScreenshot: %v", err)
			}
			if got.Width != 2 || got.Height != 3 || got.RowStride != 8 || !bytes.Equal(got.PNG, pngData) {
				t.Fatalf("unexpected image: %dx%d stride %d, %d bytes", got.Width, got.Height, got.RowStride, len(got.PNG))
			}
			if n := stream.closes.Load(); n != 1 {
				t.Fatalf("stream closed %d times, want 1", n)
			}
			assertScreenshotRequest(t, &stream.written)
		})
	}
}

// assertScreenshotRequest checks that the client wrote exactly one record: a
// hello with the capabilities the upstream servers read for this request.
func assertScreenshotRequest(t *testing.T, written *bytes.Buffer) {
	t.Helper()
	packet, err := ReadRecord(written)
	if err != nil {
		t.Fatalf("read client hello: %v", err)
	}
	if written.Len() != 0 {
		t.Fatalf("client wrote %d bytes after its hello", written.Len())
	}
	if len(packet) != 2 || packet[0] != packetHello {
		t.Fatalf("client packet = %#v, want [hello, caps]", packet)
	}
	caps, ok := packet[1].(map[string]any)
	if !ok {
		t.Fatalf("client caps = %#v", packet[1])
	}
	want := map[string]any{
		"version":           xpraClientVersion,
		"request":           "screenshot",
		"ui_client":         false,
		"rencodeplus":       true,
		"compression_level": int64(0),
		"chunks":            false,
	}
	for key, value := range want {
		if caps[key] != value {
			t.Errorf("caps[%q] = %#v, want %#v", key, caps[key], value)
		}
	}
	if encoders, ok := caps["encoders"].([]any); !ok || len(encoders) != 1 || encoders[0] != "rencodeplus" {
		t.Errorf("caps[encoders] = %#v, want [rencodeplus]", caps["encoders"])
	}
	if _, present := caps["packet-types"]; present {
		t.Error("caps carry packet-types, which invites an ssl-upgrade the client cannot accept")
	}
}

func TestGetScreenshotReadsExactlyOneRecord(t *testing.T) {
	first := screenshotRecords(t, imagePacket(packetScreenshot, 1, 1, encodePNG(t, 1, 1)))
	trailing := screenshotRecords(t, []any{"ping", int64(1)})
	stream := &screenshotStream{reader: bytes.NewReader(append(first, trailing...))}
	if _, err := GetScreenshot(context.Background(), stream); err != nil {
		t.Fatalf("GetScreenshot: %v", err)
	}
	if stream.reader.Len() != len(trailing) {
		t.Fatalf("%d bytes left unread, want the %d-byte trailing record", stream.reader.Len(), len(trailing))
	}
}

func TestGetScreenshotRejectsNonImageReplies(t *testing.T) {
	reply := imagePacket(packetScreenshot, 1, 1, encodePNG(t, 1, 1))
	tests := []struct {
		name    string
		packets [][]any
		want    error
	}{
		{"challenge", [][]any{{"challenge", "salt", "", "xor"}}, ErrAuthenticationRequired},
		{"ssl upgrade", [][]any{{"ssl-upgrade", map[string]any{}}}, ErrUnsupportedUpgrade},
		{"disconnect", [][]any{{"disconnect", "permission error"}}, ErrScreenshotSession},
		{"connection close", [][]any{{"connection-close", "server shutting down"}}, ErrScreenshotSession},
		// A server hello means the request was not honored. It is rejected
		// even when an image follows it.
		{"server hello", [][]any{{"hello", map[string]any{"version": "6.4"}}, reply}, ErrScreenshotUnsupported},
		{"unexpected packet", [][]any{{"draw", int64(1)}}, ErrScreenshotSession},
		{"no reply", nil, ErrScreenshotSession},
		{"empty image", [][]any{{packetScreenshot, int64(0), int64(0), "png", int64(0), []byte{}}}, ErrEmptyScreenshot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := newScreenshotStream(t, tt.packets...)
			_, err := GetScreenshot(context.Background(), stream)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if n := stream.closes.Load(); n != 1 {
				t.Fatalf("stream closed %d times, want 1", n)
			}
		})
	}
}

func TestGetScreenshotRejectsMalformedImagePackets(t *testing.T) {
	pngData := encodePNG(t, 2, 3)
	corrupt := corruptIDAT(t, pngData)
	tests := []struct {
		name   string
		packet []any
	}{
		{"too few fields", []any{packetScreenshot, int64(2), int64(3), "png", int64(8)}},
		{"too many fields", []any{packetScreenshot, int64(2), int64(3), "png", int64(8), pngData, int64(0)}},
		{"not png", []any{packetScreenshot, int64(2), int64(3), "jpeg", int64(8), pngData}},
		{"data as string", []any{packetScreenshot, int64(2), int64(3), "png", int64(8), "png"}},
		{"zero width", []any{packetScreenshot, int64(0), int64(3), "png", int64(0), pngData}},
		{"negative height", []any{packetScreenshot, int64(2), int64(-3), "png", int64(8), pngData}},
		{"width over uint16", []any{packetScreenshot, int64(1 << 16), int64(1), "png", int64(8), pngData}},
		{"negative stride", []any{packetScreenshot, int64(2), int64(3), "png", int64(-1), pngData}},
		{"stride over uint32", []any{packetScreenshot, int64(2), int64(3), "png", uint64(math.MaxUint32) + 1, pngData}},
		{"empty data", []any{packetScreenshot, int64(2), int64(3), "png", int64(8), []byte{}}},
		{"not a png", []any{packetScreenshot, int64(2), int64(3), "png", int64(8), []byte("bad")}},
		{"packet and png disagree", []any{packetScreenshot, int64(3), int64(2), "png", int64(12), pngData}},
		{"corrupt pixel data", []any{packetScreenshot, int64(2), int64(3), "png", int64(8), corrupt}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetScreenshot(context.Background(), newScreenshotStream(t, tt.packet))
			if !errors.Is(err, ErrScreenshotSession) {
				t.Fatalf("error = %v, want ErrScreenshotSession", err)
			}
		})
	}
}

// TestGetScreenshotAcceptsRowStrideAtUint32Max pins the boundary of the
// explicit stride check: MaxUint32 is the largest value the wire field can
// carry, so it must survive the narrowing conversion unchanged.
func TestGetScreenshotAcceptsRowStrideAtUint32Max(t *testing.T) {
	pngData := encodePNG(t, 2, 3)
	packet := []any{packetScreenshot, int64(2), int64(3), "png", uint64(math.MaxUint32), pngData}
	got, err := GetScreenshot(context.Background(), newScreenshotStream(t, packet))
	if err != nil {
		t.Fatalf("GetScreenshot: %v", err)
	}
	if got.RowStride != math.MaxUint32 {
		t.Fatalf("RowStride = %d, want %d", got.RowStride, uint32(math.MaxUint32))
	}
}

// TestGetScreenshotEnforcesImageCaps uses real, decodable PNGs so that each
// case is stopped by its cap and not by an earlier check.
func TestGetScreenshotEnforcesImageCaps(t *testing.T) {
	onePixel := encodePNG(t, 1, 1)
	tests := []struct {
		name          string
		width, height int64
		data          []byte
		accept        bool
	}{
		// png.Decode ignores bytes after IEND, so the byte cap is the only
		// check that sees this padding.
		{"png exactly at the byte cap", 1, 1, padTo(onePixel, MaxScreenshotBytes), true},
		{"png one byte over the byte cap", 1, 1, padTo(onePixel, MaxScreenshotBytes+1), false},
		{"exactly the pixel cap", 4096, 1024, encodePNG(t, 4096, 1024), true},
		{"one row over the pixel cap", 4096, 1025, encodePNG(t, 4096, 1025), false},
		{"widest image at the cap", MaxScreenshotDimension, 512, encodePNG(t, MaxScreenshotDimension, 512), true},
		{"tallest image at the cap", 512, MaxScreenshotDimension, encodePNG(t, 512, MaxScreenshotDimension), true},
		{"one pixel wider than the cap", MaxScreenshotDimension + 1, 1, encodePNG(t, MaxScreenshotDimension+1, 1), false},
		{"one pixel taller than the cap", 1, MaxScreenshotDimension + 1, encodePNG(t, 1, MaxScreenshotDimension+1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := png.Decode(bytes.NewReader(tt.data)); err != nil {
				t.Fatalf("fixture is not a decodable PNG: %v", err)
			}
			got, err := GetScreenshot(context.Background(), newScreenshotStream(t, imagePacket(packetScreenshot, tt.width, tt.height, tt.data)))
			if tt.accept {
				if err != nil || int64(got.Width) != tt.width || int64(got.Height) != tt.height {
					t.Fatalf("GetScreenshot = %dx%d, %v; want %dx%d accepted", got.Width, got.Height, err, tt.width, tt.height)
				}
				return
			}
			if !errors.Is(err, ErrScreenshotSession) {
				t.Fatalf("error = %v, want ErrScreenshotSession", err)
			}
		})
	}
}

func TestGetScreenshotHonorsCancellation(t *testing.T) {
	stream := newBlockingStream()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := GetScreenshot(ctx, stream); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled session did not stop")
	}
	select {
	case <-stream.closed:
	default:
		t.Fatal("cancellation did not close stream")
	}
}

func TestGetScreenshotHonorsCallerDeadline(t *testing.T) {
	stream := newBlockingStream()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := GetScreenshot(ctx, stream)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if n := stream.closes.Load(); n != 1 {
		t.Fatalf("stream closed %d times, want 1", n)
	}
}

// TestGetScreenshotSessionCapBoundsAnOpenEndedContext proves the session cap
// with a short limit: a caller context without a deadline must not let a
// silent server hold the stream open.
func TestGetScreenshotSessionCapBoundsAnOpenEndedContext(t *testing.T) {
	stream := newBlockingStream()
	done := make(chan error, 1)
	go func() {
		_, err := getScreenshot(context.Background(), stream, 30*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		_ = stream.Close()
		t.Fatal("session outlived its cap")
	}
	if n := stream.closes.Load(); n != 1 {
		t.Fatalf("stream closed %d times, want 1", n)
	}
}

func TestGetScreenshotRejectsCanceledContextBeforeWriting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream := newScreenshotStream(t)
	if _, err := GetScreenshot(ctx, stream); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if stream.written.Len() != 0 {
		t.Fatalf("wrote %d bytes after cancellation", stream.written.Len())
	}
	if n := stream.closes.Load(); n != 1 {
		t.Fatalf("stream closed %d times, want 1", n)
	}
}

func TestGetScreenshotClosesStreamOnceWhenCanceledMidRead(t *testing.T) {
	for range 50 {
		stream := newBlockingStream()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := GetScreenshot(ctx, stream); done <- err }()
		// Cancel only once the session is blocked in Read, so the context's
		// close and the deferred close both happen.
		<-stream.reading
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if got := stream.closes.Load(); got != 1 {
			t.Fatalf("stream closed %d times, want 1", got)
		}
	}
}

func TestGetScreenshotRejectsMissingArguments(t *testing.T) {
	var missing context.Context // a nil context is the input under test
	if _, err := GetScreenshot(missing, newScreenshotStream(t)); !errors.Is(err, ErrScreenshotSession) {
		t.Fatalf("nil context error = %v, want ErrScreenshotSession", err)
	}
	if _, err := GetScreenshot(context.Background(), nil); !errors.Is(err, ErrScreenshotSession) {
		t.Fatalf("nil stream error = %v, want ErrScreenshotSession", err)
	}
}

func TestGetScreenshotReportsWriteFailure(t *testing.T) {
	stream := &failingWriteStream{}
	if _, err := GetScreenshot(context.Background(), stream); !errors.Is(err, ErrScreenshotSession) {
		t.Fatalf("error = %v, want ErrScreenshotSession", err)
	}
	if !stream.closed {
		t.Fatal("stream not closed")
	}
}

func FuzzGetScreenshot(f *testing.F) {
	pngData := encodePNG(f, 1, 1)
	for _, packet := range [][]any{
		imagePacket(packetScreenshot, 1, 1, pngData),
		imagePacket(packetDisplayScreenshot, 1, 1, pngData),
		{packetScreenshot, int64(0), int64(0), "png", int64(0), []byte{}},
		{"challenge", "salt", "", "xor"},
		{"disconnect", "permission error"},
	} {
		f.Add(screenshotRecords(f, packet))
	}
	valid := screenshotRecords(f, imagePacket(packetScreenshot, 1, 1, pngData))
	f.Add(valid[:recordHeaderSize+3])
	f.Fuzz(func(t *testing.T, data []byte) {
		shot, err := GetScreenshot(context.Background(), &screenshotStream{reader: bytes.NewReader(data)})
		if err != nil {
			return
		}
		if len(shot.PNG) == 0 || len(shot.PNG) > MaxScreenshotBytes ||
			shot.Width > MaxScreenshotDimension || shot.Height > MaxScreenshotDimension ||
			int(shot.Width)*int(shot.Height) > MaxScreenshotPixels {
			t.Fatalf("accepted out-of-bound screenshot: %dx%d, %d bytes", shot.Width, shot.Height, len(shot.PNG))
		}
	})
}

// blockingScreenshotStream blocks every Read until it is closed, like a
// server that never answers.
type blockingScreenshotStream struct {
	closed      chan struct{}
	reading     chan struct{} // closed on the first Read
	once        sync.Once
	readingOnce sync.Once
	closes      atomic.Int32
}

func newBlockingStream() *blockingScreenshotStream {
	return &blockingScreenshotStream{closed: make(chan struct{}), reading: make(chan struct{})}
}

func (s *blockingScreenshotStream) Read([]byte) (int, error) {
	s.readingOnce.Do(func() { close(s.reading) })
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *blockingScreenshotStream) Write(p []byte) (int, error) { return len(p), nil }

func (s *blockingScreenshotStream) Close() error {
	s.closes.Add(1)
	s.once.Do(func() { close(s.closed) })
	return nil
}

type failingWriteStream struct{ closed bool }

func (s *failingWriteStream) Read([]byte) (int, error)  { return 0, io.EOF }
func (s *failingWriteStream) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (s *failingWriteStream) Close() error              { s.closed = true; return nil }

func screenshotRecords(t testing.TB, packets ...[]any) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, packet := range packets {
		if err := WriteRecord(&out, packet); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

// encodePNG returns a valid grayscale PNG. A uniform image compresses to a
// few kilobytes even at the pixel cap, so the size caps can be tested apart.
func encodePNG(t testing.TB, width, height int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewGray(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// padTo appends zero bytes after the PNG's IEND chunk up to size bytes.
func padTo(data []byte, size int) []byte {
	return append(append(make([]byte, 0, size), data...), make([]byte, size-len(data))...)
}

// corruptIDAT flips a byte inside the first IDAT chunk. The header still
// parses, so only the full decode can reject the image.
func corruptIDAT(t *testing.T, data []byte) []byte {
	t.Helper()
	at := bytes.Index(data, []byte("IDAT"))
	if at < 0 {
		t.Fatal("PNG has no IDAT chunk")
	}
	out := append([]byte(nil), data...)
	out[at+4] ^= 0xff
	if _, err := png.DecodeConfig(bytes.NewReader(out)); err != nil {
		t.Fatalf("corrupted PNG no longer has a readable header: %v", err)
	}
	return out
}
