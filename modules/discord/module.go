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
}

func New(db *sql.DB, self *url.URL, iss *engine.TokenIssuer, eventLogger *engine.EventLogger) *Module {
	engine.MustMigrate(db, migration)
	// Add columns that can't use IF NOT EXISTS with ALTER TABLE
	db.Exec("ALTER TABLE discord_config ADD COLUMN sync_interval_hours INTEGER NOT NULL DEFAULT 24")
	db.Exec("ALTER TABLE discord_config ADD COLUMN signup_message_template TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE discord_config ADD COLUMN print_completed_message_template TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE discord_config ADD COLUMN print_failed_message_template TEXT NOT NULL DEFAULT ''")
	return &Module{
		db:             db,
		self:           self,
		stateTokIssuer: iss,
		httpClient:     &http.Client{Timeout: time.Second * 10},
		eventLogger:    eventLogger,
	}
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

// IsLoginEnabled reports whether Discord OAuth login is available.
func (m *Module) IsLoginEnabled(ctx context.Context) bool {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return false
	}
	return cfg.clientID != "" && cfg.clientSecret != "" && m.loginComplete != nil
}

// getLoginOAuthConfig builds an OAuth2 config for the login flow
// (uses a different redirect URL than account linking).
func (m *Module) getLoginOAuthConfig(ctx context.Context) (*oauth2.Config, error) {
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
		RedirectURL:  fmt.Sprintf("%s/login/discord/callback", m.self.String()),
		ClientID:     cfg.clientID,
		ClientSecret: cfg.clientSecret,
	}, nil
}

func (m *Module) AttachRoutes(router *engine.Router) {
	// Account linking (requires existing session)
	router.HandleFunc("GET /discord/login", router.WithAuthn(m.handleLogin))
	router.HandleFunc("GET /discord/callback", router.WithAuthn(m.handleCallback))

	// Discord-based login (unauthenticated)
	router.HandleFunc("GET /login/discord", m.handleLoginStart)
	router.HandleFunc("GET /login/discord/callback", m.handleLoginCallback)
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
func (m *Module) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	oauthConf, err := m.getLoginOAuthConfig(r.Context())
	if err != nil {
		engine.ClientError(w, "Not Available", "Discord login is not configured", 503)
		return
	}

	callbackURI := r.URL.Query().Get("callback_uri")

	state, err := m.stateTokIssuer.Sign(&jwt.RegisteredClaims{
		Issuer:    callbackURI,
		Audience:  jwt.ClaimStrings{"discord-login"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.Redirect(w, r, oauthConf.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

// handleLoginCallback completes the Discord OAuth2 login flow (unauthenticated).
func (m *Module) handleLoginCallback(w http.ResponseWriter, r *http.Request) {
	// Verify state JWT
	stateStr := r.URL.Query().Get("state")
	stateClaims, err := m.stateTokIssuer.Verify(stateStr)
	if err != nil || len(stateClaims.Audience) == 0 || stateClaims.Audience[0] != "discord-login" {
		engine.ClientError(w, "Invalid State", "The login state is invalid or expired. Please try again.", 400)
		return
	}
	callbackURI := stateClaims.Issuer

	// Handle OAuth error (e.g. user denied)
	if r.URL.Query().Get("error") != "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	// Exchange code for token
	oauthConf, err := m.getLoginOAuthConfig(r.Context())
	if err != nil {
		engine.ClientError(w, "Not Available", "Discord login is not configured", 503)
		return
	}

	token, err := oauthConf.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		engine.ClientError(w, "Login Failed", "Discord login failed. Please try again.", 400)
		return
	}

	// Fetch Discord user info
	client := oauthConf.Client(r.Context(), token)
	resp, err := client.Get("https://discord.com/api/users/@me")
	if err != nil {
		engine.SystemError(w, "Failed to fetch Discord user info: "+err.Error())
		return
	}
	defer resp.Body.Close()

	var discordUser struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&discordUser); err != nil {
		engine.SystemError(w, "Failed to decode Discord user info: "+err.Error())
		return
	}

	if discordUser.Email == "" {
		engine.ClientError(w, "Email Required", "Your Discord account does not have a verified email address. Please use email login instead.", 400)
		return
	}
	discordUser.Email = strings.ToLower(discordUser.Email)

	// Find or create member: first by discord_user_id, then by email
	var memberID int64
	err = m.db.QueryRowContext(r.Context(),
		"SELECT id FROM members WHERE discord_user_id = ? LIMIT 1",
		discordUser.ID).Scan(&memberID)

	if err == sql.ErrNoRows {
		// No member with this discord_user_id; check by email
		err = m.db.QueryRowContext(r.Context(),
			"SELECT id FROM members WHERE email = ?",
			discordUser.Email).Scan(&memberID)

		if err == sql.ErrNoRows {
			// No account exists at all - show signup confirmation
			if m.signupConfirm != nil {
				m.signupConfirm(w, r, discordUser.Email, "discord:"+discordUser.ID, callbackURI)
				return
			}
			// Fallback: create account directly if no confirmation function is set
			err = m.db.QueryRowContext(r.Context(),
				"INSERT INTO members (email) VALUES ($1) ON CONFLICT (email) DO UPDATE SET email=email RETURNING id",
				discordUser.Email).Scan(&memberID)
			if err != nil {
				engine.SystemError(w, err.Error())
				return
			}
		} else if err != nil {
			engine.SystemError(w, err.Error())
			return
		}
	} else if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Link Discord account and mark for async sync (avatar will be fetched by the
	// discord module's background worker via discord_last_synced = NULL).
	_, err = m.db.ExecContext(r.Context(),
		"UPDATE members SET email = ?, discord_user_id = ?, discord_email = ?, discord_last_synced = NULL WHERE id = ?",
		discordUser.Email, discordUser.ID, discordUser.Email, memberID)
	if err != nil {
		slog.Error("failed to link discord account during login", "error", err, "memberID", memberID)
	}

	m.loginComplete(w, r, memberID, callbackURI)
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
