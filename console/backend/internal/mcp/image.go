package mcp

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/png"

	"github.com/slchris/qubes-air/console/internal/xpra"
)

var errInvalidPNG = errors.New("desktop frame is not a PNG within the frame limits")

// mimePNG is the only image type the desktop frame route serves.
const mimePNG = "image/png"

// PNGContent builds an MCP image block from one desktop frame. The bytes come
// from the Console API, which already checked them, but this process treats
// them as untrusted all the same: the size and the declared dimensions are
// checked against the same caps the Console enforces (xpra.MaxScreenshotBytes,
// MaxScreenshotDimension, MaxScreenshotPixels) before any pixel is decoded,
// and then the whole image must decode.
func PNGContent(data []byte) (ContentItem, error) {
	if len(data) == 0 || len(data) > xpra.MaxScreenshotBytes {
		return ContentItem{}, errInvalidPNG
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 ||
		config.Width > xpra.MaxScreenshotDimension || config.Height > xpra.MaxScreenshotDimension ||
		config.Width*config.Height > xpra.MaxScreenshotPixels {
		return ContentItem{}, errInvalidPNG
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return ContentItem{}, errInvalidPNG
	}
	return ContentItem{
		Type:     contentTypeImage,
		Data:     base64.StdEncoding.EncodeToString(data),
		MIMEType: mimePNG,
	}, nil
}
