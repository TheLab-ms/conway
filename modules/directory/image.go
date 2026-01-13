package directory

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	_ "image/jpeg" // Register JPEG decoder

	"golang.org/x/image/draw"
)

const (
	MaxUploadSize = 20 * 1024 * 1024 // 20MB
	targetSize    = 160              // 2x for retina displays
)

// ValidImageContentType returns true if the content type is a valid image type.
func ValidImageContentType(contentType string) bool {
	return contentType == "image/jpeg" || contentType == "image/png"
}

// ProcessProfileImage takes raw image bytes, center-crops to a square,
// resizes to 160x160, and returns PNG-encoded bytes.
func ProcessProfileImage(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Center crop to square
	var cropRect image.Rectangle
	if width > height {
		offset := (width - height) / 2
		cropRect = image.Rect(bounds.Min.X+offset, bounds.Min.Y, bounds.Min.X+offset+height, bounds.Max.Y)
	} else {
		offset := (height - width) / 2
		cropRect = image.Rect(bounds.Min.X, bounds.Min.Y+offset, bounds.Max.X, bounds.Min.Y+offset+width)
	}

	// Create cropped image
	size := cropRect.Dx()
	cropped := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(cropped, cropped.Bounds(), img, cropRect.Min, draw.Src)

	// Resize to target size
	resized := image.NewRGBA(image.Rect(0, 0, targetSize, targetSize))
	draw.CatmullRom.Scale(resized, resized.Bounds(), cropped, cropped.Bounds(), draw.Over, nil)

	// Encode as PNG
	var buf bytes.Buffer
	if err := png.Encode(&buf, resized); err != nil {
		return nil, fmt.Errorf("encoding PNG: %w", err)
	}

	return buf.Bytes(), nil
}
