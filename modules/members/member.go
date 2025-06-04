package members

import (
	"bytes"
	"embed"
	"html/template"

	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/TheLab-ms/conway/modules/bootstrap"
)

//go:embed templates/*
var templateFS embed.FS

var (
	memberTemplate           *template.Template
	membershipStatusTemplate *template.Template
)

func init() {
	var err error
	memberTemplate, err = template.ParseFS(templateFS, "templates/member.html")
	if err != nil {
		panic(err)
	}
	membershipStatusTemplate, err = template.ParseFS(templateFS, "templates/membership_status.html")
	if err != nil {
		panic(err)
	}
}

type member struct {
	ID            int64
	AccessStatus  string
	DiscordLinked bool
	Email         string
}

type MemberData struct {
	DiscordLinked    bool
	MembershipStatus template.HTML
}

func renderMember(member *member) templates.Component {
	// First render the membership status
	var statusBuf bytes.Buffer
	if err := membershipStatusTemplate.Execute(&statusBuf, member); err != nil {
		panic(err)
	}

	data := MemberData{
		DiscordLinked:    member.DiscordLinked,
		MembershipStatus: template.HTML(statusBuf.String()),
	}

	memberContent := &templates.TemplateComponent{
		Template: memberTemplate,
		Data:     data,
	}

	return bootstrap.View(memberContent)
}

func renderMembershipStatus(member *member) templates.Component {
	return &templates.TemplateComponent{
		Template: membershipStatusTemplate,
		Data:     member,
	}
}