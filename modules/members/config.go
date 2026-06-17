package members

import "github.com/TheLab-ms/conway/engine/config"

// ReferralSource is a member signup survey option.
type ReferralSource struct {
	Label string `json:"label" config:"label=Choice,required,placeholder=e.g. Friend or member"`
}

// Config holds member-facing signup settings.
type Config struct {
	ReferralSources []ReferralSource `json:"referral_sources" config:"label=Referral Sources,item=Referral Source,key=Label"`
}

// ConfigSpec returns the Members configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module: "members",
		Title:  "Members",
		Type:   Config{},
		ArrayFields: []config.ArrayFieldDef{
			{
				FieldName: "ReferralSources",
				Label:     "Signup Referral Sources",
				ItemLabel: "Source",
				Help:      "Choices shown to new members when they create an account. The member profile stores the selected text, so renaming or deleting a choice does not change existing answers.",
				KeyField:  "Label",
			},
		},
		Order: 10,
	}
}
