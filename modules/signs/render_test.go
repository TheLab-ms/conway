package signs

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderSign_DefaultMaintenanceTemplate_ProducesPDF(t *testing.T) {
	pdf, err := RenderSign(DefaultMaintenanceTemplate, SignData{
		"DiscordHandle": "@alice",
		"Date":          "2025-01-15",
		"MachineName":   "Bambu X1C #2",
		"Issue":         "Nozzle clogged. Do not use.",
	})
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(pdf, []byte("%PDF-")), "output should be a PDF")
	require.Greater(t, len(pdf), 1000, "PDF should be non-trivial in size")
}

func TestRenderSign_TemplateParseError(t *testing.T) {
	bad := Template{
		Slug:        "bad",
		Name:        "Bad",
		Orientation: "portrait",
		Body:        "# Hello\n\n{{.Missing | bad_func}}\n",
	}
	_, err := RenderSign(bad, SignData{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "parsing template")
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

// TestRenderSign_UnicodeContent guards against a regression where the
// renderer used fpdf's cp1252-only "Helvetica" core font and silently
// mojibaked any non-cp1252 input (smart quotes, ✉/☎ icons, emoji) into
// "â€…" gibberish. If the bundled UTF-8 font isn't registered, fpdf's
// internal error flag trips and Output returns an error, so a successful
// render is sufficient signal that the Unicode pipeline is wired up.
func TestRenderSign_UnicodeContent(t *testing.T) {
	tmpl := Template{
		Slug:        "unicode",
		Name:        "Unicode",
		Orientation: "portrait",
		Body: "# Work in progress\n\n## Sun May 3, 2026 8:12 PM\n\n" +
			"### WHAT:\n\nWorking on a project — needed to step out. " +
			"Don’t move it unless it’s been 4 hours.\n\n" +
			"### WHO:\n\n- ✉ doug.emes\n- ☎ 2145009813\n- 📞 emoji test 😀\n",
	}
	pdf, err := RenderSign(tmpl, SignData{})
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(pdf, []byte("%PDF-")))
	require.Greater(t, len(pdf), 1000)
}
