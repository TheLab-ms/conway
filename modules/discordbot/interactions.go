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
	Type   int             `json:"type"`
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
	Type int                  `json:"type"`
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

// handleComponent applies a string-select choice from the signup message.
func (m *Module) handleComponent(ctx context.Context, w http.ResponseWriter, req *interactionRequest) {
	if req.Data == nil || !strings.HasPrefix(req.Data.CustomID, customIDPrefix) {
		writeEphemeral(w, "Unknown component.")
		return
	}
	if len(req.Data.Values) == 0 {
		writeEphemeral(w, "No value provided.")
		return
	}

	memberID, err := strconv.ParseInt(strings.TrimPrefix(req.Data.CustomID, customIDPrefix), 10, 64)
	if err != nil {
		writeEphemeral(w, "Malformed member ID.")
		return
	}

	chosen := req.Data.Values[0]
	storeValue := chosen
	if chosen == noneSentinel {
		storeValue = ""
	}
	if !memberdb.IsValidDiscountType(storeValue) {
		writeEphemeral(w, fmt.Sprintf("Unknown discount option %q.", chosen))
		return
	}

	// Look up the member's email for the confirmation embed and to verify
	// the row still exists before we touch it.
	var email string
	err = m.db.QueryRowContext(ctx, "SELECT email FROM members WHERE id = ?", memberID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		writeEphemeral(w, "That member no longer exists.")
		return
	}
	if err != nil {
		slog.Error("loading member for discord interaction", "error", err, "memberID", memberID)
		writeEphemeral(w, "Database error — please try again.")
		return
	}

	// UPDATE with the same CASE pattern used by the admin form so the empty
	// string maps to NULL (matching the existing schema convention).
	_, err = m.db.ExecContext(ctx,
		`UPDATE members SET discount_type = (CASE WHEN ? = '' THEN NULL ELSE ? END) WHERE id = ?`,
		storeValue, storeValue, memberID)
	if err != nil {
		slog.Error("updating discount_type from discord interaction", "error", err, "memberID", memberID)
		writeEphemeral(w, "Failed to save — please try again.")
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

	label := memberdb.DiscountLabel(storeValue)
	m.eventLogger.LogEvent(ctx, memberID, "DiscountSetViaDiscord", discordUserID, discordUsername, true,
		fmt.Sprintf("set discount_type=%q for %s", storeValue, email))

	// UPDATE_MESSAGE with empty components removes the picker, which is how
	// we satisfy the "lock after first click" behavior the operator chose.
	by := "Discord"
	if discordUserID != "" {
		by = fmt.Sprintf("<@%s>", discordUserID)
	}
	writeJSON(w, interactionResponse{
		Type: responseTypeUpdateMessage,
		Data: &interactionResponseData{
			Embeds: []embed{{
				Title:       "New member signed up",
				Description: fmt.Sprintf("**%s**\n\nDiscount set to **%s** by %s.", email, label, by),
				Color:       0x57F287, // Discord green.
			}},
			Components: []actionRow{}, // explicit empty = removes the picker
		},
	})
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
