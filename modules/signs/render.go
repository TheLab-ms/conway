package signs

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/go-pdf/fpdf"
)

// SignData is the variable bag passed to a sign template's body.
// It is a string map so that templates can define arbitrary custom fields.
// The keys "DiscordHandle" and "Date" are always present; other keys come
// from the template's FieldDef definitions.
type SignData map[string]string

// RenderSign executes the template's Go-text/template body against data,
// then converts the resulting markdown into a Letter-size PDF.
func RenderSign(t Template, data SignData) ([]byte, error) {
	tmpl, err := template.New(t.Slug).Parse(t.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("executing template: %w", err)
	}
	return renderMarkdownPDF(buf.String(), t.Orientation)
}

// renderMarkdownPDF converts a small subset of markdown into a printable PDF.
//
// Supported syntax (line-based):
//   - "# ", "## ", "### "  → headings (very large for #)
//   - "---" or "***"       → horizontal rule
//   - "- " or "* "         → bullet list item
//   - blank line           → paragraph break
//   - everything else      → paragraph text
//
// Inline **bold** is rendered with bold spans. Other inline markdown is
// stripped/ignored, which is intentional: signs need to be readable, not
// rich.
func renderMarkdownPDF(md, orientation string) ([]byte, error) {
	orient := "P"
	if strings.EqualFold(orientation, "landscape") {
		orient = "L"
	}

	pdf := fpdf.New(orient, "mm", "Letter", "")
	pdf.SetMargins(15, 30, 15)
	pdf.SetAutoPageBreak(true, 15)
	pdf.AddPage()

	pageW, _ := pdf.GetPageSize()
	usableW := pageW - 30 // left+right margins

	// Conway-green accent bar across the top.
	pdf.SetFillColor(0x00, 0xC8, 0x53)
	pdf.Rect(0, 0, pageW, 8, "F")

	lines := strings.Split(md, "\n")

	var paraBuf []string
	flushPara := func() {
		text := strings.TrimSpace(strings.Join(paraBuf, " "))
		paraBuf = paraBuf[:0]
		if text == "" {
			return
		}
		pdf.SetTextColor(20, 20, 20)
		writeRichText(pdf, text, "", 18, 9, usableW)
		pdf.Ln(4)
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			flushPara()
			continue
		}
		if trimmed == "---" || trimmed == "***" {
			flushPara()
			y := pdf.GetY() + 2
			pdf.SetDrawColor(0x00, 0xC8, 0x53)
			pdf.SetLineWidth(0.6)
			pdf.Line(15, y, pageW-15, y)
			pdf.Ln(8)
			continue
		}
		if rest, ok := stripPrefix(trimmed, "### "); ok {
			flushPara()
			pdf.Ln(2)
			pdf.SetTextColor(20, 20, 20)
			writeRichText(pdf, rest, "B", 22, 10, usableW)
			pdf.Ln(2)
			continue
		}
		if rest, ok := stripPrefix(trimmed, "## "); ok {
			flushPara()
			pdf.Ln(3)
			pdf.SetTextColor(20, 20, 20)
			writeRichText(pdf, rest, "B", 32, 14, usableW)
			pdf.Ln(3)
			continue
		}
		if rest, ok := stripPrefix(trimmed, "# "); ok {
			flushPara()
			pdf.Ln(4)
			// Big, loud, accent-colored title.
			pdf.SetTextColor(0x00, 0x96, 0x3F)
			writeRichText(pdf, rest, "B", 56, 22, usableW)
			pdf.Ln(4)
			continue
		}
		if rest, ok := stripBulletPrefix(trimmed); ok {
			flushPara()
			startY := pdf.GetY()
			pdf.SetTextColor(0x00, 0xC8, 0x53)
			pdf.SetFont("Helvetica", "B", 18)
			pdf.SetXY(15, startY)
			pdf.Cell(6, 9, "•")
			pdf.SetTextColor(20, 20, 20)
			pdf.SetXY(21, startY)
			writeRichText(pdf, rest, "", 18, 9, usableW-6)
			continue
		}
		paraBuf = append(paraBuf, trimmed)
	}
	flushPara()

	// fpdf accumulates errors internally and returns them via pdf.Err()/
	// pdf.Error(). pdf.Output() can also return its own write error. Check
	// Output's error first (covers most failures), then the accumulated
	// internal error before returning the bytes.
	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("emitting pdf: %w", err)
	}
	if pdf.Err() {
		return nil, pdf.Error()
	}
	return out.Bytes(), nil
}

func stripPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(s, prefix)), true
	}
	return "", false
}

func stripBulletPrefix(s string) (string, bool) {
	for _, p := range []string{"- ", "* "} {
		if strings.HasPrefix(s, p) {
			return strings.TrimSpace(strings.TrimPrefix(s, p)), true
		}
	}
	return "", false
}

// writeRichText emits text at the current cursor with **bold** spans honored.
// baseStyle is the style applied to non-bold text ("" or "B"). baseStyle is
// upgraded to "B" inside **...** runs.
//
// Long unbreakable tokens (URLs, kernel paths, etc.) are soft-wrapped: any
// token wider than the available width is split character-by-character so it
// can't overflow the right margin.
func writeRichText(pdf *fpdf.Fpdf, text, baseStyle string, fontSize, lineH float64, width float64) {
	spans := splitBoldSpans(text)
	if len(spans) == 0 {
		return
	}

	startX := pdf.GetX()
	if startX < 15 {
		startX = 15
		pdf.SetX(startX)
	}

	// Tokenize into bold/regular words then word-wrap by measuring.
	type token struct {
		word string
		bold bool
	}
	var tokens []token
	for _, s := range spans {
		for i, w := range strings.Fields(s.text) {
			if i > 0 || len(tokens) > 0 {
				tokens = append(tokens, token{word: " ", bold: s.bold})
			}
			tokens = append(tokens, token{word: w, bold: s.bold})
		}
	}

	leftX := startX
	pdf.SetX(leftX)
	lineWidth := 0.0
	emit := func(word string, bold bool) {
		style := baseStyle
		if bold {
			style = "B"
		}
		pdf.SetFont("Helvetica", style, fontSize)
		w := pdf.GetStringWidth(word)
		if lineWidth+w > width && lineWidth > 0 {
			pdf.Ln(lineH)
			pdf.SetX(leftX)
			lineWidth = 0
			if word == " " {
				return
			}
		}
		// Soft-wrap: word still doesn't fit on a fresh line — break it up.
		if w > width && word != " " {
			for _, ch := range word {
				cw := pdf.GetStringWidth(string(ch))
				if lineWidth+cw > width && lineWidth > 0 {
					pdf.Ln(lineH)
					pdf.SetX(leftX)
					lineWidth = 0
				}
				pdf.Cell(cw, lineH, string(ch))
				lineWidth += cw
			}
			return
		}
		pdf.Cell(w, lineH, word)
		lineWidth += w
	}
	for _, tk := range tokens {
		emit(tk.word, tk.bold)
	}
	pdf.Ln(lineH)
}

// boldSpan is one segment of text with a bold flag — the unit produced by
// splitBoldSpans.
type boldSpan struct {
	text string
	bold bool
}

// splitBoldSpans splits a string into runs separated by **bold** delimiters.
// Unterminated `**` is treated as plain text so we never silently drop the
// trailing portion of a line.
func splitBoldSpans(s string) []boldSpan {
	var out []boldSpan
	rest := s
	for {
		i := strings.Index(rest, "**")
		if i < 0 {
			if rest != "" {
				out = append(out, boldSpan{text: rest})
			}
			return out
		}
		if i > 0 {
			out = append(out, boldSpan{text: rest[:i]})
		}
		rest = rest[i+2:]
		j := strings.Index(rest, "**")
		if j < 0 {
			// Unterminated bold marker: keep the literal "**" + the rest
			// as plain so the user can still see what they typed.
			out = append(out, boldSpan{text: "**" + rest})
			return out
		}
		out = append(out, boldSpan{text: rest[:j], bold: true})
		rest = rest[j+2:]
	}
}
