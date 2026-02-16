package discordwebhook

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderMessage(t *testing.T) {
	tests := []struct {
		name         string
		tmpl         string
		replacements map[string]string
		username     string
		wantErr      bool
		wantMsg      string
	}{
		{
			name: "simple signup template",
			tmpl: `New member signed up: **{email}** (member ID: {member_id})`,
			replacements: map[string]string{
				"email":     "test@example.com",
				"member_id": "42",
			},
			username: "Conway",
			wantMsg:  `New member signed up: **test@example.com** (member ID: 42)`,
		},
		{
			name: "print completed with mention",
			tmpl: `{mention}: your print has completed successfully on {printer_name}.`,
			replacements: map[string]string{
				"mention":      "<@123456>",
				"printer_name": "Bambu X1C",
				"file_name":    "model.gcode",
			},
			username: "Conway Print Bot",
			wantMsg:  `<@123456>: your print has completed successfully on Bambu X1C.`,
		},
		{
			name: "print failed with all fields",
			tmpl: `{mention}: your print on {printer_name} has failed with error code: {error_code}. File: {file_name}`,
			replacements: map[string]string{
				"mention":      "<@123456>",
				"printer_name": "Bambu X1C",
				"error_code":   "0700 8002",
				"file_name":    "model.gcode",
			},
			username: "Conway Print Bot",
			wantMsg:  `<@123456>: your print on Bambu X1C has failed with error code: 0700 8002. File: model.gcode`,
		},
		{
			name: "print failed without mention",
			tmpl: `A print on {printer_name} has failed with error code: {error_code}.`,
			replacements: map[string]string{
				"mention":      "",
				"printer_name": "Bambu P1P",
				"error_code":   "0300 1001",
				"file_name":    "model.gcode",
			},
			username: "Conway Print Bot",
			wantMsg:  `A print on Bambu P1P has failed with error code: 0300 1001.`,
		},
		{
			name:         "empty template produces error",
			tmpl:         ``,
			replacements: map[string]string{"email": "test@example.com"},
			username:     "Conway",
			wantErr:      true,
		},
		{
			name: "template with unreferenced placeholders",
			tmpl: `Welcome {email}! Your ID is {member_id}.`,
			replacements: map[string]string{
				"email":     "user@test.com",
				"member_id": "100",
			},
			username: "Conway",
			wantMsg:  `Welcome user@test.com! Your ID is 100.`,
		},
		{
			name: "template with no matching placeholders left as-is",
			tmpl: `Hello {unknown_placeholder}!`,
			replacements: map[string]string{
				"email": "test@example.com",
			},
			username: "Conway",
			wantMsg:  `Hello {unknown_placeholder}!`,
		},
		{
			name:         "nil replacements with content",
			tmpl:         `Static message with no placeholders`,
			replacements: nil,
			username:     "Conway",
			wantMsg:      `Static message with no placeholders`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := RenderMessage(tt.tmpl, tt.replacements, tt.username)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Parse the JSON payload
			var payload map[string]string
			err = json.Unmarshal([]byte(result), &payload)
			require.NoError(t, err)

			assert.Equal(t, tt.wantMsg, payload["content"])
			assert.Equal(t, tt.username, payload["username"])
		})
	}
}
