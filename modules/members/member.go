package members

import (
	"bytes"
	"html/template"

	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/TheLab-ms/conway/modules/bootstrap"
)

var (
	memberTemplate           *template.Template
	membershipStatusTemplate *template.Template
)

func init() {
	var err error
	memberTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/members/templates/member.html")
	if err != nil {
		panic(err)
	}
	membershipStatusTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/members/templates/membership_status.html")
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