package engine

import (
	"embed"
	"html/template"

	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/TheLab-ms/conway/modules/bootstrap"
)

//go:embed templates/*
var templateFS embed.FS

var (
	errorTemplate *template.Template
)

func init() {
	var err error
	errorTemplate, err = template.ParseFS(templateFS, "templates/error.html")
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