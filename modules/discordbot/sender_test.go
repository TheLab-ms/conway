package discordbot

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/TheLab-ms/conway/modules/members/memberdb"
	"github.com/stretchr/testify/require"
)

func TestBuildSignupPayload_Shape(t *testing.T) {
	t.Parallel()
	raw, err := buildSignupPayload(42, "new@example.com")
	require.NoError(t, err)

	var payload webhookPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))

	require.Equal(t, botUsername, payload.Username)
	require.Len(t, payload.Embeds, 1)
	require.Contains(t, payload.Embeds[0].Description, "new@example.com")
	require.NotZero(t, payload.Embeds[0].Color)

	require.Len(t, payload.Components, 1, "exactly one action row")
	row := payload.Components[0]
	require.Equal(t, componentTypeActionRow, row.Type)
	require.Len(t, row.Components, 1, "exactly one select menu")

	menu := row.Components[0]
	require.Equal(t, componentTypeStringSelect, menu.Type)
	require.Equal(t, fmt.Sprintf("%s%d", customIDPrefix, int64(42)), menu.CustomID)
	require.Equal(t, 1, menu.MinValues)
	require.Equal(t, 1, menu.MaxValues)
}

func TestBuildSignupPayload_OptionsMirrorDiscountTypes(t *testing.T) {
	t.Parallel()
	raw, err := buildSignupPayload(1, "x@y.z")
	require.NoError(t, err)

	var payload webhookPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	opts := payload.Components[0].Components[0].Options

	require.Len(t, opts, len(memberdb.DiscountTypes),
		"every DiscountType must be selectable")

	// Build a map for easier assertion and check the none-sentinel mapping.
	byLabel := map[string]string{}
	for _, o := range opts {
		byLabel[o.Label] = o.Value
	}
	for _, d := range memberdb.DiscountTypes {
		got, ok := byLabel[d.Label]
		require.True(t, ok, "missing discount option label %q", d.Label)
		if d.Value == "" {
			require.Equal(t, noneSentinel, got,
				"empty discount value must round-trip as %q sentinel", noneSentinel)
		} else {
			require.Equal(t, d.Value, got)
		}
	}
}

func TestBuildSignupPayload_NoEmptyOptionValue(t *testing.T) {
	t.Parallel()
	// Discord rejects option values that are the empty string; guard
	// against accidental regressions of the sentinel substitution.
	raw, err := buildSignupPayload(1, "x@y.z")
	require.NoError(t, err)

	var payload webhookPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	for _, o := range payload.Components[0].Components[0].Options {
		require.NotEmpty(t, o.Value, "option %q has empty value", o.Label)
	}
}
