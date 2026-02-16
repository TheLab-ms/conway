package discordwebhook

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RenderMessage renders a message template by substituting {placeholder} values
// from the replacements map, then wraps the result in a Discord webhook JSON payload.
func RenderMessage(tmpl string, replacements map[string]string, username string) (string, error) {
	content := tmpl
	for key, val := range replacements {
		content = strings.ReplaceAll(content, "{"+key+"}", val)
	}

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
