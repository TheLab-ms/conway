package discordbot

import (
	"encoding/json"
	"fmt"

	"github.com/TheLab-ms/conway/modules/members/memberdb"
)

// approveCustomIDPrefix tags interactions originating from a discount-request
// notification. The full custom_id is "conway:approve_discount:<memberID>" so
// the interaction handler can route the click back to the right member
// without any extra state.
const approveCustomIDPrefix = "conway:approve_discount:"

// botUsername mirrors the hardcoded username used by modules/discordwebhook
// so notifications appear under the same identity as other Conway webhook
// posts.
const botUsername = "Conway"

// familyDiscountType is the one discount tier that needs a root-account
// linkage leadership can only complete in the admin panel; the Discord
// approval embed calls this out explicitly.
const familyDiscountType = "family"

// buildRequestPayload returns the JSON body POSTed to the leadership channel
// webhook when a member requests a discount. The message includes an embed
// describing the request and a single Approve button. There is intentionally
// no deny/cancel control: leadership can only approve, and members remove
// their own requests.
func buildRequestPayload(memberID int64, email, discountType string) (string, error) {
	label := memberdb.DiscountLabel(discountType)

	desc := fmt.Sprintf("**%s** requested the **%s** discount.\nClick **Approve** to apply it.", email, label)
	if discountType == familyDiscountType {
		desc += "\n\n_Family discounts must also be linked to a root account in the admin panel._"
	}

	payload := webhookPayload{
		Username: botUsername,
		Embeds: []embed{{
			Title:       "Discount requested",
			Description: desc,
			Color:       0x5865F2, // Discord blurple.
		}},
		Components: []actionRow{{
			Type: componentTypeActionRow,
			Components: []button{{
				Type:     componentTypeButton,
				Style:    buttonStyleSuccess,
				Label:    "Approve",
				CustomID: fmt.Sprintf("%s%d", approveCustomIDPrefix, memberID),
			}},
		}},
	}

	out, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshaling discount-request payload: %w", err)
	}
	return string(out), nil
}

// Discord component type constants.
//
// https://discord.com/developers/docs/interactions/message-components#component-object-component-types
const (
	componentTypeActionRow = 1
	componentTypeButton    = 2
)

// Discord button style constants.
//
// https://discord.com/developers/docs/interactions/message-components#button-object-button-styles
const (
	buttonStyleSuccess = 3
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
	interactionTypePing      = 1
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
	Type       int      `json:"type"`
	Components []button `json:"components"`
}

type button struct {
	Type     int    `json:"type"`
	Style    int    `json:"style"`
	Label    string `json:"label"`
	CustomID string `json:"custom_id"`
}
