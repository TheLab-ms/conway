package bootstrap

import (
	"context"
	"io"
	"testing"

	snaptest "github.com/TheLab-ms/conway/internal/testing"
	"github.com/a-h/templ"
)

// mockComponent creates a simple test component for testing bootstrap layouts
func mockComponent() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) (err error) {
		_, err = w.Write([]byte(`<body><h1>Test Content</h1><p>This is test content.</p></body>`))
		return err
	})
}

func TestView(t *testing.T) {
	// Test View() by creating a component that uses it with mock content
	component := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return View().Render(templ.WithChildren(ctx, mockComponent()), w)
	})
	snaptest.RenderSnapshotWithName(t, component, "")
}

func TestDarkmodeView(t *testing.T) {
	// Test DarkmodeView() by creating a component that uses it with mock content
	component := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return DarkmodeView().Render(templ.WithChildren(ctx, mockComponent()), w)
	})
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
			component := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
				return view(tt.theme).Render(templ.WithChildren(ctx, mockComponent()), w)
			})
			snaptest.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}