package waiver

import (
	"regexp"
	"strings"
)

// ParsedWaiver represents the parsed waiver content.
type ParsedWaiver struct {
	Title      string
	Paragraphs []string
	Checkboxes []string
}

var checkboxPattern = regexp.MustCompile(`^-\s*\[\s*\]\s*(.+)$`)

// ParseWaiverMarkdown parses waiver markdown content into structured data.
// Format:
//   - Lines starting with "# " become the title (first one wins)
//   - Lines starting with "- [ ] " define required checkboxes
//   - All other non-empty lines are grouped into paragraphs (blank lines separate)
func ParseWaiverMarkdown(content string) *ParsedWaiver {
	result := &ParsedWaiver{}
	lines := strings.Split(content, "\n")

	var currentParagraph strings.Builder

	flushParagraph := func() {
		text := strings.TrimSpace(currentParagraph.String())
		if text != "" {
			result.Paragraphs = append(result.Paragraphs, text)
		}
		currentParagraph.Reset()
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Empty line - flush current paragraph
		if trimmed == "" {
			flushParagraph()
			continue
		}

		// Title line: # Title
		if strings.HasPrefix(trimmed, "# ") && result.Title == "" {
			result.Title = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
			continue
		}

		// Checkbox line: - [ ] Label
		if match := checkboxPattern.FindStringSubmatch(trimmed); match != nil {
			flushParagraph()
			result.Checkboxes = append(result.Checkboxes, strings.TrimSpace(match[1]))
			continue
		}

		// Regular paragraph content
		if currentParagraph.Len() > 0 {
			currentParagraph.WriteString(" ")
		}
		currentParagraph.WriteString(trimmed)
	}

	flushParagraph()
	return result
}
