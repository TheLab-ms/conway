package discordbot

import (
	"encoding/json"
	"fmt"

	"github.com/TheLab-ms/conway/modules/members/memberdb"
)

// customIDPrefix tags interactions originating from a signup-notification
// message. The full custom_id is "conway:set_discount:<memberID>" so the
// interaction handler can route the click back to the right member without
// any extra state.
const customIDPrefix = "conway:set_discount:"

// botUsername mirrors the hardcoded username used by modules/discordwebhook
// so signup notifications appear under the same identity as other Conway
// webhook posts.
const botUsername = "Conway"

// buildSignupPayload returns the JSON body POSTed to the signup channel
// webhook. The message includes an embed describing the new member and a
// single string-select component listing every memberdb.DiscountType value.
func buildSignupPayload(memberID int64, email string) (string, error) {
	options := make([]selectOption, 0, len(memberdb.DiscountTypes))
	for _, opt := range memberdb.DiscountTypes {
		// Discord's option values must be 1-100 chars. The empty "None"
		// option needs a sentinel so it can round-trip through Discord;
		// the interaction handler treats "_none" as NULL.
		value := opt.Value
		if value == "" {
			value = noneSentinel
		}
		options = append(options, selectOption{
			Label: opt.Label,
			Value: value,
		})
	}

	payload := webhookPayload{
		Username: botUsername,
		Embeds: []embed{{
			Title:       "New member signed up",
			Description: fmt.Sprintf("**%s** just created a Conway account.\nUse the menu below to assign a discount type (or leave as **None**).", email),
			Color:       0x5865F2, // Discord blurple.
		}},
		Components: []actionRow{{
			Type: componentTypeActionRow,
			Components: []selectMenu{{
				Type:        componentTypeStringSelect,
				CustomID:    fmt.Sprintf("%s%d", customIDPrefix, memberID),
				Placeholder: "Assign a discount type...",
				MinValues:   1,
				MaxValues:   1,
				Options:     options,
			}},
		}},
	}

	out, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshaling signup payload: %w", err)
	}
	return string(out), nil
}

// noneSentinel is the Discord-side value standing in for the empty "no
// discount" option; the empty string is reserved by Discord.
const noneSentinel = "_none"

// Discord component type constants.
//
// https://discord.com/developers/docs/interactions/message-components#component-object-component-types
const (
	componentTypeActionRow    = 1
	componentTypeStringSelect = 3
)

// Interaction response types.
//
// https://discord.com/developers/docs/interactions/receiving-and-responding#interaction-response-object-interaction-callback-type
const (
	responseTypePong          = 1
	responseTypeChannelMsg    = 4
	responseTypeUpdateMessage = 7
)

// Interaction request types.
const (
	interactionTypePing     = 1
	interactionTypeComponent = 3
)

type webhookPayload struct {
	Username   string      `json:"username,omitempty"`
	Content    string      `json:"content,omitempty"`
	Embeds     []embed     `json:"embeds,omitempty"`
	Components []actionRow `json:"components,omitempty"`
}

type embed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Color       int    `json:"color,omitempty"`
}

type actionRow struct {
	Type       int          `json:"type"`
	Components []selectMenu `json:"components"`
}

type selectMenu struct {
	Type        int            `json:"type"`
	CustomID    string         `json:"custom_id"`
	Placeholder string         `json:"placeholder,omitempty"`
	MinValues   int            `json:"min_values,omitempty"`
	MaxValues   int            `json:"max_values,omitempty"`
	Options     []selectOption `json:"options"`
}

type selectOption struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	Default bool   `json:"default,omitempty"`
}
