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
	"github.com/julienschmidt/httprouter"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
)

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
	rateLimiter    *rate.Limiter
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
		rateLimiter:    rate.NewLimiter(rate.Every(time.Second), 2),
	}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/discord/login", router.WithAuth(m.handleLogin))
	router.Handle("GET", "/discord/callback", router.WithAuth(m.handleCallback))
}

func (m *Module) handleLogin(r *http.Request, ps httprouter.Params) engine.Response {
	state, err := m.stateTokIssuer.Sign(&jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(auth.GetUserMeta(r.Context()).ID, 10),
		Audience:  jwt.ClaimStrings{"discord-oauth"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if err != nil {
		return engine.Errorf("creating state token: %s", err)
	}

	url := m.authConf.AuthCodeURL(state)
	return engine.Redirect(url, http.StatusTemporaryRedirect)
}

func (m *Module) handleCallback(r *http.Request, ps httprouter.Params) engine.Response {
	state := r.URL.Query().Get("state")
	claims, err := m.stateTokIssuer.Verify(state)
	if err != nil {
		return engine.Errorf("verifying state token: %s", err)
	}

	if id, err := strconv.ParseInt(claims.Subject, 10, 64); err != nil {
		return engine.ClientErrorf(400, "invalid user ID in token")
	} else if id != auth.GetUserMeta(r.Context()).ID {
		return engine.ClientErrorf(403, "you aren't allowed to update another member's Discord ID")
	}

	userID := auth.GetUserMeta(r.Context()).ID
	code := r.URL.Query().Get("code")

	return m.processDiscordCallback(r.Context(), userID, code)
}

func (m *Module) processDiscordCallback(ctx context.Context, userID int64, authCode string) engine.Response {
	token, err := m.authConf.Exchange(ctx, authCode)
	if err != nil {
		return engine.Errorf("exchanging auth code for token: %s", err)
	}

	discordUserID, err := m.client.getUserInfo(ctx, token)
	if err != nil {
		return engine.Errorf("fetching Discord user info: %s", err)
	}

	_, err = m.db.ExecContext(ctx, "UPDATE members SET discord_user_id = ? WHERE id = ?", discordUserID, userID)
	if err != nil {
		return engine.Errorf("updating member Discord user ID: %s", err)
	}

	slog.Info("discovered discord user", "discordID", discordUserID, "memberID", userID)
	return engine.Redirect("/", http.StatusTemporaryRedirect)
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	if m.roleID == "" {
		slog.Warn("disabling discord role sync because the role ID was configured")
		return
	}

	mgr.Add(engine.Poll(time.Minute, m.scheduleFullReconciliation))
	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(m)))
}

// scheduleFullReconciliation updates members to force Discord resync once per day - just in case things are out of sync somehow.
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
	return true
}

func (m *Module) GetItem(ctx context.Context) (item syncItem, err error) {
	err = m.db.QueryRowContext(ctx, `UPDATE members SET discord_last_synced = unixepoch() WHERE id = ( SELECT id FROM members WHERE discord_user_id IS NOT NULL AND discord_last_synced IS NULL ORDER BY id ASC LIMIT 1) RETURNING id, discord_user_id, payment_status`).Scan(&item.MemberID, &item.DiscordUserID, &item.PaymentStatus)
	return item, err
}

func (m *Module) ProcessItem(ctx context.Context, item syncItem) error {
	m.rateLimiter.Wait(ctx)
	shouldHaveRole := item.PaymentStatus.Valid && item.PaymentStatus.String != ""
	changed, err := m.client.ensureRole(ctx, item.DiscordUserID, m.roleID, shouldHaveRole)
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
