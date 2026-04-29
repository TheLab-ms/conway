package signs

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderSign_DefaultMaintenanceTemplate_ProducesPDF(t *testing.T) {
	pdf, err := RenderSign(DefaultMaintenanceTemplate, SignData{
		DiscordHandle: "@alice",
		Date:          "2025-01-15",
		MachineName:   "Bambu X1C #2",
		Issue:         "Nozzle clogged. Do not use.",
	})
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(pdf, []byte("%PDF-")), "output should be a PDF")
	require.Greater(t, len(pdf), 1000, "PDF should be non-trivial in size")
}

func TestRenderSign_TemplateExecutionError(t *testing.T) {
	bad := Template{
		Slug:        "bad",
		Name:        "Bad",
		Orientation: "portrait",
		Body:        "# Hello\n\n{{.Missing.Field}}\n",
	}
	_, err := RenderSign(bad, SignData{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "executing template")
}

func TestRenderSign_LandscapeOrientation(t *testing.T) {
	tmpl := Template{
		Slug:        "landscape",
		Name:        "Landscape",
		Orientation: "landscape",
		Body:        "# Hello\n",
	}
	pdf, err := RenderSign(tmpl, SignData{})
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(pdf, []byte("%PDF-")))
}
