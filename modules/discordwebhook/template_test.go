package discordwebhook

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderMessage(t *testing.T) {
	tests := []struct {
		name     string
		tmpl     string
		data     any
		username string
		wantErr  bool
		wantMsg  string
	}{
		{
			name:     "simple signup template",
			tmpl:     `New member signed up: **{{.Email}}** (member ID: {{.MemberID}})`,
			data:     SignupData{Email: "test@example.com", MemberID: 42},
			username: "Conway",
			wantMsg:  `New member signed up: **test@example.com** (member ID: 42)`,
		},
		{
			name:     "print completed with mention",
			tmpl:     `{{.Mention}}: your print has completed successfully on {{.PrinterName}}.`,
			data:     PrintCompletedData{Mention: "<@123456>", PrinterName: "Bambu X1C", FileName: "model.gcode"},
			username: "Conway Print Bot",
			wantMsg:  `<@123456>: your print has completed successfully on Bambu X1C.`,
		},
		{
			name:     "print failed with mention",
			tmpl:     `{{if .Mention}}{{.Mention}}: your{{else}}A{{end}} print on {{.PrinterName}} has failed with error code: {{.ErrorCode}}.`,
			data:     PrintFailedData{Mention: "<@123456>", PrinterName: "Bambu X1C", ErrorCode: "0700 8002", FileName: "model.gcode"},
			username: "Conway Print Bot",
			wantMsg:  `<@123456>: your print on Bambu X1C has failed with error code: 0700 8002.`,
		},
		{
			name:     "print failed without mention",
			tmpl:     `{{if .Mention}}{{.Mention}}: your{{else}}A{{end}} print on {{.PrinterName}} has failed with error code: {{.ErrorCode}}.`,
			data:     PrintFailedData{Mention: "", PrinterName: "Bambu P1P", ErrorCode: "0300 1001", FileName: "model.gcode"},
			username: "Conway Print Bot",
			wantMsg:  `A print on Bambu P1P has failed with error code: 0300 1001.`,
		},
		{
			name:     "invalid template syntax",
			tmpl:     `{{.Invalid`,
			data:     SignupData{Email: "test@example.com", MemberID: 1},
			username: "Conway",
			wantErr:  true,
		},
		{
			name:     "template produces empty output",
			tmpl:     `{{if false}}content{{end}}`,
			data:     SignupData{Email: "test@example.com", MemberID: 1},
			username: "Conway",
			wantErr:  true,
		},
		{
			name:     "custom template with all fields",
			tmpl:     `Welcome {{.Email}}! Your ID is {{.MemberID}}.`,
			data:     SignupData{Email: "user@test.com", MemberID: 100},
			username: "Conway",
			wantMsg:  `Welcome user@test.com! Your ID is 100.`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := RenderMessage(tt.tmpl, tt.data, tt.username)
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
