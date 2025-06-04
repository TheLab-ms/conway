package engine

import (
	"html/template"

	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/TheLab-ms/conway/modules/bootstrap"
)

var (
	errorTemplate *template.Template
)

func init() {
	var err error
	errorTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/engine/templates/error.html")
	if err != nil {
		panic(err)
	}
}

func renderError(e *httpError) templates.Component {
	errorContent := &templates.TemplateComponent{
		Template: errorTemplate,
		Data:     e,
	}
	return bootstrap.View(errorContent)
}