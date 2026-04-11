package e2e

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

//go:embed axe.min.js
var axeScript string

// AxeViolation represents a single accessibility violation found by axe-core.
type AxeViolation struct {
	ID          string    `json:"id"`
	Impact      string    `json:"impact"`
	Description string    `json:"description"`
	Help        string    `json:"help"`
	HelpURL     string    `json:"helpUrl"`
	Tags        []string  `json:"tags"`
	Nodes       []AxeNode `json:"nodes"`
}

// AxeNode represents a specific DOM node that has an accessibility violation.
type AxeNode struct {
	HTML   string `json:"html"`
	Target []any  `json:"target"`
}

// AxeResults represents the full results from an axe-core scan.
type AxeResults struct {
	Violations []AxeViolation `json:"violations"`
	Passes     []AxeViolation `json:"passes"`
	Incomplete []AxeViolation `json:"incomplete"`
	URL        string         `json:"url"`
}

// RunAxe injects axe-core into the page and runs an accessibility scan.
// It returns the parsed results. Use AssertNoViolations for a simple pass/fail check.
func RunAxe(t *testing.T, page playwright.Page) AxeResults {
	t.Helper()

	// Inject axe-core into the page
	_, err := page.Evaluate(axeScript)
	require.NoError(t, err, "could not inject axe-core into page")

	// Run axe and get results
	result, err := page.Evaluate(`async () => {
		const results = await axe.run(document, {
			runOnly: {
				type: 'tag',
				values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice']
			}
		});
		return JSON.stringify(results);
	}`)
	require.NoError(t, err, "could not run axe-core scan")

	var axeResults AxeResults
	err = json.Unmarshal([]byte(result.(string)), &axeResults)
	require.NoError(t, err, "could not parse axe-core results")

	return axeResults
}

// AssertNoViolations runs an axe-core scan and fails the test if any
// accessibility violations are found. It logs a detailed report of each violation.
func AssertNoViolations(t *testing.T, page playwright.Page) {
	t.Helper()

	results := RunAxe(t, page)

	if len(results.Violations) == 0 {
		return
	}

	var report strings.Builder
	report.WriteString(fmt.Sprintf("\nAccessibility violations found on %s:\n", results.URL))
	report.WriteString(strings.Repeat("=", 72) + "\n")

	for i, v := range results.Violations {
		report.WriteString(fmt.Sprintf("\nViolation %d: %s [%s]\n", i+1, v.Help, v.Impact))
		report.WriteString(fmt.Sprintf("  Rule: %s\n", v.ID))
		report.WriteString(fmt.Sprintf("  Info: %s\n", v.HelpURL))
		report.WriteString(fmt.Sprintf("  Tags: %s\n", strings.Join(v.Tags, ", ")))
		report.WriteString(fmt.Sprintf("  Affected nodes (%d):\n", len(v.Nodes)))
		for j, node := range v.Nodes {
			if j >= 5 {
				report.WriteString(fmt.Sprintf("    ... and %d more\n", len(v.Nodes)-5))
				break
			}
			report.WriteString(fmt.Sprintf("    - %s\n", node.HTML))
		}
	}

	t.Fatalf("%s", report.String())
}

// AssertNoViolationsExcept runs an axe-core scan and fails the test if any
// accessibility violations are found, excluding the specified rule IDs.
// This is useful for pages with known issues that are being addressed separately.
func AssertNoViolationsExcept(t *testing.T, page playwright.Page, excludeRuleIDs ...string) {
	t.Helper()

	results := RunAxe(t, page)

	// Build a set of excluded rule IDs
	excluded := make(map[string]bool, len(excludeRuleIDs))
	for _, id := range excludeRuleIDs {
		excluded[id] = true
	}

	// Filter violations
	var filtered []AxeViolation
	for _, v := range results.Violations {
		if !excluded[v.ID] {
			filtered = append(filtered, v)
		}
	}

	if len(filtered) == 0 {
		return
	}

	var report strings.Builder
	report.WriteString(fmt.Sprintf("\nAccessibility violations found on %s:\n", results.URL))
	report.WriteString(strings.Repeat("=", 72) + "\n")

	for i, v := range filtered {
		report.WriteString(fmt.Sprintf("\nViolation %d: %s [%s]\n", i+1, v.Help, v.Impact))
		report.WriteString(fmt.Sprintf("  Rule: %s\n", v.ID))
		report.WriteString(fmt.Sprintf("  Info: %s\n", v.HelpURL))
		report.WriteString(fmt.Sprintf("  Tags: %s\n", strings.Join(v.Tags, ", ")))
		report.WriteString(fmt.Sprintf("  Affected nodes (%d):\n", len(v.Nodes)))
		for j, node := range v.Nodes {
			if j >= 5 {
				report.WriteString(fmt.Sprintf("    ... and %d more\n", len(v.Nodes)-5))
				break
			}
			report.WriteString(fmt.Sprintf("    - %s\n", node.HTML))
		}
	}

	t.Fatalf("%s", report.String())
}
