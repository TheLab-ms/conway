package testing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RenderSnapshot tests a template component against a snapshot fixture.
// If RENDER_SNAPSHOTS environment variable is set, it will write/update the fixture file.
// Otherwise, it will compare the rendered output against the existing fixture.
func RenderSnapshot(t *testing.T, component templates.Component, fixturePath string) {
	t.Helper()

	// Render the component to string
	var buf strings.Builder
	err := component.Render(context.Background(), &buf)
	require.NoError(t, err, "Failed to render template component")

	rendered := buf.String()

	if os.Getenv("RENDER_SNAPSHOTS") != "" {
		// Write mode: create/update fixture files
		err := os.MkdirAll(filepath.Dir(fixturePath), 0755)
		require.NoError(t, err, "Failed to create fixture directory")

		err = os.WriteFile(fixturePath, []byte(rendered), 0644)
		require.NoError(t, err, "Failed to write fixture file")

		t.Logf("Updated fixture: %s", fixturePath)
		return
	}

	// Test mode: compare against existing fixture
	expected, err := os.ReadFile(fixturePath)
	require.NoError(t, err, "Failed to read fixture file: %s. Run tests with RENDER_SNAPSHOTS=1 to generate it.", fixturePath)

	assert.Equal(t, string(expected), rendered, "Rendered output does not match fixture: %s", fixturePath)
}

// RenderSnapshotWithName is a convenience function that generates a fixture path
// based on the test name and an optional suffix.
func RenderSnapshotWithName(t *testing.T, component templates.Component, suffix string) {
	t.Helper()

	testName := strings.ReplaceAll(t.Name(), "/", "_")
	fixturePath := filepath.Join("fixtures", testName+suffix+".html")
	RenderSnapshot(t, component, fixturePath)
}