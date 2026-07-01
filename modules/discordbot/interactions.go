package discordbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/TheLab-ms/conway/modules/members/memberdb"
)

// maxBodyBytes caps how much of the inbound interaction body we read. Discord
// interactions are small (a few KB at most); anything larger is suspicious.
const maxBodyBytes = 64 * 1024

// interactionRequest captures the subset of Discord's interaction object we
// care about. See
// https://discord.com/developers/docs/interactions/receiving-and-responding#interaction-object
type interactionRequest struct {
	Type   int              `json:"type"`
	Data   *interactionData `json:"data,omitempty"`
	Member *guildMember     `json:"member,omitempty"`
	User   *discordUser     `json:"user,omitempty"`
}

type interactionData struct {
	CustomID string   `json:"custom_id"`
	Values   []string `json:"values,omitempty"`
}

type guildMember struct {
	User *discordUser `json:"user,omitempty"`
}

type discordUser struct {
	ID       string `json:"id"`
	Username string `json:"username,omitempty"`
}

// interactionResponse is the JSON we write back.
type interactionResponse struct {
	Type int                      `json:"type"`
	Data *interactionResponseData `json:"data,omitempty"`
}

type interactionResponseData struct {
	Content    string      `json:"content,omitempty"`
	Embeds     []embed     `json:"embeds,omitempty"`
	Components []actionRow `json:"components"` // explicitly empty array clears the picker
	Flags      int         `json:"flags,omitempty"`
}

const flagEphemeral = 1 << 6 // 64

// handleInteraction verifies, parses, and dispatches an inbound Discord
// interaction. Discord requires that we return within 3 seconds, so the
// happy path runs entirely in-process.
func (m *Module) handleInteraction(w http.ResponseWriter, r *http.Request) {
	cfg, err := m.loadConfig(r.Context())
	if err != nil {
		slog.Error("loading discordbot config for interaction", "error", err)
		http.Error(w, "config error", http.StatusInternalServerError)
		return
	}
	if cfg.ApplicationPublicKey == "" {
		http.Error(w, "discord bot not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	sig := r.Header.Get("X-Signature-Ed25519")
	ts := r.Header.Get("X-Signature-Timestamp")
	if err := verifySignature(cfg.ApplicationPublicKey, sig, ts, body); err != nil {
		slog.Warn("rejected discord interaction with bad signature", "error", err)
		http.Error(w, "invalid request signature", http.StatusUnauthorized)
		return
	}

	var req interactionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad interaction body", http.StatusBadRequest)
		return
	}

	switch req.Type {
	case interactionTypePing:
		writeJSON(w, interactionResponse{Type: responseTypePong})
		return
	case interactionTypeComponent:
		m.handleComponent(r.Context(), w, &req)
		return
	default:
		// Unknown interaction kind. Reply with an ephemeral nudge so Discord
		// doesn't show "this interaction failed".
		writeJSON(w, interactionResponse{
			Type: responseTypeChannelMsg,
			Data: &interactionResponseData{
				Content:    "Unsupported interaction.",
				Components: []actionRow{},
				Flags:      flagEphemeral,
			},
		})
	}
}

// handleComponent approves a pending discount request from the Approve button
// on a leadership-channel notification.
func (m *Module) handleComponent(ctx context.Context, w http.ResponseWriter, req *interactionRequest) {
	if req.Data == nil || !strings.HasPrefix(req.Data.CustomID, approveCustomIDPrefix) {
		writeEphemeral(w, "Unknown component.")
		return
	}

	memberID, err := strconv.ParseInt(strings.TrimPrefix(req.Data.CustomID, approveCustomIDPrefix), 10, 64)
	if err != nil {
		writeEphemeral(w, "Malformed member ID.")
		return
	}

	// Load the member's email and current discount type/status. We only
	// approve a request that is still pending; if the member already removed
	// it (or it was approved elsewhere), we say so and clear the button.
	var email string
	var discountType *string
	var discountStatus *string
	var discountRequestID *string
	err = m.db.QueryRowContext(ctx,
		"SELECT email, discount_type, discount_status, discount_request_id FROM members WHERE id = ?", memberID).
		Scan(&email, &discountType, &discountStatus, &discountRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		writeEphemeral(w, "That member no longer exists.")
		return
	}
	if err != nil {
		slog.Error("loading member for discord interaction", "error", err, "memberID", memberID)
		writeEphemeral(w, "Database error — please try again.")
		return
	}

	discordUserID := ""
	discordUsername := ""
	if req.Member != nil && req.Member.User != nil {
		discordUserID = req.Member.User.ID
		discordUsername = req.Member.User.Username
	} else if req.User != nil {
		discordUserID = req.User.ID
		discordUsername = req.User.Username
	}

	// Approve only when still pending. The WHERE clause makes this atomic so
	// two leaders clicking at once can't double-approve.
	res, err := m.db.ExecContext(ctx,
		`UPDATE members SET discount_status = 'approved' WHERE id = ? AND discount_status = 'requested'`,
		memberID)
	if err != nil {
		slog.Error("approving discount from discord interaction", "error", err, "memberID", memberID)
		writeEphemeral(w, "Failed to save — please try again.")
		return
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		// Nothing to approve: the request was already resolved or removed.
		writeJSON(w, interactionResponse{
			Type: responseTypeUpdateMessage,
			Data: &interactionResponseData{
				Embeds: []embed{{
					Title:       "Discount request closed",
					Description: discountClosedDescription(email, discountRequestID),
					Color:       0x99AAB5, // Discord greyple.
				}},
				Components: []actionRow{}, // explicit empty = removes the button
			},
		})
		return
	}

	label := "None"
	if discountType != nil {
		label = memberdb.DiscountLabel(*discountType)
	}
	m.eventLogger.LogEvent(ctx, memberID, "DiscountApprovedViaDiscord", discordUserID, discordUsername, true,
		fmt.Sprintf("approved %q discount for %s", label, email))

	by := "Discord"
	if discordUserID != "" {
		by = fmt.Sprintf("<@%s>", discordUserID)
	}
	desc := fmt.Sprintf("**%s**\n\n**%s** discount approved by %s.", email, label, by)
	if discountRequestID != nil && *discountRequestID != "" {
		desc += fmt.Sprintf("\n\nRequest ID: `%s`", *discountRequestID)
	}
	if discountType != nil && *discountType == familyDiscountType {
		desc += "\n\n_Remember to link the root family account in the admin panel._"
	}

	// UPDATE_MESSAGE with empty components removes the Approve button so the
	// request can't be actioned twice.
	writeJSON(w, interactionResponse{
		Type: responseTypeUpdateMessage,
		Data: &interactionResponseData{
			Embeds: []embed{{
				Title:       "Discount approved",
				Description: desc,
				Color:       0x57F287, // Discord green.
			}},
			Components: []actionRow{}, // explicit empty = removes the button
		},
	})
}

func discountClosedDescription(email string, requestID *string) string {
	desc := fmt.Sprintf("**%s**\n\nThis request is no longer pending (already approved or withdrawn).", email)
	if requestID != nil && *requestID != "" {
		desc += fmt.Sprintf("\n\nRequest ID: `%s`", *requestID)
	}
	return desc
}

func writeJSON(w http.ResponseWriter, resp interactionResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encoding discord interaction response", "error", err)
	}
}

func writeEphemeral(w http.ResponseWriter, message string) {
	writeJSON(w, interactionResponse{
		Type: responseTypeChannelMsg,
		Data: &interactionResponseData{
			Content:    message,
			Components: []actionRow{},
			Flags:      flagEphemeral,
		},
	})
}
