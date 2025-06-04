package bootstrap

import (
	"bytes"
	"context"
	"html/template"
	"io"

	"github.com/TheLab-ms/conway/internal/templates"
)

var (
	viewTemplate *template.Template
)

func init() {
	var err error
	viewTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/bootstrap/templates/view.html")
	if err != nil {
		panic(err)
	}
}

type ViewData struct {
	Theme   string
	Content template.HTML
}

// View creates a bootstrap layout with no theme
func View(content templates.Component) templates.Component {
	return view("", content)
}

// DarkmodeView creates a bootstrap layout with dark theme
func DarkmodeView(content templates.Component) templates.Component {
	return view("dark", content)
}

// view creates a bootstrap layout with the specified theme
func view(theme string, content templates.Component) templates.Component {
	return &layoutComponent{
		theme:   theme,
		content: content,
	}
}

// layoutComponent implements templates.Component for bootstrap layouts
type layoutComponent struct {
	theme   string
	content templates.Component
}

func (lc *layoutComponent) Render(ctx context.Context, w io.Writer) error {
	// First render the content to a buffer
	var contentBuf bytes.Buffer
	if err := lc.content.Render(ctx, &contentBuf); err != nil {
		return err
	}

	// Then render the layout with the content
	data := ViewData{
		Theme:   lc.theme,
		Content: template.HTML(contentBuf.String()),
	}
	
	return viewTemplate.Execute(w, data)
}