package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
`

//go:generate go run github.com/a-h/templ/cmd/templ generate

// See: https://www.cloudflare.com/application-services/products/turnstile
type TurnstileOptions struct {
	SiteKey string
	Secret  string
}

type Module struct {
	db          *sql.DB
	self        *url.URL
	turnstile   *TurnstileOptions
	authLimiter *rate.Limiter
	tokens      *engine.TokenIssuer

	// DiscordLoginEnabled is set by the discord module to indicate whether
	// Discord-based login is available. If nil, the Discord login button is hidden.
	DiscordLoginEnabled func(ctx context.Context) bool

	// GoogleLoginEnabled is set by the google module to indicate whether
	// Google-based login is available. If nil, the Google login button is hidden.
	GoogleLoginEnabled func(ctx context.Context) bool

	// OnSignup is called when a new member account is created.
	// Set by external modules (e.g., discord) to send notifications.
	OnSignup func(ctx context.Context, email string, memberID int64)
}

func New(d *sql.DB, self *url.URL, tso *TurnstileOptions, tokens *engine.TokenIssuer) *Module {
	engine.MustMigrate(d, migration)
	return &Module{db: d, self: self, turnstile: tso, authLimiter: rate.NewLimiter(rate.Every(time.Second), 5), tokens: tokens}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		callback := r.URL.Query().Get("callback_uri")
		discordEnabled := m.DiscordLoginEnabled != nil && m.DiscordLoginEnabled(r.Context())
		googleEnabled := m.GoogleLoginEnabled != nil && m.GoogleLoginEnabled(r.Context())
		w.Header().Set("Content-Type", "text/html")
		renderLoginPage(callback, m.turnstile, discordEnabled, googleEnabled).Render(r.Context(), w)
	})

	router.HandleFunc("GET /login/sent", func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("email")
		w.Header().Set("Content-Type", "text/html")
		renderLoginSentPage(email).Render(r.Context(), w)
	})

	router.HandleFunc("POST /login/code", m.handleLoginCodeSubmit)
	router.HandleFunc("GET /login/code", m.handleLoginCodeLink)

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

	router.HandleFunc("POST /login", m.handleLoginFormPost)
	router.HandleFunc("POST /login/confirm-signup", m.handleConfirmSignup)
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

// handleLoginFormPost starts a login flow for the given member (by email).
func (s *Module) handleLoginFormPost(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(r.FormValue("email"))
	callback := r.FormValue("callback_uri")

	if !s.verifyTurnstileResponse(r) {
		engine.ClientError(w, "Verification Failed", "We weren't able to verify that you are a human", 401)
		return
	}

	// Check whether an account already exists for this email
	var memberID int64
	err := s.db.QueryRowContext(r.Context(), "SELECT id FROM members WHERE email = ?", email).Scan(&memberID)
	if err == sql.ErrNoRows {
		// No account exists - show the signup confirmation page
		s.renderSignupConfirmation(w, r, email, "email", callback)
		return
	}
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	s.sendLoginCode(w, r, memberID, email, callback)
}

// sendLoginCode generates a login code for an existing member and sends it via email.
func (s *Module) sendLoginCode(w http.ResponseWriter, r *http.Request, memberID int64, email, callback string) {
	// Generate login code and email
	code, body, err := s.newLoginEmail(r.Context(), memberID, callback)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Queue the email
	_, err = s.db.ExecContext(r.Context(), "INSERT INTO outbound_mail (recipient, subject, body) VALUES ($1, 'Makerspace Login', $2);", email, body)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Redirect to sent page with email for display
	q := url.Values{}
	q.Set("email", email)
	if callback != "" {
		q.Set("callback_uri", callback)
	}
	_ = code // code is stored in DB during newLoginEmail
	http.Redirect(w, r, "/login/sent?"+q.Encode(), http.StatusSeeOther)
}

// renderSignupConfirmation renders the signup confirmation page with a signed token.
// The provider parameter indicates the login method: "email", "discord:<user_id>", or "google".
func (s *Module) renderSignupConfirmation(w http.ResponseWriter, r *http.Request, email, provider, callbackURI string) {
	confirmToken, err := s.tokens.Sign(&jwt.RegisteredClaims{
		Subject:   email,
		Issuer:    provider,
		Audience:  jwt.ClaimStrings{"signup-confirm"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
	})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/html")
	renderSignupConfirmPage(email, confirmToken, callbackURI).Render(r.Context(), w)
}

// handleConfirmSignup handles the signup confirmation form submission.
// It verifies the confirmation token, creates the member, and completes
// the appropriate login flow based on the provider encoded in the token.
func (s *Module) handleConfirmSignup(w http.ResponseWriter, r *http.Request) {
	confirmToken := r.FormValue("confirm_token")
	callbackURI := r.FormValue("callback_uri")

	claims, err := s.tokens.Verify(confirmToken)
	if err != nil || len(claims.Audience) == 0 || claims.Audience[0] != "signup-confirm" {
		engine.ClientError(w, "Expired", "This confirmation has expired. Please start the login process again.", 400)
		return
	}

	email := claims.Subject
	provider := claims.Issuer

	// Create the member
	var memberID int64
	err = s.db.QueryRowContext(r.Context(),
		"INSERT INTO members (email) VALUES ($1) ON CONFLICT (email) DO UPDATE SET email=email RETURNING id",
		email).Scan(&memberID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Notify about new signup
	if s.OnSignup != nil {
		s.OnSignup(r.Context(), email, memberID)
	}

	// Complete the login flow based on the provider
	if strings.HasPrefix(provider, "discord:") {
		// Discord login: link the Discord user ID and complete login directly
		discordUserID := strings.TrimPrefix(provider, "discord:")
		_, err = s.db.ExecContext(r.Context(),
			"UPDATE members SET discord_user_id = ?, discord_email = ?, discord_last_synced = NULL WHERE id = ?",
			discordUserID, email, memberID)
		if err != nil {
			slog.Error("failed to link discord account during signup confirmation", "error", err, "memberID", memberID)
		}
		s.CompleteLoginForMember(w, r, memberID, callbackURI)
		return
	}

	if provider == "google" {
		// Google login: complete login directly (no extra fields to link)
		s.CompleteLoginForMember(w, r, memberID, callbackURI)
		return
	}

	// Email login: send a login code
	s.sendLoginCode(w, r, memberID, email, callbackURI)
}

// RenderSignupConfirmation renders the signup confirmation page.
// This is exported so that OAuth login modules can use it when they detect a new account.
func (s *Module) RenderSignupConfirmation(w http.ResponseWriter, r *http.Request, email, provider, callbackURI string) {
	s.renderSignupConfirmation(w, r, email, provider, callbackURI)
}

// generateLoginCode generates a cryptographically secure 5-digit code.
func generateLoginCode() (string, error) {
	var n uint32
	if err := binary.Read(rand.Reader, binary.BigEndian, &n); err != nil {
		return "", err
	}
	return fmt.Sprintf("%05d", n%100000), nil
}

func (m *Module) newLoginEmail(ctx context.Context, memberID int64, callback string) (code string, body string, err error) {
	expiresAt := time.Now().Add(time.Minute * 5)

	tok, err := m.tokens.Sign(&jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(memberID, 10),
		ExpiresAt: &jwt.NumericDate{Time: expiresAt},
	})
	if err != nil {
		return "", "", err
	}

	// Generate a unique 5-digit code
	for attempts := 0; attempts < 3; attempts++ {
		code, err = generateLoginCode()
		if err != nil {
			return "", "", err
		}

		// Try to insert the code (will fail if code already exists due to PRIMARY KEY)
		_, err = m.db.ExecContext(ctx,
			"INSERT INTO login_codes (code, token, email, callback, expires_at) VALUES (?, ?, (SELECT email FROM members WHERE id = ?), ?, ?)",
			code, tok, memberID, callback, expiresAt.Unix(),
		)
		if err == nil {
			break
		}
		// If we get here, code collision occurred, try again
		if attempts == 2 {
			return "", "", fmt.Errorf("failed to generate unique login code after 3 attempts")
		}
	}

	var buf bytes.Buffer
	err = renderLoginEmail(m.self, code).Render(context.Background(), &buf)
	if err != nil {
		return "", "", err
	}
	return code, buf.String(), nil
}

func (s *Module) verifyTurnstileResponse(r *http.Request) bool {
	if s.turnstile == nil {
		return true // fail open
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Second*3)
	defer cancel()

	tsr := r.FormValue("cf-turnstile-response")
	if tsr == "" {
		return false
	}
	form := url.Values{}
	form.Set("response", tsr)
	form.Set("secret", s.turnstile.Secret)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return true
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("unable to verify turnstile response - failing open", "error", err)
		return true
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("unable to verify turnstile response - failing open", "status", resp.StatusCode, "body", string(body))
		return true
	}

	result := &struct {
		Success bool `json:"success"`
	}{}
	json.NewDecoder(resp.Body).Decode(result)
	return result.Success
}

// handleLoginCodeSubmit handles code entry from the login sent page form.
func (s *Module) handleLoginCodeSubmit(w http.ResponseWriter, r *http.Request) {
	s.authLimiter.Wait(r.Context())

	code := r.FormValue("code")
	s.verifyCodeAndLogin(w, r, code)
}

// handleLoginCodeLink handles short link clicks from email (GET /login/code?code=xxxxx).
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

	_, err = s.db.ExecContext(r.Context(), "UPDATE members SET confirmed = true WHERE id = CAST($1 AS INTEGER) AND confirmed = false;", claims.Subject)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	exp := time.Now().Add(time.Hour * 24 * 30)
	sessionToken, err := s.tokens.Sign(&jwt.RegisteredClaims{
		Issuer:    "conway",
		Subject:   claims.Subject,
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

	if callback == "" {
		callback = "/"
	}
	http.Redirect(w, r, callback, http.StatusFound)
}

// CompleteLoginForMember creates a session for the given member ID and redirects.
// This is used by external login providers (e.g. Discord OAuth) to finish a login flow.
func (m *Module) CompleteLoginForMember(w http.ResponseWriter, r *http.Request, memberID int64, callbackURI string) {
	internalToken, err := m.tokens.Sign(&jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(memberID, 10),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	m.completeLogin(w, r, internalToken, callbackURI)
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
