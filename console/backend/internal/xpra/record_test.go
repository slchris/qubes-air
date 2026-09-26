package xpra

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
)

func TestWriteReadRecordRoundTrip(t *testing.T) {
	packet := []any{"hello", map[string]any{"version": "6.6"}}
	var wire bytes.Buffer
	if err := WriteRecord(&wire, packet); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	got, err := ReadRecord(&wire)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if !reflect.DeepEqual(got, packet) {
		t.Fatalf("ReadRecord = %#v, want %#v", got, packet)
	}
}

func TestWriteRecordWireHeader(t *testing.T) {
	var wire bytes.Buffer
	if err := WriteRecord(&wire, []any{"foo"}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	want := []byte{'P', recordFlagRencode, 0, 0, 0, 0, 0, 5, 193, 131, 'f', 'o', 'o'}
	if !bytes.Equal(wire.Bytes(), want) {
		t.Fatalf("wire = %x, want %x", wire.Bytes(), want)
	}
}

func TestRecordRejectsInvalidPacketShape(t *testing.T) {
	for _, packet := range [][]any{nil, {}, {int64(1)}, {nil}, {""}} {
		if err := WriteRecord(io.Discard, packet); !errors.Is(err, ErrRecord) {
			t.Errorf("WriteRecord(%#v) error = %v, want ErrRecord", packet, err)
		}
	}
	wire := encodedRecord(t, recordFlagRencode, 0, 0, mustEncode(t, []any{int64(1)}))
	if _, err := ReadRecord(bytes.NewReader(wire)); !errors.Is(err, ErrRecord) {
		t.Fatalf("ReadRecord invalid packet type error = %v, want ErrRecord", err)
	}
}

func TestReadRecordAcceptsFlushFlag(t *testing.T) {
	payload, err := EncodeRencode([]any{"ping"})
	if err != nil {
		t.Fatalf("EncodeRencode: %v", err)
	}
	wire := encodedRecord(t, recordFlagRencode|recordFlagFlush, 0, 0, payload)
	if got, err := ReadRecord(bytes.NewReader(wire)); err != nil || !reflect.DeepEqual(got, []any{"ping"}) {
		t.Fatalf("ReadRecord = %#v, %v; want [ping], nil", got, err)
	}
}

func TestReadRecordRejectsInvalidHeaders(t *testing.T) {
	payload := mustEncode(t, []any{"p"})
	// The same payload under a valid header is accepted, so each rejection
	// below comes from its header field alone.
	if _, err := ReadRecord(bytes.NewReader(encodedRecord(t, recordFlagRencode, 0, 0, payload))); err != nil {
		t.Fatalf("ReadRecord with a valid header: %v", err)
	}
	badMagic := encodedRecord(t, recordFlagRencode, 0, 0, payload)
	badMagic[0] = 'X'
	tests := []struct {
		name   string
		wire   []byte
		reason string
	}{
		{name: "bad magic", wire: badMagic, reason: "bad magic"},
		{name: "missing encoding flag", wire: encodedRecord(t, 0, 0, 0, payload), reason: "unsupported protocol flags"},
		{name: "yaml flag", wire: encodedRecord(t, recordFlagRencode|recordFlagYAML, 0, 0, payload), reason: "unsupported protocol flags"},
		{name: "unknown flag", wire: encodedRecord(t, recordFlagRencode|0x80, 0, 0, payload), reason: "unsupported protocol flags"},
		{name: "compressed record", wire: encodedRecord(t, recordFlagRencode, 0x11, 0, payload), reason: "record compression is unsupported"},
		{name: "raw chunk", wire: encodedRecord(t, recordFlagRencode, 0, 1, payload), reason: "raw chunks are unsupported"},
		{name: "empty payload", wire: encodedRecord(t, recordFlagRencode, 0, 0, nil), reason: "payload length out of range"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadRecord(bytes.NewReader(tc.wire))
			if !errors.Is(err, ErrRecord) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("ReadRecord error = %v, want ErrRecord with %q", err, tc.reason)
			}
		})
	}
}

func TestReadRecordRejectsOversizeLengthBeforeReadingPayload(t *testing.T) {
	wire := append(declaredRecord(MaxEncodedPacket+1), make([]byte, MaxEncodedPacket+1)...)
	reader := bytes.NewReader(wire)
	_, err := ReadRecord(reader)
	if !errors.Is(err, ErrRecord) || !strings.Contains(err.Error(), "payload length out of range") {
		t.Fatalf("ReadRecord error = %v, want ErrRecord for the declared length", err)
	}
	if consumed := len(wire) - reader.Len(); consumed != recordHeaderSize {
		t.Fatalf("ReadRecord consumed %d bytes, want only the %d-byte header", consumed, recordHeaderSize)
	}
}

func TestReadRecordRejectsTruncatedAndMalformedRecords(t *testing.T) {
	payload := mustEncode(t, []any{"p"})
	tests := []struct {
		name string
		wire []byte
		want error
	}{
		{name: "truncated header", wire: []byte{'P', recordFlagRencode}, want: io.ErrUnexpectedEOF},
		{name: "no header", wire: nil, want: io.EOF},
		{name: "truncated payload", wire: encodedRecord(t, recordFlagRencode, 0, 0, payload)[:recordHeaderSize+1], want: io.ErrUnexpectedEOF},
		{name: "packet is not a list", wire: encodedRecord(t, recordFlagRencode, 0, 0, mustEncode(t, int64(0))), want: ErrRecord},
		{name: "packet type is not a string", wire: encodedRecord(t, recordFlagRencode, 0, 0, mustEncode(t, []any{int64(1)})), want: ErrRecord},
		{name: "undecodable payload", wire: encodedRecord(t, recordFlagRencode, 0, 0, []byte{terminator}), want: ErrRencode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadRecord(bytes.NewReader(tc.wire)); !errors.Is(err, tc.want) {
				t.Fatalf("ReadRecord error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReadRecordReadsSequentialRecords(t *testing.T) {
	var wire bytes.Buffer
	for _, value := range [][]any{{"first", int64(1)}, {"second", "next"}} {
		if err := WriteRecord(&wire, value); err != nil {
			t.Fatalf("WriteRecord: %v", err)
		}
	}
	for _, want := range [][]any{{"first", int64(1)}, {"second", "next"}} {
		got, err := ReadRecord(&wire)
		if err != nil {
			t.Fatalf("ReadRecord: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ReadRecord = %#v, want %#v", got, want)
		}
	}
}

func TestWriteRecordHandlesPartialAndFailedWriters(t *testing.T) {
	if err := WriteRecord(shortWriter{}, []any{"packet"}); err != nil {
		t.Fatalf("WriteRecord short writer: %v", err)
	}
	if err := WriteRecord(zeroWriter{}, []any{"packet"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("WriteRecord zero writer error = %v, want io.ErrShortWrite", err)
	}
}

func TestReadRecordAllocatesOnlyWhatThePeerSends(t *testing.T) {
	// A header that declares the maximum payload followed by a few bytes and
	// EOF must not make the reader allocate the declared 4 MiB up front.
	wire := append(declaredRecord(MaxEncodedPacket), bytes.Repeat([]byte{0}, 100)...)
	if _, err := ReadRecord(bytes.NewReader(wire)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadRecord error = %v, want io.ErrUnexpectedEOF", err)
	}
	perRun := bytesAllocatedPerRun(func() { _, _ = ReadRecord(bytes.NewReader(wire)) })
	if perRun >= MaxEncodedPacket/8 {
		t.Fatalf("ReadRecord allocated %d bytes per truncated record, want < %d", perRun, MaxEncodedPacket/8)
	}
}

// bytesAllocatedPerRun reports the mean heap bytes fn allocates. The package's
// tests do not run in parallel, so the process-wide counter is attributable.
func bytesAllocatedPerRun(fn func()) uint64 {
	const runs = 8
	fn()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		fn()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / runs
}

func TestReadRecordGrowsAcrossChunkBoundaries(t *testing.T) {
	for _, size := range []int{recordReadChunk - 16, recordReadChunk, recordReadChunk + 1, 3*recordReadChunk + 7} {
		packet := []any{"blob", bytes.Repeat([]byte{0xa5}, size)}
		var wire bytes.Buffer
		if err := WriteRecord(&wire, packet); err != nil {
			t.Fatalf("WriteRecord(%d): %v", size, err)
		}
		// HalfReader returns short reads, so every window is filled by more
		// than one Read call.
		got, err := ReadRecord(iotest.HalfReader(bytes.NewReader(wire.Bytes())))
		if err != nil {
			t.Fatalf("ReadRecord(%d): %v", size, err)
		}
		if !reflect.DeepEqual(got, packet) {
			t.Fatalf("ReadRecord(%d) returned a different packet", size)
		}
	}
}

func TestReadRecordRejectsPayloadTruncatedAfterGrowth(t *testing.T) {
	var wire bytes.Buffer
	if err := WriteRecord(&wire, []any{"blob", bytes.Repeat([]byte{1}, 3*recordReadChunk)}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	truncated := wire.Bytes()[:recordHeaderSize+2*recordReadChunk]
	if _, err := ReadRecord(bytes.NewReader(truncated)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadRecord error = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestRecordSizeLimitIsInclusive(t *testing.T) {
	// ["p", <bytes>] costs 1 (list) + 2 ("p") + 7 digits + 1 ("/") of overhead.
	const overhead = 1 + 2 + 7 + 1
	packet := []any{"p", make([]byte, MaxEncodedPacket-overhead)}
	var wire bytes.Buffer
	if err := WriteRecord(&wire, packet); err != nil {
		t.Fatalf("WriteRecord at the limit: %v", err)
	}
	if got := binary.BigEndian.Uint32(wire.Bytes()[4:recordHeaderSize]); got != MaxEncodedPacket {
		t.Fatalf("declared length = %d, want %d", got, MaxEncodedPacket)
	}
	if _, err := ReadRecord(&wire); err != nil {
		t.Fatalf("ReadRecord at the limit: %v", err)
	}
	over := []any{"p", make([]byte, MaxEncodedPacket-overhead+1)}
	if err := WriteRecord(io.Discard, over); !errors.Is(err, ErrRencode) {
		t.Fatalf("WriteRecord over the limit error = %v, want ErrRencode", err)
	}
}

func TestWriteRecordReportsWriterFailure(t *testing.T) {
	failing := errors.New("stream closed")
	if err := WriteRecord(failingWriter{err: failing}, []any{"packet"}); !errors.Is(err, failing) {
		t.Fatalf("WriteRecord error = %v, want %v", err, failing)
	}
}

func FuzzReadRecord(f *testing.F) {
	var valid bytes.Buffer
	if err := WriteRecord(&valid, []any{"hello", map[string]any{"version": "6.6"}}); err != nil {
		f.Fatalf("WriteRecord seed: %v", err)
	}
	f.Add(valid.Bytes())
	f.Add([]byte{'P', recordFlagRencode, 0, 0, 0, 0, 0, 1, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ReadRecord(bytes.NewReader(data))
	})
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return 1, nil
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func encodedRecord(t *testing.T, flags, compression, index byte, payload []byte) []byte {
	t.Helper()
	wire := declaredRecord(uint32(len(payload)))
	wire[0] = recordMagic
	wire[1] = flags
	wire[2] = compression
	wire[3] = index
	return append(wire, payload...)
}

func declaredRecord(length uint32) []byte {
	header := make([]byte, recordHeaderSize)
	header[0] = recordMagic
	header[1] = recordFlagRencode
	binary.BigEndian.PutUint32(header[4:], length)
	return header
}

func mustEncode(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := EncodeRencode(value)
	if err != nil {
		t.Fatalf("EncodeRencode: %v", err)
	}
	return encoded
}
