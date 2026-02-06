package auth

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var discordOAuthEndpoint = oauth2.Endpoint{
	AuthURL:   "https://discord.com/api/oauth2/authorize",
	TokenURL:  "https://discord.com/api/oauth2/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

// handleGoogleLogin redirects the user to Google's OAuth consent screen.
func (m *Module) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	cfg := m.loadOAuthConfig(r.Context())
	if cfg.GoogleClientID == "" || cfg.GoogleClientSecret == "" {
		renderOAuthError(w, r, "Google login is not configured")
		return
	}

	oauthCfg := &oauth2.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{"openid", "email"},
		RedirectURL:  fmt.Sprintf("%s/login/google/callback", m.self.String()),
	}

	callback := r.URL.Query().Get("callback_uri")
	state, err := m.signOAuthState(callback)
	if err != nil {
		renderOAuthError(w, r, "Failed to generate state")
		return
	}

	http.Redirect(w, r, oauthCfg.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

// handleGoogleCallback processes the Google OAuth callback.
func (m *Module) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	m.authLimiter.Wait(r.Context())

	callback, err := m.verifyOAuthState(r.URL.Query().Get("state"))
	if err != nil {
		renderOAuthError(w, r, "Invalid or expired state")
		return
	}

	cfg := m.loadOAuthConfig(r.Context())
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{"openid", "email"},
		RedirectURL:  fmt.Sprintf("%s/login/google/callback", m.self.String()),
	}

	token, err := oauthCfg.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		slog.Error("google oauth token exchange failed", "error", err)
		renderOAuthError(w, r, "Login failed - please try again")
		return
	}

	// Fetch user info
	client := oauthCfg.Client(r.Context(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		slog.Error("google oauth userinfo fetch failed", "error", err)
		renderOAuthError(w, r, "Login failed - please try again")
		return
	}
	defer resp.Body.Close()

	var userInfo struct {
		Email         string `json:"email"`
		VerifiedEmail bool   `json:"verified_email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		slog.Error("google oauth userinfo decode failed", "error", err)
		renderOAuthError(w, r, "Login failed - please try again")
		return
	}

	if !userInfo.VerifiedEmail || userInfo.Email == "" {
		renderOAuthError(w, r, "Your Google account email is not verified")
		return
	}

	m.completeOAuthLogin(w, r, userInfo.Email, callback)
}

// handleDiscordLogin redirects the user to Discord's OAuth consent screen.
func (m *Module) handleDiscordLogin(w http.ResponseWriter, r *http.Request) {
	cfg := m.loadOAuthConfig(r.Context())
	if cfg.DiscordClientID == "" || cfg.DiscordClientSecret == "" {
		renderOAuthError(w, r, "Discord login is not configured")
		return
	}

	oauthCfg := &oauth2.Config{
		ClientID:     cfg.DiscordClientID,
		ClientSecret: cfg.DiscordClientSecret,
		Endpoint:     discordOAuthEndpoint,
		Scopes:       []string{"identify", "email"},
		RedirectURL:  fmt.Sprintf("%s/login/discord/callback", m.self.String()),
	}

	callback := r.URL.Query().Get("callback_uri")
	state, err := m.signOAuthState(callback)
	if err != nil {
		renderOAuthError(w, r, "Failed to generate state")
		return
	}

	http.Redirect(w, r, oauthCfg.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

// handleDiscordCallback processes the Discord OAuth callback.
func (m *Module) handleDiscordCallback(w http.ResponseWriter, r *http.Request) {
	m.authLimiter.Wait(r.Context())

	callback, err := m.verifyOAuthState(r.URL.Query().Get("state"))
	if err != nil {
		renderOAuthError(w, r, "Invalid or expired state")
		return
	}

	cfg := m.loadOAuthConfig(r.Context())
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.DiscordClientID,
		ClientSecret: cfg.DiscordClientSecret,
		Endpoint:     discordOAuthEndpoint,
		Scopes:       []string{"identify", "email"},
		RedirectURL:  fmt.Sprintf("%s/login/discord/callback", m.self.String()),
	}

	token, err := oauthCfg.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		slog.Error("discord oauth token exchange failed", "error", err)
		renderOAuthError(w, r, "Login failed - please try again")
		return
	}

	// Fetch user info
	client := oauthCfg.Client(r.Context(), token)
	resp, err := client.Get("https://discord.com/api/users/@me")
	if err != nil {
		slog.Error("discord oauth userinfo fetch failed", "error", err)
		renderOAuthError(w, r, "Login failed - please try again")
		return
	}
	defer resp.Body.Close()

	var userInfo struct {
		Email    string `json:"email"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		slog.Error("discord oauth userinfo decode failed", "error", err)
		renderOAuthError(w, r, "Login failed - please try again")
		return
	}

	if !userInfo.Verified || userInfo.Email == "" {
		renderOAuthError(w, r, "Your Discord account email is not verified")
		return
	}

	m.completeOAuthLogin(w, r, userInfo.Email, callback)
}

// completeOAuthLogin looks up or creates a member by email and logs them in.
func (m *Module) completeOAuthLogin(w http.ResponseWriter, r *http.Request, email, callback string) {
	email = strings.ToLower(email)

	var memberID int64
	err := m.db.QueryRowContext(r.Context(),
		"INSERT INTO members (email, confirmed) VALUES ($1, true) ON CONFLICT (email) DO UPDATE SET confirmed = true RETURNING id",
		email).Scan(&memberID)
	if err != nil {
		slog.Error("oauth login member upsert failed", "error", err, "email", email)
		renderOAuthError(w, r, "Login failed - please try again")
		return
	}

	m.completeLoginByMemberID(w, r, memberID, callback)
}

// signOAuthState creates a signed JWT state parameter for OAuth flows.
func (m *Module) signOAuthState(callback string) (string, error) {
	return m.tokens.Sign(&jwt.RegisteredClaims{
		Subject:   callback,
		Audience:  jwt.ClaimStrings{"login-oauth"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	})
}

// verifyOAuthState verifies the state parameter and returns the callback URI.
func (m *Module) verifyOAuthState(state string) (callback string, err error) {
	claims, err := m.tokens.Verify(state)
	if err != nil {
		return "", err
	}
	if len(claims.Audience) == 0 || claims.Audience[0] != "login-oauth" {
		return "", fmt.Errorf("invalid state audience")
	}
	callback = claims.Subject
	if callback != "" && !strings.HasPrefix(callback, "/") {
		callback = "/"
	}
	return callback, nil
}

// renderOAuthError renders a user-facing OAuth error page.
func renderOAuthError(w http.ResponseWriter, r *http.Request, message string) {
	q := url.Values{}
	q.Set("error", message)
	http.Redirect(w, r, "/login?"+q.Encode(), http.StatusFound)
}
