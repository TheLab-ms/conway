// Package accessdenied sends a Discord DM to members when they fob in and
// are denied access. The message explains why access was denied and how to
// fix it.
//
// Architecture:
//
//   - An AFTER INSERT trigger on fob_swipes inserts member IDs into
//     accessdenied_queue when the swipe was denied (allowed=0) and the
//     fob is associated with a known member.
//   - A polling worker drains the queue: for each member it checks whether
//     the last notification was less than 1 hour ago. If not, it looks up
//     the member's access_status to determine the denial reason, builds a
//     helpful Discord DM, and sends it via the bot REST API.
package accessdenied

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/modules/discord"
)

const migration = `
CREATE TABLE IF NOT EXISTS accessdenied_queue (
    member_id INTEGER PRIMARY KEY REFERENCES members(id) ON DELETE CASCADE,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
) STRICT;

CREATE TRIGGER IF NOT EXISTS accessdenied_on_swipe
AFTER INSERT ON fob_swipes
WHEN NEW.allowed = 0
    AND NEW.member IS NOT NULL
BEGIN
    INSERT OR IGNORE INTO accessdenied_queue (member_id) VALUES (NEW.member);
END;
`

const (
	notifyInterval = 1 * time.Hour
	botUsername    = "Conway"
)

// Module handles access-denied Discord DM notifications.
type Module struct {
	db           *sql.DB
	configLoader *config.Loader[discord.Config]
	httpClient   *http.Client

	// botTokenOverride, when non-nil, is called to get the bot token.
	// Production code uses the config loader; tests inject a stub.
	botTokenOverride func(context.Context) (string, error)
}

// New creates the module and runs migrations.
func New(db *sql.DB) *Module {
	engine.MustMigrate(db, migration)
	return &Module{
		db:         db,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetConfigLoader binds the config store to the discord module's config.
func (m *Module) SetConfigLoader(store *config.Store) {
	m.configLoader = config.NewLoader[discord.Config](store, "discord")
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(15*time.Second, engine.PollWorkqueue(m)))
}

// queueItem is one row from accessdenied_queue joined with member info.
type queueItem struct {
	MemberID      int64
	DiscordUserID string
	AccessStatus  string
	DisplayName   string
}

func (s *queueItem) String() string {
	return "access-denied:" + s.DisplayName
}

// GetItem fetches the next pending access-denied notification.
func (m *Module) GetItem(ctx context.Context) (*queueItem, error) {
	var item queueItem
	err := m.db.QueryRowContext(ctx, `
		SELECT q.member_id, m.discord_user_id, m.access_status, COALESCE(m.name_override, m.name, m.email)
		FROM accessdenied_queue q
		JOIN members m ON m.id = q.member_id
		ORDER BY q.created ASC
		LIMIT 1`).Scan(&item.MemberID, &item.DiscordUserID, &item.AccessStatus, &item.DisplayName)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// ProcessItem sends the Discord DM if the feature is enabled and
// the member hasn't been notified in the last hour.
func (m *Module) ProcessItem(ctx context.Context, item *queueItem) error {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		slog.Debug("accessdenied not configured; skipping", "memberID", item.MemberID)
		return nil
	}

	// Skip if no Discord user ID (can't DM them)
	if item.DiscordUserID == "" {
		slog.Debug("accessdenied: member has no Discord user ID; skipping",
			"memberID", item.MemberID)
		return nil
	}

	// Rate limit: skip if notified within the last hour.
	var lastNotified int64
	err = m.db.QueryRowContext(ctx, `
		SELECT COALESCE(fob_last_seen, 0)
		FROM members WHERE id = ?`, item.MemberID).Scan(&lastNotified)
	if err != nil {
		return fmt.Errorf("checking last notification for member %d: %w", item.MemberID, err)
	}
	if lastNotified > 0 && time.Now().Unix()-lastNotified < int64(notifyInterval.Seconds()) {
		slog.Debug("accessdenied: skipping recent notification",
			"memberID", item.MemberID, "lastNotified", lastNotified)
		return nil
	}

	payload, err := buildDMPayload(item.AccessStatus, item.DisplayName)
	if err != nil {
		return err
	}

	botToken, err := m.getBotToken(ctx)
	if err != nil {
		return fmt.Errorf("getting bot token: %w", err)
	}
	if botToken == "" {
		slog.Debug("accessdenied: no bot token configured; skipping")
		return nil
	}

	return m.sendDM(ctx, botToken, item.DiscordUserID, payload)
}

// UpdateItem removes the queue row after processing (success or failure).
func (m *Module) UpdateItem(ctx context.Context, item *queueItem, success bool) error {
	if !success {
		slog.Error("accessdenied: failed to send notification", "memberID", item.MemberID)
	}
	_, err := m.db.ExecContext(ctx,
		"DELETE FROM accessdenied_queue WHERE member_id = ?", item.MemberID)
	return err
}

func (m *Module) loadConfig(ctx context.Context) (*Config, error) {
	if m.configLoader == nil {
		return &Config{}, nil
	}
	dc, err := m.configLoader.Load(ctx)
	if err != nil {
		return nil, err
	}
	return &Config{
		Enabled: dc.AccessDeniedEnabled,
	}, nil
}

func (m *Module) getBotToken(ctx context.Context) (string, error) {
	if m.botTokenOverride != nil {
		return m.botTokenOverride(ctx)
	}
	if m.configLoader == nil {
		return "", nil
	}
	dc, err := m.configLoader.Load(ctx)
	if err != nil {
		return "", err
	}
	return dc.BotToken, nil
}

// apiBase is the Discord REST API base. Package-level var for test override.
var apiBase = "https://discord.com/api/v10"

// sendDM opens a DM channel with the user and sends the message.
func (m *Module) sendDM(ctx context.Context, botToken, recipientUserID, payload string) error {
	// Step 1: Create/open DM channel
	dmChannelID, err := m.createDMChannel(ctx, botToken, recipientUserID)
	if err != nil {
		return fmt.Errorf("creating DM channel: %w", err)
	}

	// Step 2: Send message to the DM channel
	url := fmt.Sprintf("%s/channels/%s/messages", apiBase, dmChannelID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBufferString(payload))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+botToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending DM: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// createDMChannel opens a DM channel with the recipient and returns the channel ID.
func (m *Module) createDMChannel(ctx context.Context, botToken, recipientUserID string) (string, error) {
	body := fmt.Sprintf(`{"recipient_id":"%s"}`, recipientUserID)
	url := fmt.Sprintf("%s/users/@me/channels", apiBase)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBufferString(body))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+botToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("creating DM channel: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("discord returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding DM channel response: %w", err)
	}
	return result.ID, nil
}

// buildDMPayload returns the JSON body for an access-denied Discord DM.
func buildDMPayload(accessStatus, displayName string) (string, error) {
	reason, fix := denialReason(accessStatus)
	content := fmt.Sprintf("Hi %s,\n\nYour fob was denied access at the makerspace.\n\n**Reason:** %s\n\n**How to fix:** %s\n\nIf you need help, please contact leadership on Discord or in person at the space.",
		displayName, reason, fix)

	payload := dmPayload{
		Content: content,
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshaling access-denied payload: %w", err)
	}
	return string(out), nil
}

// denialReason returns a human-readable reason and fix instructions
// based on the member's access_status.
func denialReason(accessStatus string) (reason, fix string) {
	switch accessStatus {
	case "UnconfirmedEmail":
		return "Your email address has not been confirmed.",
			"Check your inbox for the confirmation email and click the link. If you need a new link, contact leadership."
	case "MissingWaiver":
		return "You haven't signed the makerspace waiver.",
			"Sign the waiver at the kiosk when you visit the space, or ask leadership for help."
	case "PaymentInactive":
		return "Your membership payment is not active.",
			"Log in to your account to update your payment method, or contact leadership for assistance."
	case "MissingKeyFob":
		return "Your key fob is not registered to your account.",
			"Register your fob at the kiosk when you visit the space, or ask leadership to register it for you."
	case "FamilyInactive":
		return "Your family membership is inactive.",
			"Contact the primary account holder on your family plan to resolve this."
	default:
		return "Access is currently unavailable.",
			"Please contact leadership for assistance."
	}
}

type dmPayload struct {
	Content string `json:"content"`
}
