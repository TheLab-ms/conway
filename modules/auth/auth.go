package auth

import (
	"embed"
	"fmt"
	"html/template"
	"net/url"

	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/TheLab-ms/conway/modules/bootstrap"
)

//go:embed templates/*
var templateFS embed.FS

var (
	loginTemplate      *template.Template
	loginSentTemplate  *template.Template
	loginEmailTemplate *template.Template
)

func init() {
	var err error
	loginTemplate, err = template.ParseFS(templateFS, "templates/login.html")
	if err != nil {
		panic(err)
	}
	loginSentTemplate, err = template.ParseFS(templateFS, "templates/login_sent.html")
	if err != nil {
		panic(err)
	}
	loginEmailTemplate, err = template.ParseFS(templateFS, "templates/login_email.html")
	if err != nil {
		panic(err)
	}
}

type LoginPageData struct {
	CallbackURI      string
	TurnstileOptions *TurnstileOptions
}

type LoginEmailData struct {
	LoginURL string
	BaseURL  string
}

func renderLoginPage(callbackURI string, tso *TurnstileOptions) templates.Component {
	data := LoginPageData{
		CallbackURI:      callbackURI,
		TurnstileOptions: tso,
	}

	loginContent := &templates.TemplateComponent{
		Template: loginTemplate,
		Data:     data,
	}

	return bootstrap.View(loginContent)
}

func renderLoginSentPage() templates.Component {
	loginSentContent := &templates.TemplateComponent{
		Template: loginSentTemplate,
		Data:     nil,
	}

	return bootstrap.View(loginSentContent)
}

func renderLoginEmail(self *url.URL, token, callback string) templates.Component {
	loginURL := fmt.Sprintf("%s/login?t=%s&n=%s", self.String(), url.QueryEscape(token), url.QueryEscape(callback))

	data := LoginEmailData{
		LoginURL: loginURL,
		BaseURL:  self.String(),
	}

	return &templates.TemplateComponent{
		Template: loginEmailTemplate,
		Data:     data,
	}
}