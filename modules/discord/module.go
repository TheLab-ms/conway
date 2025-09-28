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

// TODO:
// - Use generic cronjob for resync
// - Decouple from members table?

const maxRPS = 3

var endpoint = oauth2.Endpoint{
	AuthURL:   "https://discord.com/api/oauth2/authorize",
	TokenURL:  "https://discord.com/api/oauth2/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

type Module struct {
	db             *sql.DB
	self           *url.URL
	stateTokIssuer *engine.TokenIssuer
	authConf       *oauth2.Config
	roleID         string
	client         *discordAPIClient
}

func New(db *sql.DB, self *url.URL, iss *engine.TokenIssuer, clientID, clientSecret, botToken, guildID, roleID string) *Module {
	conf := &oauth2.Config{
		Endpoint:     endpoint,
		Scopes:       []string{"identify"},
		RedirectURL:  fmt.Sprintf("%s/discord/callback", self.String()),
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}
	client := &http.Client{Timeout: time.Second * 10}
	return &Module{
		db:             db,
		self:           self,
		stateTokIssuer: iss,
		authConf:       conf,
		roleID:         roleID,
		client:         newDiscordAPIClient(botToken, guildID, client, conf),
	}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /discord/login", router.WithAuthn(m.handleLogin))
	router.HandleFunc("GET /discord/callback", router.WithAuthn(m.handleCallback))
}

func (m *Module) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := m.stateTokIssuer.Sign(&jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(auth.GetUserMeta(r.Context()).ID, 10),
		Audience:  jwt.ClaimStrings{"discord-oauth"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.Redirect(w, r, m.authConf.AuthCodeURL(state), http.StatusTemporaryRedirect)
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
	token, err := m.authConf.Exchange(ctx, authCode)
	if err != nil {
		return err
	}
	discordUserID, err := m.client.GetUserInfo(ctx, token)
	if err != nil {
		return err
	}

	_, err = m.db.ExecContext(ctx, "UPDATE members SET discord_user_id = ? WHERE id = ?", discordUserID, userID)
	return err
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	if m.roleID == "" {
		slog.Warn("disabling discord role sync because the role ID was configured")
		return
	}

	mgr.Add(engine.Poll(time.Minute, m.scheduleFullReconciliation))
	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, maxRPS))))
}

func (m *Module) scheduleFullReconciliation(ctx context.Context) bool {
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

func (m *Module) GetItem(ctx context.Context) (item syncItem, err error) {
	err = m.db.QueryRowContext(ctx, `UPDATE members SET discord_last_synced = unixepoch() WHERE id = ( SELECT id FROM members WHERE discord_user_id IS NOT NULL AND discord_last_synced IS NULL ORDER BY id ASC LIMIT 1) RETURNING id, discord_user_id, payment_status`).Scan(&item.MemberID, &item.DiscordUserID, &item.PaymentStatus)
	return item, err
}

func (m *Module) ProcessItem(ctx context.Context, item syncItem) error {
	shouldHaveRole := item.PaymentStatus.Valid && item.PaymentStatus.String != ""
	changed, err := m.client.EnsureRole(ctx, item.DiscordUserID, m.roleID, shouldHaveRole)
	if err != nil {
		return err
	}
	slog.Info("sync'd discord role", "memberID", item.MemberID, "discordUserID", item.DiscordUserID, "shouldHaveRole", shouldHaveRole, "changed", changed)
	return nil
}

func (m *Module) UpdateItem(ctx context.Context, item syncItem, success bool) error {
	if success {
		_, err := m.db.ExecContext(ctx, "UPDATE members SET discord_last_synced = unixepoch() WHERE id = ?", item.MemberID)
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
}

func (s syncItem) String() string {
	return fmt.Sprintf("memberID=%s", s.MemberID)
}
