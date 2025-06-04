package bootstrap

import (
	"testing"

	"github.com/TheLab-ms/conway/internal/templates"
	snaptest "github.com/TheLab-ms/conway/internal/testing"
)

// mockComponent creates a simple test component for testing bootstrap layouts
func mockComponent() templates.Component {
	return templates.ComponentFromString(`<body><h1>Test Content</h1><p>This is test content.</p></body>`, nil)
}

func TestView(t *testing.T) {
	// Test View() by creating a component that uses it with mock content
	component := View(mockComponent())
	snaptest.RenderSnapshotWithName(t, component, "")
}

func TestDarkmodeView(t *testing.T) {
	// Test DarkmodeView() by creating a component that uses it with mock content
	component := DarkmodeView(mockComponent())
	snaptest.RenderSnapshotWithName(t, component, "")
}

func TestViewWithTheme(t *testing.T) {
	tests := []struct {
		name        string
		theme       string
		fixtureName string
	}{
		{
			name:        "empty_theme",
			theme:       "",
			fixtureName: "_empty_theme",
		},
		{
			name:        "dark_theme",
			theme:       "dark",
			fixtureName: "_dark_theme",
		},
		{
			name:        "custom_theme",
			theme:       "custom",
			fixtureName: "_custom_theme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := view(tt.theme, mockComponent())
			snaptest.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}