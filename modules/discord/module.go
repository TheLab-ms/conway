package discord

import (
	"database/sql"
	"encoding/json"
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
}

func New(db *sql.DB, self *url.URL, iss *engine.TokenIssuer, clientID, clientSecret string) *Module {
	conf := &oauth2.Config{
		Endpoint:     endpoint,
		Scopes:       []string{"identify"},
		RedirectURL:  fmt.Sprintf("%s/discord/callback", self.String()),
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}
	return &Module{db: db, self: self, stateTokIssuer: iss, authConf: conf}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/discord/login", router.WithAuth(m.handleLogin))
	router.Handle("GET", "/discord/callback", router.WithAuth(m.handleCallback))
}

func (m *Module) handleLogin(r *http.Request, ps httprouter.Params) engine.Response {
	// Include a JWT as state so we can verify it later to prevent CSRF
	state, err := m.stateTokIssuer.Sign(&jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(auth.GetUserMeta(r.Context()).ID, 10),
		Audience:  jwt.ClaimStrings{"discord-oauth"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if err != nil {
		return engine.Error(err)
	}

	url := m.authConf.AuthCodeURL(state)
	return engine.Redirect(url, http.StatusTemporaryRedirect)
}

func (m *Module) handleCallback(r *http.Request, ps httprouter.Params) engine.Response {
	state := r.URL.Query().Get("state")
	claims, err := m.stateTokIssuer.Verify(state)
	if err != nil {
		return engine.Error(err)
	}
	if id, _ := strconv.ParseInt(claims.Subject, 10, 64); id != auth.GetUserMeta(r.Context()).ID {
		return engine.ClientErrorf(403, "You aren't allowed to update another member's Discord ID")
	}

	token, err := m.authConf.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		return engine.Error(err)
	}

	client := m.authConf.Client(r.Context(), token)
	resp, err := client.Get("https://discord.com/api/users/@me")
	if err != nil {
		return engine.Error(err)
	}
	defer resp.Body.Close()

	var user struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return engine.Error(err)
	}

	_, err = m.db.Exec("UPDATE members SET discord_user_id = $1 WHERE id = $2", user.ID, claims.Subject)
	if err != nil {
		return engine.Errorf("writing Discord user ID to the db: %s", err)
	}
	slog.Info("discovered discord user", "discordID", user.ID, "memberID", claims.Subject)
	return engine.Redirect("/", http.StatusTemporaryRedirect)
}
