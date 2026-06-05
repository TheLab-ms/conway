package discordbot

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildRequestPayload_Shape(t *testing.T) {
	t.Parallel()
	raw, err := buildRequestPayload(42, "new@example.com", "student")
	require.NoError(t, err)

	var payload webhookPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))

	require.Equal(t, botUsername, payload.Username)
	require.Len(t, payload.Embeds, 1)
	require.Equal(t, "Discount requested", payload.Embeds[0].Title)
	require.Contains(t, payload.Embeds[0].Description, "new@example.com")
	require.Contains(t, payload.Embeds[0].Description, "Student")
	require.NotZero(t, payload.Embeds[0].Color)

	require.Len(t, payload.Components, 1, "exactly one action row")
	row := payload.Components[0]
	require.Equal(t, componentTypeActionRow, row.Type)
	require.Len(t, row.Components, 1, "exactly one Approve button")

	btn := row.Components[0]
	require.Equal(t, componentTypeButton, btn.Type)
	require.Equal(t, buttonStyleSuccess, btn.Style)
	require.Equal(t, "Approve", btn.Label)
	require.Equal(t, fmt.Sprintf("%s%d", approveCustomIDPrefix, int64(42)), btn.CustomID)
}

func TestBuildRequestPayload_FamilyMentionsLinkage(t *testing.T) {
	t.Parallel()
	raw, err := buildRequestPayload(7, "fam@example.com", "family")
	require.NoError(t, err)

	var payload webhookPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	require.Contains(t, payload.Embeds[0].Description, "root account",
		"family requests must call out the admin linkage step")
}

func TestBuildRequestPayload_NonFamilyOmitsLinkageNote(t *testing.T) {
	t.Parallel()
	raw, err := buildRequestPayload(8, "x@y.z", "military")
	require.NoError(t, err)

	var payload webhookPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	require.NotContains(t, payload.Embeds[0].Description, "root account")
}
