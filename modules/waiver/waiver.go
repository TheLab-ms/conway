package waiver

import (
	"html/template"

	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/TheLab-ms/conway/modules/bootstrap"
)

var (
	waiverTemplate *template.Template
)

func init() {
	var err error
	waiverTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/waiver/templates/waiver.html")
	if err != nil {
		panic(err)
	}
}

type WaiverData struct {
	Signed   bool
	Name     string
	Email    string
	Redirect string
	HasData  bool
}

func renderWaiver(signed bool, name, email, redirect string) templates.Component {
	data := WaiverData{
		Signed:   signed,
		Name:     name,
		Email:    email,
		Redirect: redirect,
		HasData:  name != "",
	}
	
	waiverContent := &templates.TemplateComponent{
		Template: waiverTemplate,
		Data:     data,
	}
	
	return bootstrap.View(waiverContent)
}