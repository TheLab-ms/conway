// Package badgenotify posts a Discord message when an opted-in member badges
// into the makerspace. It rate-limits to one notification per member every 4
// hours to avoid flapping.
//
// Architecture:
//
//   - An AFTER INSERT trigger on fob_swipes inserts member IDs into
//     badgenotify_queue when the member has discord_checkin_notify enabled.
//   - A polling worker drains the queue: for each member it checks whether
//     the last notification was less than 4 hours ago. If not, it builds a
//     Discord embed and forwards it to discordwebhook for delivery.
//   - The config (stored in discord_config) controls whether the feature
//     is enabled and which channel receives the messages.
package badgenotify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/modules/discord"
	"github.com/TheLab-ms/conway/modules/discordwebhook"
)

const migration = `
CREATE TABLE IF NOT EXISTS badgenotify_queue (
    member_id INTEGER PRIMARY KEY REFERENCES members(id) ON DELETE CASCADE,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
) STRICT;

CREATE TRIGGER IF NOT EXISTS badgenotify_on_swipe
AFTER INSERT ON fob_swipes
WHEN NEW.member IS NOT NULL
    AND (SELECT COALESCE(discord_checkin_notify, 0) FROM members WHERE id = NEW.member) = 1
BEGIN
    INSERT OR IGNORE INTO badgenotify_queue (member_id) VALUES (NEW.member);
END;
`

const (
	// notifyInterval is the minimum time between notifications for the same member.
	notifyInterval = 4 * time.Hour

	botUsername = "Conway"
)

// Module handles badge-in Discord notifications.
type Module struct {
	db           *sql.DB
	webhooks     discordwebhook.MessageQueuer
	configLoader *config.Loader[discord.Config]
}

// New creates the module and runs migrations.
func New(db *sql.DB, webhooks discordwebhook.MessageQueuer) *Module {
	engine.MustMigrate(db, migration)
	return &Module{db: db, webhooks: webhooks}
}

// SetConfigLoader binds the config store to the discord module's config.
// Badge-in settings are stored on the shared Discord config page.
func (m *Module) SetConfigLoader(store *config.Store) {
	m.configLoader = config.NewLoader[discord.Config](store, "discord")
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(15*time.Second, engine.PollWorkqueue(m)))
}

// queueItem is one row from badgenotify_queue joined with member info.
type queueItem struct {
	MemberID    int64
	DisplayName string
}

func (s *queueItem) String() string {
	return "badge-notify:" + s.DisplayName
}

// GetItem fetches the next pending badge notification.
func (m *Module) GetItem(ctx context.Context) (*queueItem, error) {
	var item queueItem
	err := m.db.QueryRowContext(ctx, `
		SELECT q.member_id, COALESCE(m.name_override, m.name, m.email)
		FROM badgenotify_queue q
		JOIN members m ON m.id = q.member_id
		ORDER BY q.created ASC
		LIMIT 1`).Scan(&item.MemberID, &item.DisplayName)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// ProcessItem sends the Discord notification if the feature is enabled and
// the member hasn't been notified in the last 4 hours.
func (m *Module) ProcessItem(ctx context.Context, item *queueItem) error {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled || cfg.ChannelID == "" {
		slog.Debug("badgenotify not configured; skipping", "memberID", item.MemberID)
		return nil
	}

	// Rate limit: skip if notified within the last 4 hours.
	var lastNotified int64
	err = m.db.QueryRowContext(ctx, `
		SELECT COALESCE(fob_last_seen, 0)
		FROM members WHERE id = ?`, item.MemberID).Scan(&lastNotified)
	if err != nil {
		return fmt.Errorf("checking last notification for member %d: %w", item.MemberID, err)
	}
	if lastNotified > 0 && time.Now().Unix()-lastNotified < int64(notifyInterval.Seconds()) {
		slog.Debug("badgenotify: skipping recent notification",
			"memberID", item.MemberID, "lastNotified", lastNotified)
		return nil
	}

	payload, err := buildPayload(item.DisplayName)
	if err != nil {
		return err
	}
	return m.webhooks.QueueChannelMessage(ctx, cfg.ChannelID, payload)
}

// UpdateItem removes the queue row after processing (success or failure).
func (m *Module) UpdateItem(ctx context.Context, item *queueItem, success bool) error {
	if !success {
		slog.Error("badgenotify: failed to send notification", "memberID", item.MemberID)
	}
	_, err := m.db.ExecContext(ctx,
		"DELETE FROM badgenotify_queue WHERE member_id = ?", item.MemberID)
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
		Enabled:   dc.BadgeNotifyEnabled,
		ChannelID: dc.BadgeNotifyChannelID,
	}, nil
}

// buildPayload returns the JSON body for a badge-in Discord notification.
func buildPayload(displayName string) (string, error) {
	payload := webhookPayload{
		Username: botUsername,
		Embeds: []embed{{
			Title:       "Badge In",
			Description: fmt.Sprintf("**%s** just badged into the makerspace", displayName),
			Color:       0x57F287, // Discord green.
		}},
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshaling badge-notify payload: %w", err)
	}
	return string(out), nil
}

type webhookPayload struct {
	Username string  `json:"username,omitempty"`
	Embeds   []embed `json:"embeds,omitempty"`
}

type embed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Color       int    `json:"color,omitempty"`
}
