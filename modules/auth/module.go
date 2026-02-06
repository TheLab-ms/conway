package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"
)

const migration = `
/* Login Codes - Maps 5-digit codes to JWT tokens for passwordless login */
CREATE TABLE IF NOT EXISTS login_codes (
    code TEXT PRIMARY KEY,
    token TEXT NOT NULL,
    email TEXT NOT NULL,
    callback TEXT NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
) STRICT;

CREATE INDEX IF NOT EXISTS login_codes_expires_at_idx ON login_codes (expires_at);
CREATE INDEX IF NOT EXISTS login_codes_email_idx ON login_codes (email);

/* OAuth config - Google and Discord OAuth credentials for login */
CREATE TABLE IF NOT EXISTS auth_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    google_client_id TEXT NOT NULL DEFAULT '',
    google_client_secret TEXT NOT NULL DEFAULT '',
    discord_client_id TEXT NOT NULL DEFAULT '',
    discord_client_secret TEXT NOT NULL DEFAULT ''
) STRICT;
`

//go:generate go run github.com/a-h/templ/cmd/templ generate

type Module struct {
	db          *sql.DB
	self        *url.URL
	authLimiter *rate.Limiter
	tokens      *engine.TokenIssuer
}

func New(d *sql.DB, self *url.URL, tokens *engine.TokenIssuer) *Module {
	engine.MustMigrate(d, migration)
	return &Module{db: d, self: self, authLimiter: rate.NewLimiter(rate.Every(time.Second), 5), tokens: tokens}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /login", m.handleLoginPage)

	router.HandleFunc("POST /login/code", m.handleLoginCodeSubmit)
	router.HandleFunc("GET /login/code", m.handleLoginCodeLink)

	// OAuth login
	router.HandleFunc("GET /login/google", m.handleGoogleLogin)
	router.HandleFunc("GET /login/google/callback", m.handleGoogleCallback)
	router.HandleFunc("GET /login/discord", m.handleDiscordLogin)
	router.HandleFunc("GET /login/discord/callback", m.handleDiscordCallback)

	router.HandleFunc("GET /whoami", m.WithAuthn(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(GetUserMeta(r.Context()))
	}))

	router.HandleFunc("GET /logout", func(w http.ResponseWriter, r *http.Request) {
		callback := r.URL.Query().Get("callback_uri")
		cook := &http.Cookie{Name: "token"}
		http.SetCookie(w, cook)
		http.Redirect(w, r, callback, http.StatusTemporaryRedirect)
	})
}

// WithAuthn authenticates incoming requests, or redirects them to the login page.
func (m *Module) WithAuthn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := url.Values{}
		q.Add("callback_uri", r.URL.String())

		// Parse the JWT (if provided)
		cook, err := r.Cookie("token")
		if err != nil {
			http.Redirect(w, r, "/login?"+q.Encode(), http.StatusFound)
			return
		}
		claims, err := m.tokens.Verify(cook.Value)
		if err != nil || len(claims.Audience) == 0 || claims.Audience[0] != "conway" {
			http.Redirect(w, r, "/login?"+q.Encode(), http.StatusFound)
			return
		}

		// Get the member from the DB
		var meta UserMetadata
		err = m.db.QueryRowContext(r.Context(), "SELECT id, email, payment_status IS NOT NULL, leadership FROM members WHERE id = ? LIMIT 1", claims.Subject).Scan(&meta.ID, &meta.Email, &meta.ActiveMember, &meta.Leadership)
		if err != nil {
			http.Redirect(w, r, "/login?"+q.Encode(), http.StatusFound)
			return
		}

		r = r.WithContext(withUserMeta(r.Context(), &meta))
		next(w, r)
	}
}

// WithLeadership wraps WithAuthn and additionally requires the user to be a member of leadership.
func (m *Module) WithLeadership(next http.HandlerFunc) http.HandlerFunc {
	return m.WithAuthn(func(w http.ResponseWriter, r *http.Request) {
		if meta := GetUserMeta(r.Context()); meta == nil || !meta.Leadership {
			engine.ClientError(w, "Access Denied", "You must be a member of leadership to access this page", 403)
			return
		}
		next(w, r)
	})
}

// handleLoginPage renders the login page or handles admin-generated token logins (?t= parameter).
func (m *Module) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// Handle admin-generated login token (?t= parameter)
	if tok := r.URL.Query().Get("t"); tok != "" {
		m.handleTokenLogin(w, r, tok)
		return
	}

	callback := r.URL.Query().Get("callback_uri")
	cfg := m.loadOAuthConfig(r.Context())
	w.Header().Set("Content-Type", "text/html")
	renderLoginPage(callback, cfg.GoogleClientID != "", cfg.DiscordClientID != "").Render(r.Context(), w)
}

// handleTokenLogin handles login via admin-generated JWT token (e.g., from QR code).
func (m *Module) handleTokenLogin(w http.ResponseWriter, r *http.Request, tok string) {
	claims, err := m.tokens.Verify(tok)
	if err != nil {
		engine.ClientError(w, "Invalid Link", "The login link is invalid or has expired", 400)
		return
	}

	memberID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid Link", "The login link is invalid", 400)
		return
	}

	m.completeLoginByMemberID(w, r, memberID, "/")
}

// generateLoginCode generates a cryptographically secure 5-digit code.
func generateLoginCode() (string, error) {
	var n uint32
	if err := binary.Read(rand.Reader, binary.BigEndian, &n); err != nil {
		return "", err
	}
	return fmt.Sprintf("%05d", n%100000), nil
}

// GenerateLoginCode creates a 5-digit login code for the given member without sending email.
// Used by the admin module to generate codes for members.
func (m *Module) GenerateLoginCode(ctx context.Context, memberID int64) (string, error) {
	expiresAt := time.Now().Add(time.Minute * 5)

	tok, err := m.tokens.Sign(&jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(memberID, 10),
		ExpiresAt: &jwt.NumericDate{Time: expiresAt},
	})
	if err != nil {
		return "", err
	}

	var code string
	for attempts := 0; attempts < 3; attempts++ {
		code, err = generateLoginCode()
		if err != nil {
			return "", err
		}

		_, err = m.db.ExecContext(ctx,
			"INSERT INTO login_codes (code, token, email, callback, expires_at) VALUES (?, ?, (SELECT email FROM members WHERE id = ?), ?, ?)",
			code, tok, memberID, "", expiresAt.Unix(),
		)
		if err == nil {
			return code, nil
		}
		if attempts == 2 {
			return "", fmt.Errorf("failed to generate unique login code after 3 attempts")
		}
	}
	return code, nil
}

// handleLoginCodeSubmit handles code entry from the login page form.
func (s *Module) handleLoginCodeSubmit(w http.ResponseWriter, r *http.Request) {
	s.authLimiter.Wait(r.Context())

	code := r.FormValue("code")
	s.verifyCodeAndLogin(w, r, code)
}

// handleLoginCodeLink handles link clicks with code (GET /login/code?code=xxxxx).
func (s *Module) handleLoginCodeLink(w http.ResponseWriter, r *http.Request) {
	s.authLimiter.Wait(r.Context())

	code := r.URL.Query().Get("code")
	s.verifyCodeAndLogin(w, r, code)
}

// verifyCodeAndLogin looks up a login code and completes the login flow.
func (s *Module) verifyCodeAndLogin(w http.ResponseWriter, r *http.Request, code string) {
	// Validate code format
	if len(code) != 5 {
		engine.ClientError(w, "Invalid Code", "The code you entered is invalid", 400)
		return
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			engine.ClientError(w, "Invalid Code", "The code you entered is invalid", 400)
			return
		}
	}

	// Look up code in database
	var token, callback string
	var expiresAt int64
	err := s.db.QueryRowContext(r.Context(),
		"SELECT token, callback, expires_at FROM login_codes WHERE code = ?",
		code).Scan(&token, &callback, &expiresAt)
	if err == sql.ErrNoRows {
		engine.ClientError(w, "Invalid Code", "The code you entered is invalid or has expired", 400)
		return
	}
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Check expiration
	if time.Now().Unix() > expiresAt {
		s.db.ExecContext(r.Context(), "DELETE FROM login_codes WHERE code = ?", code)
		engine.ClientError(w, "Code Expired", "The login code has expired - please request a new one", 400)
		return
	}

	// Delete code (single use)
	s.db.ExecContext(r.Context(), "DELETE FROM login_codes WHERE code = ?", code)

	// Complete login with the stored token
	s.completeLogin(w, r, token, callback)
}

// completeLogin verifies a JWT token and sets up the session.
func (s *Module) completeLogin(w http.ResponseWriter, r *http.Request, token, callback string) {
	claims, err := s.tokens.Verify(token)
	if err != nil {
		engine.ClientError(w, "Invalid Link", "The login link is invalid or has expired", 400)
		return
	}

	memberID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid Link", "The login link is invalid", 400)
		return
	}

	s.completeLoginByMemberID(w, r, memberID, callback)
}

// completeLoginByMemberID creates a session for the given member ID.
func (s *Module) completeLoginByMemberID(w http.ResponseWriter, r *http.Request, memberID int64, callback string) {
	_, err := s.db.ExecContext(r.Context(), "UPDATE members SET confirmed = true WHERE id = ? AND confirmed = false;", memberID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	exp := time.Now().Add(time.Hour * 24 * 30)
	sessionToken, err := s.tokens.Sign(&jwt.RegisteredClaims{
		Issuer:    "conway",
		Subject:   strconv.FormatInt(memberID, 10),
		Audience:  jwt.ClaimStrings{"conway"},
		ExpiresAt: &jwt.NumericDate{Time: exp},
	})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	cook := &http.Cookie{
		Name:     "token",
		Value:    sessionToken,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
		Secure:   strings.Contains(s.self.Scheme, "s"),
	}
	http.SetCookie(w, cook)

	if callback == "" || !strings.HasPrefix(callback, "/") {
		callback = "/"
	}
	http.Redirect(w, r, callback, http.StatusFound)
}

// loadOAuthConfig loads the latest OAuth configuration from the database.
func (m *Module) loadOAuthConfig(ctx context.Context) *OAuthConfig {
	cfg := &OAuthConfig{}
	m.db.QueryRowContext(ctx,
		"SELECT google_client_id, google_client_secret, discord_client_id, discord_client_secret FROM auth_config ORDER BY version DESC LIMIT 1").
		Scan(&cfg.GoogleClientID, &cfg.GoogleClientSecret, &cfg.DiscordClientID, &cfg.DiscordClientSecret)
	return cfg
}

// OnlyLAN returns a 403 error if the request is coming from the internet.
func OnlyLAN(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("CF-Connecting-IP") != "" {
			w.WriteHeader(403)
			return
		}
		next(w, r)
	}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "expired login codes",
		"DELETE FROM login_codes WHERE expires_at < unixepoch()")))
}
