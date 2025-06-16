package waiver

import (
	"testing"

	"github.com/TheLab-ms/conway/engine/testutil"
)

func TestRenderWaiver(t *testing.T) {
	tests := []struct {
		name        string
		signed      bool
		name_param  string
		email       string
		redirect    string
		fixtureName string
		description string
	}{
		{
			name:        "unsigned_waiver_empty",
			signed:      false,
			name_param:  "",
			email:       "",
			redirect:    "",
			fixtureName: "_unsigned_empty",
			description: "Empty waiver form for new user",
		},
		{
			name:        "unsigned_waiver_with_data",
			signed:      false,
			name_param:  "John Doe",
			email:       "john@example.com",
			redirect:    "/dashboard",
			fixtureName: "_unsigned_with_data",
			description: "Waiver form pre-filled with user data",
		},
		{
			name:        "signed_waiver_no_redirect",
			signed:      true,
			name_param:  "Jane Smith",
			email:       "jane@example.com",
			redirect:    "",
			fixtureName: "_signed_no_redirect",
			description: "Successfully signed waiver without redirect",
		},
		{
			name:        "signed_waiver_with_redirect",
			signed:      true,
			name_param:  "Bob Johnson",
			email:       "bob@example.com",
			redirect:    "/kiosk",
			fixtureName: "_signed_with_redirect",
			description: "Successfully signed waiver with redirect to kiosk",
		},
		{
			name:        "signed_waiver_complex_redirect",
			signed:      true,
			name_param:  "Alice Wilson",
			email:       "alice@example.com",
			redirect:    "/admin/members?filter=new",
			fixtureName: "_signed_complex_redirect",
			description: "Successfully signed waiver with complex redirect URL",
		},
		{
			name:        "unsigned_special_chars",
			signed:      false,
			name_param:  "José García-López",
			email:       "jose.garcia+test@example.com",
			redirect:    "/test?param=value&other=123",
			fixtureName: "_unsigned_special_chars",
			description: "Waiver form with special characters and complex redirect",
		},
		{
			name:        "signed_special_chars",
			signed:      true,
			name_param:  "Marie-Claire O'Connor",
			email:       "marie.claire@example.co.uk",
			redirect:    "/success?msg=waiver_complete",
			fixtureName: "_signed_special_chars",
			description: "Signed waiver with special characters in name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderWaiver(tt.signed, tt.name_param, tt.email, tt.redirect)
			testutil.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}
