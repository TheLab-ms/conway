package machines

import (
	"testing"
)

func TestParseDiscordUserID(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		// Prefix format tests
		{
			name:     "prefix: valid 17-digit ID",
			filename: "12345678901234567_benchy.gcode",
			want:     "12345678901234567",
		},
		{
			name:     "prefix: valid 18-digit ID",
			filename: "123456789012345678_benchy.gcode",
			want:     "123456789012345678",
		},
		{
			name:     "prefix: valid 19-digit ID",
			filename: "1234567890123456789_benchy.gcode",
			want:     "1234567890123456789",
		},
		{
			name:     "prefix: 3mf extension",
			filename: "123456789012345678_phone-case.3mf",
			want:     "123456789012345678",
		},
		// Suffix format tests
		{
			name:     "suffix: valid 17-digit ID",
			filename: "benchy_12345678901234567.gcode",
			want:     "12345678901234567",
		},
		{
			name:     "suffix: valid 18-digit ID",
			filename: "benchy_123456789012345678.gcode",
			want:     "123456789012345678",
		},
		{
			name:     "suffix: valid 19-digit ID",
			filename: "benchy_1234567890123456789.gcode",
			want:     "1234567890123456789",
		},
		{
			name:     "suffix: 3mf extension",
			filename: "phone-case_123456789012345678.3mf",
			want:     "123456789012345678",
		},
		{
			name:     "suffix: with dashes in name",
			filename: "my-cool-print_123456789012345678.gcode",
			want:     "123456789012345678",
		},
		// No ID tests
		{
			name:     "no ID",
			filename: "benchy.gcode",
			want:     "",
		},
		{
			name:     "too short ID prefix (16 digits)",
			filename: "1234567890123456_benchy.gcode",
			want:     "",
		},
		{
			name:     "too short ID suffix (16 digits)",
			filename: "benchy_1234567890123456.gcode",
			want:     "",
		},
		{
			name:     "too long ID prefix (20 digits)",
			filename: "12345678901234567890_benchy.gcode",
			want:     "",
		},
		{
			name:     "no underscore separator",
			filename: "123456789012345678benchy.gcode",
			want:     "",
		},
		{
			name:     "empty filename",
			filename: "",
			want:     "",
		},
		// Prefix takes precedence when both could match
		{
			name:     "prefix takes precedence",
			filename: "123456789012345678_benchy_987654321012345678.gcode",
			want:     "123456789012345678",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDiscordUserID(tt.filename)
			if got != tt.want {
				t.Errorf("parseDiscordUserID(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestStripDiscordID(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		// Prefix format
		{
			name:     "prefix: with ID",
			filename: "123456789012345678_benchy.gcode",
			want:     "benchy.gcode",
		},
		// Suffix format
		{
			name:     "suffix: with ID",
			filename: "benchy_123456789012345678.gcode",
			want:     "benchy.gcode",
		},
		{
			name:     "suffix: with dashes in name",
			filename: "my-cool-print_123456789012345678.gcode",
			want:     "my-cool-print.gcode",
		},
		{
			name:     "suffix: 3mf extension",
			filename: "phone-case_123456789012345678.3mf",
			want:     "phone-case.3mf",
		},
		// No ID
		{
			name:     "without ID",
			filename: "benchy.gcode",
			want:     "benchy.gcode",
		},
		{
			name:     "empty string",
			filename: "",
			want:     "",
		},
		{
			name:     "underscore but no valid ID",
			filename: "short_benchy.gcode",
			want:     "short_benchy.gcode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripDiscordID(tt.filename)
			if got != tt.want {
				t.Errorf("stripDiscordID(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}
