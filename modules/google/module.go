package google

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/engine/oauthlogin"
	"golang.org/x/oauth2"
)

var endpoint = oauth2.Endpoint{
	AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:  "https://oauth2.googleapis.com/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

// LoginCompleteFunc is called by the google module to finish a login flow.
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
	loginComplete  LoginCompleteFunc
	signupConfirm  SignupConfirmFunc
	configLoader   *config.Loader[Config]
}

func New(db *sql.DB, self *url.URL, iss *engine.TokenIssuer) *Module {
	engine.MustMigrate(db, `CREATE TABLE IF NOT EXISTS google_config (
		version INTEGER PRIMARY KEY AUTOINCREMENT,
		created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
		client_id TEXT NOT NULL DEFAULT '',
		client_secret TEXT NOT NULL DEFAULT ''
	) STRICT`)
	return &Module{
		db:             db,
		self:           self,
		stateTokIssuer: iss,
		httpClient:     &http.Client{Timeout: time.Second * 10},
	}
}

// SetLoginCompleter configures the function used to complete Google-based logins.
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
	m.configLoader = config.NewLoader[Config](store, "google")
}

// loadConfig loads the latest Google configuration.
func (m *Module) loadConfig(ctx context.Context) (*Config, error) {
	if m.configLoader == nil {
		return &Config{}, nil
	}
	cfg, err := m.configLoader.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading google config: %w", err)
	}
	return cfg, nil
}

// IsLoginEnabled reports whether Google OAuth login is available.
func (m *Module) IsLoginEnabled(ctx context.Context) bool {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return false
	}
	return cfg.ClientID != "" && cfg.ClientSecret != "" && m.loginComplete != nil
}

// getLoginOAuthConfig builds an OAuth2 config for the login flow.
func (m *Module) getLoginOAuthConfig(ctx context.Context) (*oauth2.Config, error) {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("google OAuth is not configured")
	}
	return &oauth2.Config{
		Endpoint:     endpoint,
		Scopes:       []string{"openid", "email"},
		RedirectURL:  fmt.Sprintf("%s/login/google/callback", m.self.String()),
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
	}, nil
}

func (m *Module) AttachRoutes(router *engine.Router) {
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
	router.HandleFunc("GET /login/google", start)
	router.HandleFunc("GET /login/google/callback", callback)
}

// loginProvider adapts the Google module to the oauthlogin.Provider interface.
type loginProvider struct{ m *Module }

func (p *loginProvider) Name() string { return "google" }

func (p *loginProvider) OAuthConfig(ctx context.Context) (*oauth2.Config, error) {
	return p.m.getLoginOAuthConfig(ctx)
}

func (p *loginProvider) FetchUser(ctx context.Context, token *oauth2.Token, oc *oauth2.Config) (*oauthlogin.UserInfo, error) {
	client := oc.Client(ctx, token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		return nil, fmt.Errorf("fetching Google user info: %w", err)
	}
	defer resp.Body.Close()

	var u struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("decoding Google user info: %w", err)
	}
	return &oauthlogin.UserInfo{Email: strings.ToLower(u.Email)}, nil
}

func (p *loginProvider) LookupExistingMember(ctx context.Context, db *sql.DB, info *oauthlogin.UserInfo) (int64, bool, error) {
	var memberID int64
	err := db.QueryRowContext(ctx, "SELECT id FROM members WHERE email = ?", info.Email).Scan(&memberID)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return memberID, true, nil
}

func (p *loginProvider) LinkAccount(_ context.Context, _ *sql.DB, _ int64, _ *oauthlogin.UserInfo) error {
	return nil
}

func (p *loginProvider) SignupProviderTag(_ *oauthlogin.UserInfo) string { return "google" }
