package discord

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/engine/oauthlogin"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

const maxRPS = 3

const migration = `
CREATE TABLE IF NOT EXISTS discord_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    client_id TEXT NOT NULL DEFAULT '',
    client_secret TEXT NOT NULL DEFAULT '',
    bot_token TEXT NOT NULL DEFAULT '',
    guild_id TEXT NOT NULL DEFAULT '',
    role_id TEXT NOT NULL DEFAULT '',
    print_webhook_url TEXT NOT NULL DEFAULT '',
    signup_webhook_url TEXT NOT NULL DEFAULT ''
) STRICT;

-- Add sync_interval_hours column to discord_config if it doesn't exist
-- SQLite doesn't support IF NOT EXISTS for ALTER TABLE, so we use a workaround
CREATE TABLE IF NOT EXISTS _discord_migration_check (id INTEGER PRIMARY KEY);
INSERT OR IGNORE INTO _discord_migration_check (id) VALUES (1);

-- Clean up old discord metrics samplings (now using direct event queries)
DELETE FROM metrics_samplings WHERE name IN ('discord-sync-successes', 'discord-sync-errors', 'discord-api-requests');

-- Drop the old hardcoded signup notification trigger (replaced by Go template-based notifications)
DROP TRIGGER IF EXISTS members_signup_notification;

-- Webhook configuration table: each row defines a webhook with its trigger event
-- or SQL trigger, message template (using {placeholder} syntax), and Discord username.
CREATE TABLE IF NOT EXISTS discord_webhooks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    webhook_url TEXT NOT NULL,
    trigger_event TEXT NOT NULL DEFAULT '',
    message_template TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT 'Conway',
    enabled INTEGER NOT NULL DEFAULT 1,
    trigger_table TEXT NOT NULL DEFAULT '',
    trigger_op TEXT NOT NULL DEFAULT ''
) STRICT;

-- Drop the old hardcoded member_events trigger (replaced by per-webhook dynamic triggers).
DROP TRIGGER IF EXISTS discord_webhook_on_member_event;

-- Legacy conditions table is no longer used; migration moves data into
-- discord_webhooks.when_clause and drops this table at startup.
`

var endpoint = oauth2.Endpoint{
	AuthURL:   "https://discord.com/api/oauth2/authorize",
	TokenURL:  "https://discord.com/api/oauth2/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

// LoginCompleteFunc is called by the discord module to finish a login flow.
// It receives the member ID and the callback URI to redirect to after login.
type LoginCompleteFunc func(w http.ResponseWriter, r *http.Request, memberID int64, callbackURI string)

// SignupConfirmFunc is called when no account exists for the user's email.
// It renders a confirmation page asking the user to confirm account creation.
type SignupConfirmFunc func(w http.ResponseWriter, r *http.Request, email, provider, callbackURI string)

type Module struct {
	db             *sql.DB
	self           *url.URL
	stateTokIssuer *engine.TokenIssuer
	httpClient     *http.Client
	eventLogger    *engine.EventLogger
	loginComplete  LoginCompleteFunc
	signupConfirm  SignupConfirmFunc
	configLoader   *config.Loader[Config]
}

func New(db *sql.DB, self *url.URL, iss *engine.TokenIssuer, eventLogger *engine.EventLogger) *Module {
	engine.MustMigrate(db, migration)
	// Add columns that can't use IF NOT EXISTS with ALTER TABLE
	db.Exec("ALTER TABLE discord_config ADD COLUMN sync_interval_hours INTEGER NOT NULL DEFAULT 24")
	db.Exec("ALTER TABLE discord_config ADD COLUMN signup_message_template TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE discord_config ADD COLUMN print_completed_message_template TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE discord_config ADD COLUMN print_failed_message_template TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE discord_webhooks ADD COLUMN trigger_table TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE discord_webhooks ADD COLUMN trigger_op TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE discord_webhooks ADD COLUMN when_clause TEXT NOT NULL DEFAULT ''")

	// Discount approval bot config (merged from the former discordbot config page).
	db.Exec("ALTER TABLE discord_config ADD COLUMN approval_bot_enabled INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE discord_config ADD COLUMN leadership_channel_webhook_url TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE discord_config ADD COLUMN application_public_key TEXT NOT NULL DEFAULT ''")

	// Migrate legacy trigger_event-based webhooks that used the old member_events trigger.
	// These become SQL triggers on the member_events table with INSERT operation.
	migrateLegacyWebhooks(db)

	// Migrate structured conditions into the when_clause column, then drop the conditions table.
	migrateConditionsToWhenClause(db)

	m := &Module{
		db:             db,
		self:           self,
		stateTokIssuer: iss,
		httpClient:     &http.Client{Timeout: time.Second * 10},
		eventLogger:    eventLogger,
	}

	return m
}

// SetLoginCompleter configures the function used to complete Discord-based logins.
// This must be called before routes are attached.
func (m *Module) SetLoginCompleter(f LoginCompleteFunc) {
	m.loginComplete = f
}

// SetSignupConfirm configures the function used to show the signup confirmation page.
// This must be called before routes are attached.
func (m *Module) SetSignupConfirm(f SignupConfirmFunc) {
	m.signupConfirm = f
}

// SetConfigLoader sets the typed config loader for this module.
func (m *Module) SetConfigLoader(store *config.Store) {
	m.configLoader = config.NewLoader[Config](store, "discord")
}

// loadConfig loads the latest Discord configuration.
func (m *Module) loadConfig(ctx context.Context) (*Config, error) {
	if m.configLoader == nil {
		return &Config{SyncIntervalHours: 24}, nil
	}
	cfg, err := m.configLoader.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading discord config: %w", err)
	}
	if cfg.SyncIntervalHours < 1 {
		cfg.SyncIntervalHours = 24
	}
	return cfg, nil
}

// getOAuthConfig builds an OAuth2 config from the current configuration.
func (m *Module) getOAuthConfig(ctx context.Context) (*oauth2.Config, error) {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("discord OAuth is not configured")
	}
	return &oauth2.Config{
		Endpoint:     endpoint,
		Scopes:       []string{"identify", "email"},
		RedirectURL:  fmt.Sprintf("%s/discord/callback", m.self.String()),
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
	}, nil
}

// getAPIClient creates a Discord API client with current configuration.
func (m *Module) getAPIClient(ctx context.Context) (*discordAPIClient, error) {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.BotToken == "" || cfg.GuildID == "" {
		return nil, fmt.Errorf("discord bot is not configured")
	}
	authConf, _ := m.getOAuthConfig(ctx) // May be nil if OAuth not configured
	return newDiscordAPIClient(cfg.BotToken, cfg.GuildID, m.httpClient, authConf), nil
}

// getRoleID gets the current role ID from configuration.
func (m *Module) getRoleID(ctx context.Context) (string, error) {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return "", err
	}
	return cfg.RoleID, nil
}

// IsLoginEnabled reports whether Discord OAuth login is available.
func (m *Module) IsLoginEnabled(ctx context.Context) bool {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return false
	}
	return cfg.ClientID != "" && cfg.ClientSecret != "" && m.loginComplete != nil
}

// getLoginOAuthConfig builds an OAuth2 config for the login flow
// (uses a different redirect URL than account linking).
func (m *Module) getLoginOAuthConfig(ctx context.Context) (*oauth2.Config, error) {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("discord OAuth is not configured")
	}
	return &oauth2.Config{
		Endpoint:     endpoint,
		Scopes:       []string{"identify", "email"},
		RedirectURL:  fmt.Sprintf("%s/login/discord/callback", m.self.String()),
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
	}, nil
}

func (m *Module) AttachRoutes(router *engine.Router) {
	// Account linking (requires existing session)
	router.HandleFunc("GET /discord/login", router.WithAuthn(m.handleLogin))
	router.HandleFunc("GET /discord/callback", router.WithAuthn(m.handleCallback))

	// Discord-based login (unauthenticated) - delegated to oauthlogin helper
	provider := &loginProvider{m: m}
	deps := oauthlogin.Deps{
		DB:             m.db,
		StateTokIssuer: m.stateTokIssuer,
		LoginComplete:  oauthlogin.LoginCompleteFunc(m.loginComplete),
	}
	if m.signupConfirm != nil {
		deps.SignupConfirm = oauthlogin.SignupConfirmFunc(m.signupConfirm)
	}
	start, callback := oauthlogin.Handlers(provider, deps)
	router.HandleFunc("GET /login/discord", start)
	router.HandleFunc("GET /login/discord/callback", callback)
}

// loginProvider adapts the Discord module to the oauthlogin.Provider interface.
type loginProvider struct{ m *Module }

func (p *loginProvider) Name() string { return "discord" }

func (p *loginProvider) OAuthConfig(ctx context.Context) (*oauth2.Config, error) {
	return p.m.getLoginOAuthConfig(ctx)
}

func (p *loginProvider) FetchUser(ctx context.Context, token *oauth2.Token, oc *oauth2.Config) (*oauthlogin.UserInfo, error) {
	client := oc.Client(ctx, token)
	resp, err := client.Get("https://discord.com/api/users/@me")
	if err != nil {
		return nil, fmt.Errorf("fetching Discord user info: %w", err)
	}
	defer resp.Body.Close()

	var u struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("decoding Discord user info: %w", err)
	}
	return &oauthlogin.UserInfo{
		Email:      strings.ToLower(u.Email),
		ProviderID: u.ID,
	}, nil
}

// LookupExistingMember resolves a member by discord_user_id first, then by email.
func (p *loginProvider) LookupExistingMember(ctx context.Context, db *sql.DB, info *oauthlogin.UserInfo) (int64, bool, error) {
	var memberID int64
	err := db.QueryRowContext(ctx,
		"SELECT id FROM members WHERE discord_user_id = ? LIMIT 1",
		info.ProviderID).Scan(&memberID)
	if err == nil {
		return memberID, true, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}

	err = db.QueryRowContext(ctx,
		"SELECT id FROM members WHERE email = ?",
		info.Email).Scan(&memberID)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return memberID, true, nil
}

// LinkAccount writes Discord identity columns and clears discord_last_synced
// so the background worker fetches the avatar.
func (p *loginProvider) LinkAccount(ctx context.Context, db *sql.DB, memberID int64, info *oauthlogin.UserInfo) error {
	_, err := db.ExecContext(ctx,
		"UPDATE members SET email = ?, discord_user_id = ?, discord_email = ?, discord_last_synced = NULL WHERE id = ?",
		info.Email, info.ProviderID, info.Email, memberID)
	if err != nil {
		slog.Error("failed to link discord account during login", "error", err, "memberID", memberID)
	}
	return err
}

func (p *loginProvider) SignupProviderTag(info *oauthlogin.UserInfo) string {
	return "discord:" + info.ProviderID
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
		m.eventLogger.LogEvent(ctx, userID, "OAuthError", "", "", false, "config error: "+err.Error())
		return err
	}

	token, err := authConf.Exchange(ctx, authCode)
	if err != nil {
		m.eventLogger.LogEvent(ctx, userID, "OAuthError", "", "", false, "token exchange: "+err.Error())
		return err
	}

	client, err := m.getAPIClient(ctx)
	if err != nil {
		// Fall back to minimal client for user info if bot not configured
		client = newDiscordAPIClient("", "", m.httpClient, authConf)
	}

	userInfo, err := client.GetUserInfo(ctx, token)
	if err != nil {
		m.eventLogger.LogEvent(ctx, userID, "OAuthError", "", "", false, "get user info: "+err.Error())
		return err
	}

	_, err = m.db.ExecContext(ctx, "UPDATE members SET discord_user_id = ?, discord_email = ?, discord_avatar = ? WHERE id = ?", userInfo.ID, userInfo.Email, userInfo.Avatar, userID)
	if err != nil {
		m.eventLogger.LogEvent(ctx, userID, "OAuthError", userInfo.ID, "", false, "update member: "+err.Error())
		return err
	}

	m.eventLogger.LogEvent(ctx, userID, "OAuthCallback", userInfo.ID, "", true, fmt.Sprintf("linked Discord account: %s", userInfo.Email))
	return nil
}

// handleLoginStart initiates the Discord OAuth2 login flow (unauthenticated).
// Removed: now handled by the oauthlogin package via loginProvider.


func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	// Always start workers - they'll check config dynamically
	mgr.Add(engine.Poll(time.Minute, m.scheduleFullReconciliation))
	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, maxRPS))))
}

func (m *Module) scheduleFullReconciliation(ctx context.Context) bool {
	// Check if role sync is configured
	cfg, err := m.loadConfig(ctx)
	if err != nil || cfg.BotToken == "" || cfg.GuildID == "" || cfg.RoleID == "" {
		return false
	}

	intervalSeconds := cfg.SyncIntervalHours * 3600

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
	if err != nil || cfg.BotToken == "" || cfg.GuildID == "" || cfg.RoleID == "" {
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
		m.eventLogger.LogEvent(ctx, memberID, "RoleSyncError", item.DiscordUserID, "", false, "config error: "+err.Error())
		return err
	}

	roleID, err := m.getRoleID(ctx)
	if err != nil {
		m.eventLogger.LogEvent(ctx, memberID, "RoleSyncError", item.DiscordUserID, "", false, "config error: "+err.Error())
		return err
	}

	shouldHaveRole := item.PaymentStatus.Valid && item.PaymentStatus.String != ""
	changed, memberInfo, err := client.EnsureRole(ctx, item.DiscordUserID, roleID, shouldHaveRole)
	if err != nil {
		m.eventLogger.LogEvent(ctx, memberID, "RoleSyncError", item.DiscordUserID, "", false, err.Error())
		return err
	}
	item.DisplayName = memberInfo.DisplayName
	item.Avatar = memberInfo.Avatar

	details := fmt.Sprintf("shouldHaveRole=%v, changed=%v, displayName=%s", shouldHaveRole, changed, memberInfo.DisplayName)
	m.eventLogger.LogEvent(ctx, memberID, "RoleSync", item.DiscordUserID, "", true, details)
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
