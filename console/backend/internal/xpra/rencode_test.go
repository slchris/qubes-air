package xpra

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestRencodeRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{name: "booleans and null", value: []any{true, false, nil}},
		{name: "integer boundaries", value: []any{int64(-32), int64(-1), int64(0), int64(43), int64(-33), int64(44), uint64(math.MaxUint64)}},
		{name: "floats", value: []any{float32(1.5), float64(-2.25)}},
		{name: "short and variable strings", value: []any{"", "hello", string(bytes.Repeat([]byte("a"), 64))}},
		{name: "binary", value: []any{[]byte{}, []byte{0, 0xff}, bytes.Repeat([]byte{0xfe}, 300)}},
		{name: "short and variable collections", value: map[string]any{
			"items": []any{int64(1), "value", []byte{1, 2}},
			"null":  nil,
		}},
		{name: "variable list", value: anyList(64)},
		{name: "variable map", value: stringMap(25)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := EncodeRencode(tc.value)
			if err != nil {
				t.Fatalf("EncodeRencode: %v", err)
			}
			decoded, err := DecodeRencode(encoded)
			if err != nil {
				t.Fatalf("DecodeRencode: %v", err)
			}
			if !reflect.DeepEqual(decoded, tc.value) {
				t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", decoded, tc.value)
			}
		})
	}
}

func TestRencodeMatchesModernWireVectors(t *testing.T) {
	cases := []struct {
		value any
		wire  []byte
	}{
		{value: int64(0), wire: []byte{0}},
		{value: int64(-1), wire: []byte{70}},
		{value: "foo", wire: []byte{131, 'f', 'o', 'o'}},
		{value: []byte{0xff}, wire: []byte{'1', '/', 0xff}},
		{value: []any{true, false}, wire: []byte{194, 67, 68}},
		{value: map[string]any{}, wire: []byte{102}},
	}
	for _, tc := range cases {
		got, err := EncodeRencode(tc.value)
		if err != nil {
			t.Fatalf("EncodeRencode(%#v): %v", tc.value, err)
		}
		if !bytes.Equal(got, tc.wire) {
			t.Errorf("EncodeRencode(%#v) = %x, want %x", tc.value, got, tc.wire)
		}
	}
}

func TestDecodeRencodeRejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "unknown tag", data: []byte{127}},
		{name: "trailing value", data: []byte{0, 0}},
		{name: "truncated short string", data: []byte{131, 'a'}},
		{name: "truncated long bytes", data: []byte{'2', '/', 1}},
		{name: "bad length", data: []byte{'x', '/', 1}},
		{name: "invalid UTF-8", data: []byte{129, 0xff}},
		{name: "unterminated integer", data: []byte{61, '1'}},
		{name: "unterminated list", data: []byte{59, 0}},
		{name: "non-string map key", data: []byte{103, 0, 0}},
		{name: "duplicate map key", data: []byte{104, 129, 'a', 0, 129, 'a', 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeRencode(tc.data); !errors.Is(err, ErrRencode) {
				t.Fatalf("DecodeRencode error = %v, want ErrRencode", err)
			}
		})
	}
}

func TestDecodeSignedIntegerWireTags(t *testing.T) {
	cases := []struct {
		name string
		wire []byte
		want int64
	}{
		{name: "signed 8 bit", wire: []byte{62, 0xff}, want: -1},
		{name: "signed 16 bit", wire: []byte{63, 0xff, 0xff}, want: -1},
		{name: "signed 32 bit", wire: []byte{64, 0xff, 0xff, 0xff, 0xff}, want: -1},
		{name: "signed 64 bit negative one", wire: []byte{65, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, want: -1},
		{name: "signed 64 bit minimum", wire: []byte{65, 0x80, 0, 0, 0, 0, 0, 0, 0}, want: math.MinInt64},
		{name: "signed 64 bit maximum", wire: []byte{65, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, want: math.MaxInt64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeRencode(tc.wire)
			if err != nil {
				t.Fatalf("DecodeRencode: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DecodeRencode = %#v, want %d", got, tc.want)
			}
		})
	}
}

func TestRencodeBoundsAndTypes(t *testing.T) {
	if _, err := EncodeRencode(make([]byte, MaxEncodedPacket+1)); !errors.Is(err, ErrRencode) {
		t.Fatalf("oversize encode error = %v, want ErrRencode", err)
	}
	if _, err := EncodeRencode(struct{}{}); !errors.Is(err, ErrRencode) {
		t.Fatalf("unsupported type error = %v, want ErrRencode", err)
	}
	if _, err := DecodeRencode(make([]byte, MaxEncodedPacket+1)); !errors.Is(err, ErrRencode) {
		t.Fatalf("oversize decode error = %v, want ErrRencode", err)
	}
	deep := any(nil)
	for range maxNestingDepth + 2 {
		deep = []any{deep}
	}
	if _, err := EncodeRencode(deep); !errors.Is(err, ErrRencode) {
		t.Fatalf("deep encode error = %v, want ErrRencode", err)
	}
}

func TestDecodeRencodeEnforcesLengthAndIntegerBounds(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{name: "length prefix over digit limit", data: append(bytes.Repeat([]byte{'0'}, maxLengthDigits+1), '/')},
		{name: "length prefix digits fill the packet", data: bytes.Repeat([]byte{'9'}, MaxEncodedPacket)},
		{name: "declared length above packet limit", data: []byte("4194305/")},
		{name: "declared length beyond data", data: []byte("5:abc")},
		{name: "integer over digit limit", data: append(append([]byte{61}, bytes.Repeat([]byte{'1'}, maxIntegerDigits+1)...), terminator)},
		{name: "integer digits fill the packet", data: append([]byte{61}, bytes.Repeat([]byte{'1'}, MaxEncodedPacket-1)...)},
		{name: "integer above uint64", data: append(append([]byte{61}, "18446744073709551616"...), terminator)},
		{name: "integer below int64", data: append(append([]byte{61}, "-9223372036854775809"...), terminator)},
		{name: "empty integer", data: []byte{61, terminator}},
		{name: "non-decimal integer", data: []byte{61, 'x', terminator}},
		{name: "truncated float64", data: []byte{44, 0, 0}},
		{name: "truncated float32", data: []byte{66, 0}},
		{name: "truncated signed 16 bit", data: []byte{63, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeRencode(tc.data); !errors.Is(err, ErrRencode) {
				t.Fatalf("DecodeRencode error = %v, want ErrRencode", err)
			}
		})
	}
}

func TestDecodeRencodeRejectsLongDigitRunsWithoutCopying(t *testing.T) {
	// A packet-sized run of digits must fail at the digit cap instead of being
	// copied into a string first.
	for name, data := range map[string][]byte{
		"integer":       append(append([]byte{61}, bytes.Repeat([]byte{'1'}, MaxEncodedPacket-2)...), terminator),
		"length prefix": bytes.Repeat([]byte{'9'}, MaxEncodedPacket),
	} {
		perRun := bytesAllocatedPerRun(func() { _, _ = DecodeRencode(data) })
		if perRun >= 64<<10 {
			t.Errorf("%s: DecodeRencode allocated %d bytes per run, want < %d", name, perRun, 64<<10)
		}
	}
}

func TestDecodeRencodeAcceptsIntegerDigitLimit(t *testing.T) {
	cases := []struct {
		text string
		want any
	}{
		{text: "-9223372036854775808", want: int64(math.MinInt64)},
		{text: "18446744073709551615", want: uint64(math.MaxUint64)},
		{text: "44", want: int64(44)},
	}
	for _, tc := range cases {
		data := append(append([]byte{61}, tc.text...), terminator)
		got, err := DecodeRencode(data)
		if err != nil {
			t.Fatalf("DecodeRencode(%s): %v", tc.text, err)
		}
		if got != tc.want {
			t.Fatalf("DecodeRencode(%s) = %#v, want %#v", tc.text, got, tc.want)
		}
	}
}

func TestDecodeRencodeEnforcesCollectionAndDepthBounds(t *testing.T) {
	variableList := func(items int) []byte {
		data := append([]byte{59}, make([]byte, items)...)
		return append(data, terminator)
	}
	// Two variable lists stay under the per-list cap but together exceed the
	// total item budget of one packet.
	half := maxCollectionLen/2 + 1
	twoLists := append([]byte{194}, variableList(half)...)
	twoLists = append(twoLists, variableList(half)...)
	nested := append(bytes.Repeat([]byte{193}, maxNestingDepth+1), 0)
	cases := []struct {
		name string
		data []byte
	}{
		{name: "variable list over item cap", data: variableList(maxCollectionLen + 1)},
		{name: "items across lists over total cap", data: twoLists},
		{name: "nesting over depth cap", data: nested},
		{name: "variable map with non-string key", data: []byte{60, 0, 0, terminator}},
		{name: "variable map duplicate key", data: []byte{60, 129, 'a', 0, 129, 'a', 1, terminator}},
		{name: "unterminated variable map", data: []byte{60, 129, 'a', 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeRencode(tc.data); !errors.Is(err, ErrRencode) {
				t.Fatalf("DecodeRencode error = %v, want ErrRencode", err)
			}
		})
	}
	if _, err := DecodeRencode(variableList(maxCollectionLen)); err != nil {
		t.Fatalf("DecodeRencode at the item cap: %v", err)
	}
	if _, err := DecodeRencode(append(bytes.Repeat([]byte{193}, maxNestingDepth), 0)); err != nil {
		t.Fatalf("DecodeRencode at the depth cap: %v", err)
	}
}

func TestEncodeRencodeRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{name: "invalid UTF-8 string", value: string([]byte{0xff})},
		{name: "invalid UTF-8 map key", value: map[string]any{string([]byte{0xff}): nil}},
		{name: "list over item cap", value: make([]any, maxCollectionLen+1)},
		{name: "untyped int", value: []any{1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EncodeRencode(tc.value); !errors.Is(err, ErrRencode) {
				t.Fatalf("EncodeRencode error = %v, want ErrRencode", err)
			}
		})
	}
}

func FuzzDecodeRencode(f *testing.F) {
	for _, seed := range [][]byte{{0}, {131, 'f', 'o', 'o'}, {59, 67, 68, 127}, {102}, {61, '1', 127}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeRencode(data)
	})
}

func anyList(length int) []any {
	values := make([]any, length)
	for i := range values {
		values[i] = int64(i)
	}
	return values
}

func stringMap(length int) map[string]any {
	values := make(map[string]any, length)
	for i := range length {
		values["key-"+string(rune('a'+i))] = int64(i)
	}
	return values
}
