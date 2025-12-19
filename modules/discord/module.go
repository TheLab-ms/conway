package discord

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/settings"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

const maxRPS = 3

var endpoint = oauth2.Endpoint{
	AuthURL:   "https://discord.com/api/oauth2/authorize",
	TokenURL:  "https://discord.com/api/oauth2/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

type discordConfig struct {
	ClientID     string
	ClientSecret string
	BotToken     string
	GuildID      string
	RoleID       string
}

type Module struct {
	db             *sql.DB
	self           *url.URL
	stateTokIssuer *engine.TokenIssuer
	settings       *settings.Store

	config   atomic.Pointer[discordConfig]
	authConf atomic.Pointer[oauth2.Config]
	client   atomic.Pointer[discordAPIClient]
	mu       sync.Mutex
}

func New(db *sql.DB, self *url.URL, iss *engine.TokenIssuer, settingsStore *settings.Store) *Module {
	settingsStore.RegisterSection(settings.Section{
		Title: "Discord",
		Fields: []settings.Field{
			{Key: "discord.client_id", Label: "Client ID", Description: "Discord OAuth2 client ID"},
			{Key: "discord.client_secret", Label: "Client Secret", Description: "Discord OAuth2 client secret", Sensitive: true},
			{Key: "discord.bot_token", Label: "Bot Token", Description: "Discord bot token", Sensitive: true},
			{Key: "discord.guild_id", Label: "Guild ID", Description: "Discord server (guild) ID"},
			{Key: "discord.role_id", Label: "Active Member Role ID", Description: "Discord role ID for active members"},
		},
	})

	m := &Module{
		db:             db,
		self:           self,
		stateTokIssuer: iss,
		settings:       settingsStore,
	}

	// Initialize with empty config
	m.config.Store(&discordConfig{})

	return m
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /discord/login", router.WithAuthn(m.handleLogin))
	router.HandleFunc("GET /discord/callback", router.WithAuthn(m.handleCallback))
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	ctx := context.Background()

	// Watch for config changes
	m.settings.Watch(ctx, "discord.client_id", func(v string) {
		m.updateConfig(func(c *discordConfig) { c.ClientID = v })
	})
	m.settings.Watch(ctx, "discord.client_secret", func(v string) {
		m.updateConfig(func(c *discordConfig) { c.ClientSecret = v })
	})
	m.settings.Watch(ctx, "discord.bot_token", func(v string) {
		m.updateConfig(func(c *discordConfig) { c.BotToken = v })
	})
	m.settings.Watch(ctx, "discord.guild_id", func(v string) {
		m.updateConfig(func(c *discordConfig) { c.GuildID = v })
	})
	m.settings.Watch(ctx, "discord.role_id", func(v string) {
		m.updateConfig(func(c *discordConfig) { c.RoleID = v })
	})

	mgr.Add(engine.Poll(time.Minute, m.scheduleFullReconciliation))
	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, maxRPS))))
}

func (m *Module) updateConfig(fn func(*discordConfig)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	old := m.config.Load()
	newCfg := *old // copy
	fn(&newCfg)
	m.config.Store(&newCfg)

	// Rebuild OAuth config and API client if we have the required fields
	if newCfg.ClientID != "" && newCfg.ClientSecret != "" {
		conf := &oauth2.Config{
			Endpoint:     endpoint,
			Scopes:       []string{"identify"},
			RedirectURL:  fmt.Sprintf("%s/discord/callback", m.self.String()),
			ClientID:     newCfg.ClientID,
			ClientSecret: newCfg.ClientSecret,
		}
		m.authConf.Store(conf)

		if newCfg.BotToken != "" && newCfg.GuildID != "" {
			httpClient := &http.Client{Timeout: time.Second * 10}
			client := newDiscordAPIClient(newCfg.BotToken, newCfg.GuildID, httpClient, conf)
			m.client.Store(client)
			slog.Info("discord module configured", "guildID", newCfg.GuildID, "roleID", newCfg.RoleID)
		}
	}
}

func (m *Module) handleLogin(w http.ResponseWriter, r *http.Request) {
	conf := m.authConf.Load()
	if conf == nil {
		http.Error(w, "Discord integration not configured", http.StatusServiceUnavailable)
		return
	}

	state, err := m.stateTokIssuer.Sign(&jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(auth.GetUserMeta(r.Context()).ID, 10),
		Audience:  jwt.ClaimStrings{"discord-oauth"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.Redirect(w, r, conf.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

func (m *Module) handleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	claims, err := m.stateTokIssuer.Verify(state)
	if err != nil {
		http.Error(w, "Invalid oauth state - try again", 400)
		return
	}

	userID := auth.GetUserMeta(r.Context()).ID
	if id, err := strconv.ParseInt(claims.Subject, 10, 64); err != nil {
		http.Error(w, "Invalid user ID", 400)
		return
	} else if id != userID {
		http.Error(w, "Unauthorized", 403)
		return
	}

	err = m.processDiscordCallback(r.Context(), userID, r.URL.Query().Get("code"))
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func (m *Module) processDiscordCallback(ctx context.Context, userID int64, authCode string) error {
	conf := m.authConf.Load()
	client := m.client.Load()
	if conf == nil || client == nil {
		return fmt.Errorf("discord not configured")
	}

	token, err := conf.Exchange(ctx, authCode)
	if err != nil {
		return err
	}
	discordUserID, err := client.GetUserInfo(ctx, token)
	if err != nil {
		return err
	}

	_, err = m.db.ExecContext(ctx, "UPDATE members SET discord_user_id = ? WHERE id = ?", discordUserID, userID)
	return err
}

func (m *Module) scheduleFullReconciliation(ctx context.Context) bool {
	cfg := m.config.Load()
	if cfg.RoleID == "" {
		return false // Role sync disabled
	}

	result, err := m.db.ExecContext(ctx, `UPDATE members SET discord_last_synced = NULL WHERE discord_user_id IS NOT NULL AND (discord_last_synced IS NULL OR discord_last_synced < unixepoch() - 86400) AND id IN (SELECT id FROM members WHERE discord_user_id IS NOT NULL AND (discord_last_synced IS NULL OR discord_last_synced < unixepoch() - 86400) ORDER BY id ASC LIMIT 10)`)
	if err != nil {
		slog.Error("failed to schedule full Discord reconciliation", "error", err)
		return false
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		slog.Info("scheduled full Discord role reconciliation", "membersMarked", rowsAffected)
	}
	return false
}

func (m *Module) GetItem(ctx context.Context) (item *syncItem, err error) {
	cfg := m.config.Load()
	if cfg.RoleID == "" || m.client.Load() == nil {
		return nil, sql.ErrNoRows // Not configured
	}

	item = &syncItem{}
	err = m.db.QueryRowContext(ctx, `UPDATE members SET discord_last_synced = unixepoch() WHERE id = ( SELECT id FROM members WHERE discord_user_id IS NOT NULL AND discord_last_synced IS NULL ORDER BY id ASC LIMIT 1) RETURNING id, discord_user_id, payment_status`).Scan(&item.MemberID, &item.DiscordUserID, &item.PaymentStatus)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (m *Module) ProcessItem(ctx context.Context, item *syncItem) error {
	cfg := m.config.Load()
	client := m.client.Load()
	if client == nil {
		return fmt.Errorf("discord client not configured")
	}

	shouldHaveRole := item.PaymentStatus.Valid && item.PaymentStatus.String != ""
	changed, displayName, err := client.EnsureRole(ctx, item.DiscordUserID, cfg.RoleID, shouldHaveRole)
	if err != nil {
		return err
	}
	item.DisplayName = displayName
	slog.Info("sync'd discord role", "memberID", item.MemberID, "discordUserID", item.DiscordUserID, "displayName", displayName, "shouldHaveRole", shouldHaveRole, "changed", changed)
	return nil
}

func (m *Module) UpdateItem(ctx context.Context, item *syncItem, success bool) error {
	if success {
		_, err := m.db.ExecContext(ctx, "UPDATE members SET discord_last_synced = unixepoch(), discord_username = ? WHERE id = ?", item.DisplayName, item.MemberID)
		return err
	}

	_, err := m.db.ExecContext(ctx, `
		UPDATE members
		SET discord_last_synced = unixepoch() + MIN(
			CASE
				WHEN discord_last_synced IS NULL OR discord_last_synced <= unixepoch() THEN 300
				ELSE (discord_last_synced - unixepoch()) * 2
			END,
			86400
		)
		WHERE id = ?`, item.MemberID)
	return err
}

type syncItem struct {
	MemberID      string
	DiscordUserID string
	PaymentStatus sql.NullString
	DisplayName   string // populated during ProcessItem
}

func (s *syncItem) String() string {
	return fmt.Sprintf("memberID=%s", s.MemberID)
}
