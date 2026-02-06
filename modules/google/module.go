package google

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
	"github.com/golang-jwt/jwt/v5"
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

type Module struct {
	db             *sql.DB
	self           *url.URL
	stateTokIssuer *engine.TokenIssuer
	httpClient     *http.Client
	loginComplete  LoginCompleteFunc
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

// loadConfig loads the latest Google configuration from the database.
func (m *Module) loadConfig(ctx context.Context) (clientID, clientSecret string, err error) {
	if m.db == nil {
		return "", "", nil
	}
	row := m.db.QueryRowContext(ctx,
		`SELECT client_id, client_secret FROM google_config ORDER BY version DESC LIMIT 1`)
	err = row.Scan(&clientID, &clientSecret)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return clientID, clientSecret, err
}

// IsLoginEnabled reports whether Google OAuth login is available.
func (m *Module) IsLoginEnabled(ctx context.Context) bool {
	clientID, clientSecret, err := m.loadConfig(ctx)
	if err != nil {
		return false
	}
	return clientID != "" && clientSecret != "" && m.loginComplete != nil
}

// getLoginOAuthConfig builds an OAuth2 config for the login flow.
func (m *Module) getLoginOAuthConfig(ctx context.Context) (*oauth2.Config, error) {
	clientID, clientSecret, err := m.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("google OAuth is not configured")
	}
	return &oauth2.Config{
		Endpoint:     endpoint,
		Scopes:       []string{"openid", "email"},
		RedirectURL:  fmt.Sprintf("%s/login/google/callback", m.self.String()),
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}, nil
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /login/google", m.handleLoginStart)
	router.HandleFunc("GET /login/google/callback", m.handleLoginCallback)
}

// handleLoginStart initiates the Google OAuth2 login flow.
func (m *Module) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	oauthConf, err := m.getLoginOAuthConfig(r.Context())
	if err != nil {
		engine.ClientError(w, "Not Available", "Google login is not configured", 503)
		return
	}

	callbackURI := r.URL.Query().Get("callback_uri")

	state, err := m.stateTokIssuer.Sign(&jwt.RegisteredClaims{
		Issuer:    callbackURI,
		Audience:  jwt.ClaimStrings{"google-login"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.Redirect(w, r, oauthConf.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

// handleLoginCallback completes the Google OAuth2 login flow.
func (m *Module) handleLoginCallback(w http.ResponseWriter, r *http.Request) {
	// Verify state JWT
	stateStr := r.URL.Query().Get("state")
	stateClaims, err := m.stateTokIssuer.Verify(stateStr)
	if err != nil || len(stateClaims.Audience) == 0 || stateClaims.Audience[0] != "google-login" {
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
		engine.ClientError(w, "Not Available", "Google login is not configured", 503)
		return
	}

	token, err := oauthConf.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		engine.ClientError(w, "Login Failed", "Google login failed. Please try again.", 400)
		return
	}

	// Fetch Google user info
	client := oauthConf.Client(r.Context(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		engine.SystemError(w, "Failed to fetch Google user info: "+err.Error())
		return
	}
	defer resp.Body.Close()

	var googleUser struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&googleUser); err != nil {
		engine.SystemError(w, "Failed to decode Google user info: "+err.Error())
		return
	}

	if googleUser.Email == "" {
		engine.ClientError(w, "Email Required", "Your Google account does not have an email address. Please use email login instead.", 400)
		return
	}
	googleUser.Email = strings.ToLower(googleUser.Email)

	// Find or create member by email (same as email login)
	var memberID int64
	err = m.db.QueryRowContext(r.Context(),
		"INSERT INTO members (email) VALUES ($1) ON CONFLICT (email) DO UPDATE SET email=email RETURNING id",
		googleUser.Email).Scan(&memberID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	m.loginComplete(w, r, memberID, callbackURI)
}
