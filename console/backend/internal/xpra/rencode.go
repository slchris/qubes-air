// Package xpra implements a bounded subset of the Xpra wire protocol: plain
// rencodeplus records and the one-shot screenshot request. It depends only on
// the Go standard library and contains no Xpra or rencode source. Every
// length, nesting level and item count read from the peer is capped before it
// drives an allocation.
//
// Nothing in the Console calls this package yet; the consent-bound desktop
// frame endpoint that will use it is a separate change, and interoperability
// with a real Xpra server has not been verified.
package xpra

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode/utf8"
)

const (
	// MaxEncodedPacket bounds one encoded rencodeplus packet, and so every
	// string, byte string and record payload inside it.
	MaxEncodedPacket = 4 << 20
	maxNestingDepth  = 64
	maxCollectionLen = 1 << 16
	// maxLengthDigits bounds the decimal length prefix of a string or byte
	// string; MaxEncodedPacket itself has seven digits.
	maxLengthDigits = 10
	// maxIntegerDigits bounds a decimal integer: the longest int64 or uint64,
	// "-9223372036854775808" and "18446744073709551615", are 20 bytes.
	maxIntegerDigits = 20
	terminator       = byte(127)
)

// ErrRencode reports a value that is malformed, over a bound or of a type the
// codec does not support.
var ErrRencode = errors.New("invalid rencodeplus value")

// EncodeRencode serializes the scalar and collection types used by Xpra
// packets. Map keys must be strings; values are restricted to the wire types.
func EncodeRencode(value any) ([]byte, error) {
	var out bytes.Buffer
	if err := encodeRencode(&out, value, 0); err != nil {
		return nil, err
	}
	if out.Len() > MaxEncodedPacket {
		return nil, fmt.Errorf("%w: encoded packet exceeds limit", ErrRencode)
	}
	return out.Bytes(), nil
}

// DecodeRencode decodes exactly one complete value and rejects trailing bytes.
func DecodeRencode(data []byte) (any, error) {
	if len(data) == 0 || len(data) > MaxEncodedPacket {
		return nil, fmt.Errorf("%w: input size out of range", ErrRencode)
	}
	d := decoder{data: data}
	value, err := d.value(0)
	if err != nil {
		return nil, err
	}
	if d.offset != len(data) {
		return nil, fmt.Errorf("%w: trailing bytes", ErrRencode)
	}
	return value, nil
}

func encodeRencode(out *bytes.Buffer, value any, depth int) error {
	if depth > maxNestingDepth {
		return fmt.Errorf("%w: nesting limit exceeded", ErrRencode)
	}
	switch typed := value.(type) {
	case nil:
		out.WriteByte(69)
	case bool:
		if typed {
			out.WriteByte(67)
		} else {
			out.WriteByte(68)
		}
	case string:
		return encodeString(out, typed)
	case []byte:
		return encodeBytes(out, typed)
	case int64:
		return encodeInteger(out, typed)
	case uint64:
		return encodeUnsigned(out, typed)
	case float32:
		out.WriteByte(66)
		return binary.Write(out, binary.BigEndian, typed)
	case float64:
		out.WriteByte(44)
		return binary.Write(out, binary.BigEndian, typed)
	case []any:
		return encodeList(out, typed, depth)
	case map[string]any:
		return encodeMap(out, typed, depth)
	default:
		return fmt.Errorf("%w: unsupported Go type %T", ErrRencode, value)
	}
	return nil
}

func encodeString(out *bytes.Buffer, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: invalid UTF-8 string", ErrRencode)
	}
	length := len(value)
	if length > MaxEncodedPacket {
		return fmt.Errorf("%w: string too large", ErrRencode)
	}
	if length <= 63 {
		out.WriteByte(byte(128 + length))
	} else if err := writeLength(out, length, ':'); err != nil {
		return err
	}
	_, _ = out.WriteString(value)
	return nil
}

func encodeBytes(out *bytes.Buffer, value []byte) error {
	if len(value) > MaxEncodedPacket {
		return fmt.Errorf("%w: byte string too large", ErrRencode)
	}
	if err := writeLength(out, len(value), '/'); err != nil {
		return err
	}
	_, _ = out.Write(value)
	return nil
}

func writeLength(out *bytes.Buffer, length int, separator byte) error {
	if length < 0 || length > MaxEncodedPacket {
		return fmt.Errorf("%w: length out of range", ErrRencode)
	}
	_, _ = out.WriteString(strconv.Itoa(length))
	out.WriteByte(separator)
	return nil
}

func encodeInteger(out *bytes.Buffer, value int64) error {
	if value >= 0 && value <= 43 {
		out.WriteByte(byte(value))
		return nil
	}
	if value < 0 && value >= -32 {
		out.WriteByte(byte(70 + (-value - 1)))
		return nil
	}
	return writeLargeInteger(out, strconv.FormatInt(value, 10))
}

func encodeUnsigned(out *bytes.Buffer, value uint64) error {
	if value <= 43 {
		out.WriteByte(byte(value))
		return nil
	}
	return writeLargeInteger(out, strconv.FormatUint(value, 10))
}

func writeLargeInteger(out *bytes.Buffer, value string) error {
	out.WriteByte(61)
	_, _ = out.WriteString(value)
	out.WriteByte(terminator)
	return nil
}

func encodeList(out *bytes.Buffer, values []any, depth int) error {
	if len(values) > maxCollectionLen {
		return fmt.Errorf("%w: list too large", ErrRencode)
	}
	if len(values) <= 63 {
		out.WriteByte(byte(192) + byte(len(values)&63))
	} else {
		out.WriteByte(59)
	}
	for _, value := range values {
		if err := encodeRencode(out, value, depth+1); err != nil {
			return err
		}
	}
	if len(values) > 63 {
		out.WriteByte(terminator)
	}
	return nil
}

func encodeMap(out *bytes.Buffer, values map[string]any, depth int) error {
	if len(values) > maxCollectionLen {
		return fmt.Errorf("%w: dictionary too large", ErrRencode)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) <= 24 {
		out.WriteByte(byte(102) + byte(len(keys)&31))
	} else {
		out.WriteByte(60)
	}
	for _, key := range keys {
		if err := encodeString(out, key); err != nil {
			return err
		}
		if err := encodeRencode(out, values[key], depth+1); err != nil {
			return err
		}
	}
	if len(keys) > 24 {
		out.WriteByte(terminator)
	}
	return nil
}

type decoder struct {
	data   []byte
	offset int
	items  int
}

func (d *decoder) value(depth int) (any, error) {
	if depth > maxNestingDepth || d.offset >= len(d.data) {
		return nil, fmt.Errorf("%w: truncated value or nesting limit", ErrRencode)
	}
	code := d.data[d.offset]
	d.offset++
	if code >= '0' && code <= '9' {
		return d.readLengthPayload()
	}
	if code <= 43 {
		return int64(code), nil
	}
	if code >= 70 && code <= 101 {
		return int64(69) - int64(code), nil
	}
	if code >= 128 && code <= 191 {
		return d.readString(int(code - 128))
	}
	if code >= 192 {
		return d.readList(int(code-192), depth)
	}
	if code >= 102 && code <= 126 {
		return d.readMap(int(code-102), depth)
	}
	return d.extendedValue(code, depth)
}

func (d *decoder) extendedValue(code byte, depth int) (any, error) {
	switch code {
	case 44:
		return d.readFloat64()
	case 66:
		return d.readFloat32()
	case 59:
		return d.readVariableList(depth)
	case 60:
		return d.readVariableMap(depth)
	case 61:
		return d.readDecimalInteger()
	case 62:
		return d.readSigned(1)
	case 63:
		return d.readSigned(2)
	case 64:
		return d.readSigned(4)
	case 65:
		return d.readSigned(8)
	case 67:
		return true, nil
	case 68:
		return false, nil
	case 69:
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: unknown type code %d", ErrRencode, code)
	}
}

func (d *decoder) readString(length int) (any, error) {
	data, err := d.readBytes(length)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: invalid UTF-8 string", ErrRencode)
	}
	return string(data), nil
}

func (d *decoder) readLengthPayload() (any, error) {
	start := d.offset - 1
	for d.offset < len(d.data) && d.data[d.offset] >= '0' && d.data[d.offset] <= '9' {
		if d.offset-start >= maxLengthDigits {
			return nil, fmt.Errorf("%w: length digit limit exceeded", ErrRencode)
		}
		d.offset++
	}
	if d.offset >= len(d.data) || (d.data[d.offset] != ':' && d.data[d.offset] != '/') {
		return nil, fmt.Errorf("%w: malformed length prefix", ErrRencode)
	}
	separator := d.data[d.offset]
	length, err := strconv.Atoi(string(d.data[start:d.offset]))
	d.offset++
	if err != nil || length < 0 || length > MaxEncodedPacket {
		return nil, fmt.Errorf("%w: length out of range", ErrRencode)
	}
	data, err := d.readBytes(length)
	if err != nil {
		return nil, err
	}
	if separator == '/' {
		return data, nil
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: invalid UTF-8 string", ErrRencode)
	}
	return string(data), nil
}

func (d *decoder) readBytes(length int) ([]byte, error) {
	if length < 0 || length > len(d.data)-d.offset {
		return nil, fmt.Errorf("%w: truncated payload", ErrRencode)
	}
	result := d.data[d.offset : d.offset+length]
	d.offset += length
	return result, nil
}

func (d *decoder) readList(length, depth int) (any, error) {
	values := make([]any, 0, length)
	for i := 0; i < length; i++ {
		if err := d.countItem(); err != nil {
			return nil, err
		}
		value, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (d *decoder) readMap(length, depth int) (any, error) {
	values := make(map[string]any, length)
	for i := 0; i < length; i++ {
		if err := d.countItem(); err != nil {
			return nil, err
		}
		keyValue, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		key, ok := keyValue.(string)
		if !ok {
			return nil, fmt.Errorf("%w: dictionary key is not a string", ErrRencode)
		}
		value, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate dictionary key", ErrRencode)
		}
		values[key] = value
	}
	return values, nil
}

func (d *decoder) readVariableList(depth int) (any, error) {
	values := make([]any, 0)
	for {
		if d.offset >= len(d.data) {
			return nil, fmt.Errorf("%w: unterminated list", ErrRencode)
		}
		if d.data[d.offset] == terminator {
			d.offset++
			return values, nil
		}
		if err := d.countItem(); err != nil {
			return nil, err
		}
		value, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
}

func (d *decoder) readVariableMap(depth int) (any, error) {
	values := make(map[string]any)
	for {
		if d.offset >= len(d.data) {
			return nil, fmt.Errorf("%w: unterminated dictionary", ErrRencode)
		}
		if d.data[d.offset] == terminator {
			d.offset++
			return values, nil
		}
		if err := d.countItem(); err != nil {
			return nil, err
		}
		keyValue, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		key, ok := keyValue.(string)
		if !ok {
			return nil, fmt.Errorf("%w: dictionary key is not a string", ErrRencode)
		}
		value, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate dictionary key", ErrRencode)
		}
		values[key] = value
	}
}

// countItem charges one collection element (a list item or a dictionary
// pair) against a budget shared by the whole packet. The budget equals
// maxCollectionLen, so it also bounds every single collection, fixed-size or
// variable-length, without a separate per-collection check.
func (d *decoder) countItem() error {
	d.items++
	if d.items > maxCollectionLen {
		return fmt.Errorf("%w: total item limit exceeded", ErrRencode)
	}
	return nil
}

func (d *decoder) readDecimalInteger() (any, error) {
	start := d.offset
	for d.offset < len(d.data) && d.data[d.offset] != terminator {
		if d.offset-start >= maxIntegerDigits {
			return nil, fmt.Errorf("%w: integer out of range", ErrRencode)
		}
		d.offset++
	}
	if d.offset == len(d.data) || start == d.offset {
		return nil, fmt.Errorf("%w: malformed integer", ErrRencode)
	}
	text := string(d.data[start:d.offset])
	d.offset++
	if value, err := strconv.ParseInt(text, 10, 64); err == nil {
		return value, nil
	}
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: integer out of range", ErrRencode)
	}
	return value, nil
}

func (d *decoder) readSigned(size int) (any, error) {
	data, err := d.readBytes(size)
	if err != nil {
		return nil, err
	}
	switch size {
	case 1:
		value := int64(data[0])
		if value >= 1<<7 {
			value -= 1 << 8
		}
		return value, nil
	case 2:
		value := int64(binary.BigEndian.Uint16(data))
		if value >= 1<<15 {
			value -= 1 << 16
		}
		return value, nil
	case 4:
		value := int64(binary.BigEndian.Uint32(data))
		if value >= 1<<31 {
			value -= 1 << 32
		}
		return value, nil
	default:
		value := binary.BigEndian.Uint64(data)
		if value <= math.MaxInt64 {
			return int64(value), nil
		}
		return math.MinInt64 + int64(value&math.MaxInt64), nil
	}
}

func (d *decoder) readFloat64() (any, error) {
	data, err := d.readBytes(8)
	if err != nil {
		return nil, err
	}
	return math.Float64frombits(binary.BigEndian.Uint64(data)), nil
}

func (d *decoder) readFloat32() (any, error) {
	data, err := d.readBytes(4)
	if err != nil {
		return nil, err
	}
	return math.Float32frombits(binary.BigEndian.Uint32(data)), nil
}
