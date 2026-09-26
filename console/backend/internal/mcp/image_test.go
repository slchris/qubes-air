package mcp

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/xpra"
)

func encodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestPNGContent_BuildsAnImageBlock(t *testing.T) {
	raw := encodeTestPNG(t, 2, 1)
	block, err := PNGContent(raw)
	if err != nil {
		t.Fatalf("PNGContent: %v", err)
	}
	if block.Type != "image" || block.MIMEType != "image/png" || block.Text != "" {
		t.Fatalf("block metadata = %#v", block)
	}
	got, err := base64.StdEncoding.DecodeString(block.Data)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("image data did not round-trip: %v", err)
	}
}

func TestPNGContent_RejectsFramesOutsideTheLimits(t *testing.T) {
	var truncated bytes.Buffer
	if err := png.Encode(&truncated, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty":           nil,
		"not a PNG":       []byte("not an image"),
		"over the bytes":  bytes.Repeat([]byte("x"), xpra.MaxScreenshotBytes+1),
		"truncated":       truncated.Bytes()[:truncated.Len()-8],
		"over the pixels": encodeTestPNG(t, 4096, 1025),
		"too wide":        encodeTestPNG(t, xpra.MaxScreenshotDimension+1, 1),
		"too tall":        encodeTestPNG(t, 1, xpra.MaxScreenshotDimension+1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			block, err := PNGContent(data)
			if err == nil {
				t.Fatalf("accepted %d bytes: %#v", len(data), block)
			}
			if len(data) > 0 && strings.Contains(err.Error(), string(data[:min(len(data), 16)])) {
				t.Fatal("error echoes untrusted frame data")
			}
		})
	}
	if _, err := PNGContent(encodeTestPNG(t, 4096, 1024)); err != nil {
		t.Fatalf("a frame at the pixel cap was refused: %v", err)
	}
}
