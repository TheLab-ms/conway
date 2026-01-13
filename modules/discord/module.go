package discord

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

const maxRPS = 3

const migration = `
-- Add sync_interval_hours column to discord_config if it doesn't exist
-- SQLite doesn't support IF NOT EXISTS for ALTER TABLE, so we use a workaround
CREATE TABLE IF NOT EXISTS _discord_migration_check (id INTEGER PRIMARY KEY);
INSERT OR IGNORE INTO _discord_migration_check (id) VALUES (1);

-- Clean up old discord metrics samplings (now using direct event queries)
DELETE FROM metrics_samplings WHERE name IN ('discord-sync-successes', 'discord-sync-errors', 'discord-api-requests');
`

var endpoint = oauth2.Endpoint{
	AuthURL:   "https://discord.com/api/oauth2/authorize",
	TokenURL:  "https://discord.com/api/oauth2/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

// discordConfig holds Discord-related configuration from the database.
type discordConfig struct {
	clientID          string
	clientSecret      string
	botToken          string
	guildID           string
	roleID            string
	syncIntervalHours int
}

type Module struct {
	db             *sql.DB
	self           *url.URL
	stateTokIssuer *engine.TokenIssuer
	httpClient     *http.Client
	eventLogger    *engine.EventLogger
}

func New(db *sql.DB, self *url.URL, iss *engine.TokenIssuer, eventLogger *engine.EventLogger) *Module {
	engine.MustMigrate(db, migration)
	// Add sync_interval_hours column if it doesn't exist (ALTER TABLE can't use IF NOT EXISTS)
	db.Exec("ALTER TABLE discord_config ADD COLUMN sync_interval_hours INTEGER NOT NULL DEFAULT 24")
	return &Module{
		db:             db,
		self:           self,
		stateTokIssuer: iss,
		httpClient:     &http.Client{Timeout: time.Second * 10},
		eventLogger:    eventLogger,
	}
}

// loadConfig loads the latest Discord configuration from the database.
func (m *Module) loadConfig(ctx context.Context) (*discordConfig, error) {
	if m.db == nil {
		return &discordConfig{syncIntervalHours: 24}, nil
	}

	row := m.db.QueryRowContext(ctx,
		`SELECT client_id, client_secret, bot_token, guild_id, role_id, COALESCE(sync_interval_hours, 24)
		 FROM discord_config ORDER BY version DESC LIMIT 1`)

	cfg := &discordConfig{}
	err := row.Scan(&cfg.clientID, &cfg.clientSecret, &cfg.botToken, &cfg.guildID, &cfg.roleID, &cfg.syncIntervalHours)
	if err == sql.ErrNoRows {
		return &discordConfig{syncIntervalHours: 24}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading discord config: %w", err)
	}
	if cfg.syncIntervalHours < 1 {
		cfg.syncIntervalHours = 24
	}
	return cfg, nil
}

// getOAuthConfig builds an OAuth2 config from the current configuration.
func (m *Module) getOAuthConfig(ctx context.Context) (*oauth2.Config, error) {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.clientID == "" || cfg.clientSecret == "" {
		return nil, fmt.Errorf("discord OAuth is not configured")
	}
	return &oauth2.Config{
		Endpoint:     endpoint,
		Scopes:       []string{"identify", "email"},
		RedirectURL:  fmt.Sprintf("%s/discord/callback", m.self.String()),
		ClientID:     cfg.clientID,
		ClientSecret: cfg.clientSecret,
	}, nil
}

// getAPIClient creates a Discord API client with current configuration.
func (m *Module) getAPIClient(ctx context.Context) (*discordAPIClient, error) {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.botToken == "" || cfg.guildID == "" {
		return nil, fmt.Errorf("discord bot is not configured")
	}
	authConf, _ := m.getOAuthConfig(ctx) // May be nil if OAuth not configured
	return newDiscordAPIClient(cfg.botToken, cfg.guildID, m.httpClient, authConf), nil
}

// getRoleID gets the current role ID from configuration.
func (m *Module) getRoleID(ctx context.Context) (string, error) {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return "", err
	}
	return cfg.roleID, nil
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /discord/login", router.WithAuthn(m.handleLogin))
	router.HandleFunc("GET /discord/callback", router.WithAuthn(m.handleCallback))
}

func (m *Module) handleLogin(w http.ResponseWriter, r *http.Request) {
	authConf, err := m.getOAuthConfig(r.Context())
	if err != nil {
		engine.ClientError(w, "Discord Not Configured", "Discord integration is not configured. Please contact an administrator.", 503)
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

	http.Redirect(w, r, authConf.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

func (m *Module) handleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	claims, err := m.stateTokIssuer.Verify(state)
	if err != nil {
		engine.ClientError(w, "Invalid State", "The OAuth state is invalid - please try again", 400)
		return
	}

	userID := auth.GetUserMeta(r.Context()).ID
	if id, err := strconv.ParseInt(claims.Subject, 10, 64); err != nil {
		engine.ClientError(w, "Invalid Request", "The user ID is invalid", 400)
		return
	} else if id != userID {
		engine.ClientError(w, "Unauthorized", "You are not authorized to perform this action", 403)
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
	authConf, err := m.getOAuthConfig(ctx)
	if err != nil {
		m.eventLogger.LogEvent(ctx, "discord", userID, "OAuthError", "", "", false, "config error: "+err.Error())
		return err
	}

	token, err := authConf.Exchange(ctx, authCode)
	if err != nil {
		m.eventLogger.LogEvent(ctx, "discord", userID, "OAuthError", "", "", false, "token exchange: "+err.Error())
		return err
	}

	client, err := m.getAPIClient(ctx)
	if err != nil {
		// Fall back to minimal client for user info if bot not configured
		client = newDiscordAPIClient("", "", m.httpClient, authConf)
	}

	userInfo, err := client.GetUserInfo(ctx, token)
	if err != nil {
		m.eventLogger.LogEvent(ctx, "discord", userID, "OAuthError", "", "", false, "get user info: "+err.Error())
		return err
	}

	_, err = m.db.ExecContext(ctx, "UPDATE members SET discord_user_id = ?, discord_email = ?, discord_avatar = ? WHERE id = ?", userInfo.ID, userInfo.Email, userInfo.Avatar, userID)
	if err != nil {
		m.eventLogger.LogEvent(ctx, "discord", userID, "OAuthError", userInfo.ID, "", false, "update member: "+err.Error())
		return err
	}

	m.eventLogger.LogEvent(ctx, "discord", userID, "OAuthCallback", userInfo.ID, "", true, fmt.Sprintf("linked Discord account: %s", userInfo.Email))
	return nil
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	// Always start workers - they'll check config dynamically
	mgr.Add(engine.Poll(time.Minute, m.scheduleFullReconciliation))
	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, maxRPS))))
}

func (m *Module) scheduleFullReconciliation(ctx context.Context) bool {
	// Check if role sync is configured
	cfg, err := m.loadConfig(ctx)
	if err != nil || cfg.botToken == "" || cfg.guildID == "" || cfg.roleID == "" {
		return false
	}

	intervalSeconds := cfg.syncIntervalHours * 3600

	result, err := m.db.ExecContext(ctx, `UPDATE members SET discord_last_synced = NULL WHERE discord_user_id IS NOT NULL AND (discord_last_synced IS NULL OR discord_last_synced < unixepoch() - ?) AND id IN (SELECT id FROM members WHERE discord_user_id IS NOT NULL AND (discord_last_synced IS NULL OR discord_last_synced < unixepoch() - ?) ORDER BY id ASC LIMIT 10)`, intervalSeconds, intervalSeconds)
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
	// Check if role sync is configured
	cfg, err := m.loadConfig(ctx)
	if err != nil || cfg.botToken == "" || cfg.guildID == "" || cfg.roleID == "" {
		return nil, sql.ErrNoRows // No work to do
	}

	item = &syncItem{}
	err = m.db.QueryRowContext(ctx, `UPDATE members SET discord_last_synced = unixepoch() WHERE id = ( SELECT id FROM members WHERE discord_user_id IS NOT NULL AND discord_last_synced IS NULL ORDER BY id ASC LIMIT 1) RETURNING id, discord_user_id, payment_status`).Scan(&item.MemberID, &item.DiscordUserID, &item.PaymentStatus)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (m *Module) ProcessItem(ctx context.Context, item *syncItem) error {
	memberID, _ := strconv.ParseInt(item.MemberID, 10, 64)

	client, err := m.getAPIClient(ctx)
	if err != nil {
		m.eventLogger.LogEvent(ctx, "discord", memberID, "RoleSyncError", item.DiscordUserID, "", false, "config error: "+err.Error())
		return err
	}

	roleID, err := m.getRoleID(ctx)
	if err != nil {
		m.eventLogger.LogEvent(ctx, "discord", memberID, "RoleSyncError", item.DiscordUserID, "", false, "config error: "+err.Error())
		return err
	}

	shouldHaveRole := item.PaymentStatus.Valid && item.PaymentStatus.String != ""
	changed, memberInfo, err := client.EnsureRole(ctx, item.DiscordUserID, roleID, shouldHaveRole)
	if err != nil {
		m.eventLogger.LogEvent(ctx, "discord", memberID, "RoleSyncError", item.DiscordUserID, "", false, err.Error())
		return err
	}
	item.DisplayName = memberInfo.DisplayName
	item.Avatar = memberInfo.Avatar

	details := fmt.Sprintf("shouldHaveRole=%v, changed=%v, displayName=%s", shouldHaveRole, changed, memberInfo.DisplayName)
	m.eventLogger.LogEvent(ctx, "discord", memberID, "RoleSync", item.DiscordUserID, "", true, details)
	slog.Info("sync'd discord role", "memberID", item.MemberID, "discordUserID", item.DiscordUserID, "displayName", memberInfo.DisplayName, "shouldHaveRole", shouldHaveRole, "changed", changed)
	return nil
}

func (m *Module) UpdateItem(ctx context.Context, item *syncItem, success bool) error {
	if success {
		_, err := m.db.ExecContext(ctx, "UPDATE members SET discord_last_synced = unixepoch(), discord_username = ?, discord_avatar = ? WHERE id = ?", item.DisplayName, item.Avatar, item.MemberID)
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
	Avatar        []byte // populated during ProcessItem
}

func (s *syncItem) String() string {
	return fmt.Sprintf("memberID=%s", s.MemberID)
}
