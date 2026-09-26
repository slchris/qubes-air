package xpra

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	recordHeaderSize = 8
	// recordReadChunk is the first allocation for a record payload. The buffer
	// then doubles up to the declared length, so a header that claims a large
	// payload only costs memory as the peer actually delivers the bytes.
	recordReadChunk   = 64 << 10
	recordMagic       = byte('P')
	recordFlagYAML    = byte(0x04)
	recordFlagFlush   = byte(0x08)
	recordFlagRencode = byte(0x10)
	maxRecordFlags    = recordFlagFlush | recordFlagRencode
)

// ErrRecord reports a malformed, oversized or unsupported Xpra record.
var ErrRecord = errors.New("invalid Xpra record")

// WriteRecord writes one uncompressed rencodeplus record. The limited client
// intentionally does not negotiate YAML, record compression or raw chunks.
func WriteRecord(w io.Writer, packet []any) error {
	if err := validatePacket(packet); err != nil {
		return err
	}
	payload, err := EncodeRencode(packet)
	if err != nil {
		return err
	}
	if len(payload) > MaxEncodedPacket {
		return fmt.Errorf("%w: encoded packet exceeds record limit", ErrRecord)
	}
	var header [recordHeaderSize]byte
	header[0] = recordMagic
	header[1] = recordFlagRencode
	length := uint32(len(payload)) // #nosec G115 -- payload length was bounded to 4 MiB above.
	binary.BigEndian.PutUint32(header[4:], length)
	if err := writeAll(w, header[:]); err != nil {
		return fmt.Errorf("write Xpra record header: %w", err)
	}
	if err := writeAll(w, payload); err != nil {
		return fmt.Errorf("write Xpra record payload: %w", err)
	}
	return nil
}

// ReadRecord reads one uncompressed rencodeplus record. The declared length is
// checked against MaxEncodedPacket before any payload is read, and the payload
// buffer grows only as bytes arrive.
func ReadRecord(r io.Reader) ([]any, error) {
	var header [recordHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("read Xpra record header: %w", err)
	}
	if err := validateRecordHeader(header); err != nil {
		return nil, err
	}
	payloadLength := binary.BigEndian.Uint32(header[4:])
	if payloadLength == 0 || payloadLength > MaxEncodedPacket {
		return nil, fmt.Errorf("%w: payload length out of range", ErrRecord)
	}
	payload, err := readPayload(r, int(payloadLength))
	if err != nil {
		return nil, fmt.Errorf("read Xpra record payload: %w", err)
	}
	value, err := DecodeRencode(payload)
	if err != nil {
		return nil, fmt.Errorf("decode Xpra record: %w", err)
	}
	packet, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: packet is not a list", ErrRecord)
	}
	if err := validatePacket(packet); err != nil {
		return nil, err
	}
	return packet, nil
}

// readPayload reads exactly length bytes. It starts with at most
// recordReadChunk bytes and doubles the buffer, capped at length, each time
// the previous window is full. A peer that declares a large payload and then
// stalls or closes has cost a small multiple of what it actually sent (plus
// the first chunk), never the full declared length.
func readPayload(r io.Reader, length int) ([]byte, error) {
	payload := make([]byte, 0, min(length, recordReadChunk))
	for len(payload) < length {
		if len(payload) == cap(payload) {
			grown := make([]byte, len(payload), min(length, 2*cap(payload)))
			copy(grown, payload)
			payload = grown
		}
		start := len(payload)
		payload = payload[:cap(payload)]
		if _, err := io.ReadFull(r, payload[start:]); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
	return payload, nil
}

func validatePacket(packet []any) error {
	if len(packet) == 0 {
		return fmt.Errorf("%w: empty packet", ErrRecord)
	}
	kind, ok := packet[0].(string)
	if !ok || kind == "" {
		return fmt.Errorf("%w: packet type is not a string", ErrRecord)
	}
	return nil
}

func validateRecordHeader(header [recordHeaderSize]byte) error {
	if header[0] != recordMagic {
		return fmt.Errorf("%w: bad magic", ErrRecord)
	}
	flags := header[1]
	if flags&^maxRecordFlags != 0 || flags&recordFlagRencode == 0 || flags&recordFlagYAML != 0 {
		return fmt.Errorf("%w: unsupported protocol flags", ErrRecord)
	}
	if header[2] != 0 {
		return fmt.Errorf("%w: record compression is unsupported", ErrRecord)
	}
	if header[3] != 0 {
		return fmt.Errorf("%w: raw chunks are unsupported", ErrRecord)
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := w.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
