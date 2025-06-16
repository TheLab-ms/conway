package kiosk

import (
	"encoding/base64"
	"testing"

	"github.com/TheLab-ms/conway/engine/testutil"
)

func TestRenderOffsiteError(t *testing.T) {
	component := renderOffsiteError()
	testutil.RenderSnapshotWithName(t, component, "")
}

func TestRenderKiosk(t *testing.T) {
	tests := []struct {
		name        string
		qrImg       []byte
		fixtureName string
		description string
	}{
		{
			name:        "no_qr_code",
			qrImg:       nil,
			fixtureName: "_no_qr",
			description: "Test kiosk view with no QR code (welcome screen)",
		},
		{
			name:        "with_qr_code",
			qrImg:       []byte("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="), // 1x1 transparent PNG in base64
			fixtureName: "_with_qr",
			description: "Test kiosk view with QR code for fob linking",
		},
		{
			name:        "empty_qr_bytes",
			qrImg:       []byte{},
			fixtureName: "_empty_qr",
			description: "Test kiosk view with empty byte slice (should show QR screen)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderKiosk(tt.qrImg)
			testutil.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}

func TestRenderKioskLargeQR(t *testing.T) {
	// Test with a larger QR code to ensure proper rendering
	largePNG := make([]byte, 1024)
	for i := range largePNG {
		largePNG[i] = byte(i % 256)
	}
	encodedPNG := make([]byte, base64.StdEncoding.EncodedLen(len(largePNG)))
	base64.StdEncoding.Encode(encodedPNG, largePNG)

	component := renderKiosk(encodedPNG)
	testutil.RenderSnapshotWithName(t, component, "_large_qr")
}
