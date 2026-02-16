package discordwebhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/template"
)

// RenderMessage executes a Go text/template with the given data and returns
// the Discord webhook JSON payload with the rendered content as the message.
func RenderMessage(tmpl string, data any, username string) (string, error) {
	t, err := template.New("msg").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("invalid template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("template execution failed: %w", err)
	}

	content := buf.String()
	if content == "" {
		return "", fmt.Errorf("template produced empty message")
	}

	payload := map[string]string{
		"content":  content,
		"username": username,
	}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}
	return string(jsonBytes), nil
}

// SignupData holds the template context for new member signup notifications.
type SignupData struct {
	Email    string
	MemberID int64
}

// PrintCompletedData holds the template context for print completion notifications.
type PrintCompletedData struct {
	Mention     string // Discord mention string like <@user_id>, or empty
	PrinterName string
	FileName    string
}

// PrintFailedData holds the template context for print failure notifications.
type PrintFailedData struct {
	Mention     string // Discord mention string like <@user_id>, or empty
	PrinterName string
	FileName    string
	ErrorCode   string
}
