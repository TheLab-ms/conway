// Package discordbot notifies leadership when a member requests a membership
// discount and lets any authorized leader approve it from Discord with a
// single button click.
//
// Architecture:
//
//   - An AFTER UPDATE OF discount_status trigger on the members table appends
//     member IDs to discordbot_discount_request_queue whenever a member's
//     discount_status transitions into 'requested'. Nothing is enqueued on
//     signup or on unrelated status changes.
//   - A polling worker drains the queue: loads config, builds the rich Discord
//     JSON payload (embed describing the request + an Approve button), and
//     forwards it to the discordwebhook module for rate-limited delivery.
//   - POST /discord/interactions receives Discord's signed callbacks. After
//     verifying the Ed25519 signature it flips discount_status to 'approved'
//     and replies with UPDATE_MESSAGE so the original message records who
//     approved and removes the button.
package discordbot

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/modules/discord"
	"github.com/TheLab-ms/conway/modules/discordwebhook"
)

const migration = `
-- Retire the legacy signup-notification plumbing. Leadership is now notified
-- only when a discount is requested, not on every signup.
DROP TRIGGER IF EXISTS discordbot_signup_notify;
DROP TABLE IF EXISTS discordbot_signup_queue;

CREATE TABLE IF NOT EXISTS discordbot_discount_request_queue (
    member_id INTEGER PRIMARY KEY REFERENCES members(id) ON DELETE CASCADE,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
) STRICT;

CREATE TRIGGER IF NOT EXISTS discordbot_discount_request_notify
AFTER UPDATE OF discount_status ON members
WHEN NEW.discount_status = 'requested' AND OLD.discount_status IS NOT 'requested'
BEGIN
    INSERT OR IGNORE INTO discordbot_discount_request_queue (member_id) VALUES (NEW.id);
END;
`

// Module is the Discord discount-request notification + approval bot.
type Module struct {
	db           *sql.DB
	eventLogger  *engine.EventLogger
	webhooks     discordwebhook.MessageQueuer
	configLoader *config.Loader[discord.Config]

	// configOverride, when non-nil, is used in place of configLoader. It
	// exists as a single test-injection seam so unit tests can drive the
	// interaction handler and the workqueue without standing up a real
	// config.Store. Production code never sets this.
	configOverride func(context.Context) (*Config, error)
}

// New wires up the module. The webhooks queuer must be non-nil; it's where
// outbound Discord messages are dispatched (see modules/discordwebhook).
func New(db *sql.DB, eventLogger *engine.EventLogger, webhooks discordwebhook.MessageQueuer) *Module {
	engine.MustMigrate(db, migration)
	return &Module{db: db, eventLogger: eventLogger, webhooks: webhooks}
}

// SetConfigLoader binds the per-module config store. The approval-bot config
// lives on the shared "discord" config page (and discord_config table), so we
// load the discord module's Config and translate it into our local Config.
// Must be called before AttachWorkers/AttachRoutes are invoked.
func (m *Module) SetConfigLoader(store *config.Store) {
	m.configLoader = config.NewLoader[discord.Config](store, "discord")
}

// loadConfig returns the current config. The test seam configOverride wins
// when set; otherwise the configured Loader is used; otherwise a zero-value
// Config is returned (narrow cases where neither is wired).
func (m *Module) loadConfig(ctx context.Context) (*Config, error) {
	if m.configOverride != nil {
		return m.configOverride(ctx)
	}
	if m.configLoader == nil {
		return &Config{}, nil
	}
	dc, err := m.configLoader.Load(ctx)
	if err != nil {
		return nil, err
	}
	return &Config{
		Enabled:                     dc.ApprovalBotEnabled,
		LeadershipChannelWebhookURL: dc.LeadershipChannelWebhookURL,
		ApplicationPublicKey:        dc.ApplicationPublicKey,
	}, nil
}

// AttachRoutes registers the inbound Discord interaction endpoint.
//
// The route is intentionally unauthenticated by Conway's session middleware:
// Discord identifies itself by signing each request with the configured
// Ed25519 key. Anyone whose request fails signature verification gets a 401.
func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("POST /discord/interactions", m.handleInteraction)
}

// AttachWorkers starts the discount-request queue drainer.
func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(15*time.Second, engine.PollWorkqueue(m)))
}

// requestItem is one row from discordbot_discount_request_queue joined with
// member info needed to render the notification.
type requestItem struct {
	MemberID     int64
	Email        string
	DiscountType string
}

func (s *requestItem) String() string {
	return "discount-request:" + s.Email
}

// GetItem implements engine.Workqueue: fetches the next unsent request.
func (m *Module) GetItem(ctx context.Context) (*requestItem, error) {
	var item requestItem
	var discountType *string
	err := m.db.QueryRowContext(ctx, `
		SELECT q.member_id, m.email, m.discount_type
		FROM discordbot_discount_request_queue q
		JOIN members m ON m.id = q.member_id
		ORDER BY q.created ASC
		LIMIT 1`).Scan(&item.MemberID, &item.Email, &discountType)
	if err != nil {
		return nil, err
	}
	if discountType != nil {
		item.DiscountType = *discountType
	}
	return &item, nil
}

// ProcessItem builds the rich Discord payload and forwards to the webhook
// queue. When the bot is disabled or unconfigured, returns nil so UpdateItem
// drops the row (we don't want a backlog accumulating until configuration
// arrives — admins can flip Enabled on and accept that historical requests
// won't retroactively notify).
func (m *Module) ProcessItem(ctx context.Context, item *requestItem) error {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled || cfg.LeadershipChannelWebhookURL == "" {
		slog.Debug("discord bot not configured; skipping discount-request notification",
			"memberID", item.MemberID)
		return nil
	}
	payload, err := buildRequestPayload(item.MemberID, item.Email, item.DiscountType)
	if err != nil {
		return err
	}
	return m.webhooks.QueueMessage(ctx, cfg.LeadershipChannelWebhookURL, payload)
}

// UpdateItem deletes the queue row on success or after a permanent error.
// We log permanent failures via the EventLogger so admins can investigate.
func (m *Module) UpdateItem(ctx context.Context, item *requestItem, success bool) error {
	if !success {
		m.eventLogger.LogEvent(ctx, item.MemberID, "DiscountRequestNotifyError", "", "", false,
			"failed to enqueue discount-request notification; dropping queue row")
	}
	_, err := m.db.ExecContext(ctx,
		"DELETE FROM discordbot_discount_request_queue WHERE member_id = ?", item.MemberID)
	return err
}
