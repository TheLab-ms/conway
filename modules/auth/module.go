package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/golang-jwt/jwt/v5"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/oauth2/google"
	"golang.org/x/time/rate"
)

//go:generate templ generate

type EmailConfig struct {
	Addr string
	From string
	Auth smtp.Auth
}

// See: https://www.cloudflare.com/application-services/products/turnstile
type TurnstileOptions struct {
	SiteKey string
	Secret  string
}

type Module struct {
	db          *sql.DB
	self        *url.URL
	Sender      EmailSender
	turnstile   *TurnstileOptions
	authLimiter *rate.Limiter
	issuer      *engine.TokenIssuer
}

func New(db *sql.DB, self *url.URL, es EmailSender, tso *TurnstileOptions, iss *engine.TokenIssuer) (*Module, error) {
	m := &Module{db: db, self: self, Sender: es, turnstile: tso, authLimiter: rate.NewLimiter(rate.Every(time.Second), 5), issuer: iss}
	if m.Sender == nil {
		m.Sender = newNoopSender()
	}
	return m, nil
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Minute, m.cleanupLogins))
	mgr.Add(engine.Poll(time.Second, m.processLoginEmail))
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/login", func(r *http.Request, ps httprouter.Params) engine.Response {
		callback := r.URL.Query().Get("callback_uri")
		return engine.Component(renderLoginPage(callback, m.turnstile))
	})

	router.Handle("GET", "/login/code", func(r *http.Request, ps httprouter.Params) engine.Response {
		callback := r.URL.Query().Get("callback_uri")
		return engine.Component(renderLoginCodePage(callback))
	})

	router.Handle("GET", "/whoami", m.WithAuth(func(r *http.Request, ps httprouter.Params) engine.Response {
		return engine.JSON(GetUserMeta(r.Context()))
	}))

	router.Handle("GET", "/logout", func(r *http.Request, ps httprouter.Params) engine.Response {
		callback := r.URL.Query().Get("callback_uri")
		cook := &http.Cookie{Name: "token"}
		return engine.WithCookie(cook, engine.Redirect(callback, http.StatusTemporaryRedirect))
	})

	router.Handle("POST", "/login", m.handleLoginFormPost)
	router.Handle("POST", "/login/code", m.handleLoginCodeFormPost)
}

// WithAuth authenticates incoming requests, or redirects them to the login page.
func (m *Module) WithAuth(next engine.Handler) engine.Handler {
	return func(r *http.Request, p httprouter.Params) engine.Response {
		q := url.Values{}
		q.Add("callback_uri", r.URL.String())

		// Parse the JWT (if provided)
		cook, err := r.Cookie("token")
		if err != nil {
			return engine.Redirect("/login?"+q.Encode(), http.StatusFound)
		}
		claims, err := m.issuer.Verify(cook.Value)
		if err != nil || len(claims.Audience) == 0 || claims.Audience[0] != "conway" {
			return engine.Redirect("/login?"+q.Encode(), http.StatusFound)
		}

		// Get the member from the DB
		var meta UserMetadata
		err = m.db.QueryRowContext(r.Context(), "SELECT id, email, payment_status IS NOT NULL, leadership FROM members WHERE id = ? LIMIT 1", claims.Subject).Scan(&meta.ID, &meta.Email, &meta.ActiveMember, &meta.Leadership)
		if err != nil {
			return engine.Redirect("/login?"+q.Encode(), http.StatusFound)
		}

		r = r.WithContext(withUserMeta(r.Context(), &meta))
		return next(r, p)
	}
}

// handleLoginFormPost starts a login flow for the given member (by email).
func (s *Module) handleLoginFormPost(r *http.Request, p httprouter.Params) engine.Response {
	email := strings.ToLower(r.FormValue("email"))

	if !s.verifyTurnstileResponse(r) {
		return engine.ClientErrorf(401, "We weren't able to verify that you are a human")
	}

	// Find the corresponding member ID or insert a new row if one doesn't exist for this email address
	var memberID int64
	err := s.db.QueryRowContext(r.Context(), "INSERT INTO members (email) VALUES ($1) ON CONFLICT (email) DO UPDATE SET email=email RETURNING id", email).Scan(&memberID)
	if err != nil {
		return engine.Errorf("finding member id: %s", err)
	}

	// Create the login
	_, err = s.db.ExecContext(r.Context(), "INSERT INTO logins (member, code) VALUES ($1, $2);", memberID, generateLoginCode())
	if err != nil {
		return engine.Errorf("creating login: %s", err)
	}

	q := url.Values{}
	q.Add("callback_uri", r.FormValue("callback_uri"))
	return engine.Redirect("/login/code?"+q.Encode(), http.StatusSeeOther)
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		slog.Warn("unable to verify turnstile response - failing open", "error", err, "status", resp.StatusCode)
		return true
	}
	defer resp.Body.Close()

	result := &struct {
		Success bool `json:"success"`
	}{}
	json.NewDecoder(resp.Body).Decode(result)
	return result.Success
}

// handleLoginCodeFormPost allows the user to enter an auth code to get redirected back to where they're headed but with token(s).
func (s *Module) handleLoginCodeFormPost(r *http.Request, p httprouter.Params) engine.Response {
	s.authLimiter.Wait(r.Context())
	code, _ := strconv.ParseInt(r.FormValue("code"), 10, 0)

	var memberID int64
	err := s.db.QueryRowContext(r.Context(), "DELETE FROM logins WHERE code = ? RETURNING member;", code).Scan(&memberID)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.ClientErrorf(403, "Login code is incorrect or has expired")
	}
	if err != nil {
		return engine.Errorf("invalidating login code: %s", err)
	}

	_, err = s.db.ExecContext(r.Context(), "UPDATE members SET confirmed = true WHERE id = ? AND confirmed = false;", memberID)
	if err != nil {
		return engine.Errorf("confirming member email: %s", err)
	}

	exp := time.Now().Add(time.Hour * 24 * 30)
	token, err := s.issuer.Sign(&jwt.RegisteredClaims{
		Issuer:    "conway",
		Subject:   strconv.FormatInt(memberID, 10),
		Audience:  jwt.ClaimStrings{"conway"},
		ExpiresAt: &jwt.NumericDate{Time: exp},
	})
	if err != nil {
		return engine.Errorf("signing jwt: %s", err)
	}
	cook := &http.Cookie{
		Name:     "token",
		Value:    token,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
		Secure:   strings.Contains(s.self.Scheme, "s"),
	}
	return engine.WithCookie(cook,
		engine.Redirect(r.FormValue("callback_uri"), http.StatusFound))
}

// generateLoginCode generates a sufficiently random int that happens to be "6 digits"
func generateLoginCode() int64 {
	const max = 999998
	const min = 100001
	val, err := rand.Int(rand.Reader, big.NewInt(max-min))
	if err != nil {
		panic(fmt.Sprintf("generating random number for login code: %s", err))
	}
	return max - val.Int64()
}

func (m *Module) cleanupLogins(ctx context.Context) bool {
	_, err := m.db.ExecContext(ctx, "DELETE FROM logins WHERE created <= strftime('%s', 'now') - 300 OR send_email_at <= strftime('%s', 'now') - 300;")
	if err != nil {
		slog.Error("unable to clean up logins", "error", err)
		return false
	}
	return false
}

func (m *Module) processLoginEmail(ctx context.Context) bool {
	// Pop item from "queue"
	var id int64
	var code int64
	var email string
	err := m.db.QueryRowContext(ctx, "SELECT logins.id, logins.code, members.email FROM logins JOIN members ON logins.member = members.id WHERE logins.send_email_at > strftime('%s', 'now') - 3600 ORDER BY logins.send_email_at ASC LIMIT 1;").Scan(&id, &code, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		slog.Error("unable to dequeue login workitem", "error", err)
		return false
	}

	slog.Info("sending login email", "loginID", id, "email", email)
	err = m.Sender(ctx, email, "Your Login Code", m.newLoginEmail(code))
	if err != nil {
		slog.Error("unable to send login email", "error", err)
	}
	success := err == nil

	// Update the item's status
	if success {
		_, err = m.db.Exec("UPDATE logins SET send_email_at = NULL WHERE id = $1;", id)
	} else {
		_, err = m.db.Exec("UPDATE logins SET send_email_at = strftime('%s', 'now') + 10 WHERE id = $1;", id)
	}
	if err != nil {
		slog.Error("unable to update status of login workitem", "error", err)
	}

	return success
}

func (m *Module) newLoginEmail(code int64) []byte {
	return []byte(strings.Join([]string{
		"Your login code is:",
		fmt.Sprintf("%d", code),
		"",
		"The code will expire in 5 minutes.",
		"Please ignore this message if you did not request a login code from TheLab Makerspace.",
	}, "\n"))
}

type EmailSender func(ctx context.Context, to, subj string, msg []byte) error

func newNoopSender() EmailSender {
	return func(ctx context.Context, to, subj string, msg []byte) error {
		fmt.Fprintf(os.Stdout, "--- START EMAIL TO %s WITH SUBJECT %q ---\n%s\n--- END EMAIL ---\n", to, subj, msg)
		return nil
	}
}

func NewGoogleSmtpSender(from string) EmailSender {
	creds, err := google.FindDefaultCredentialsWithParams(context.Background(), google.CredentialsParams{
		Scopes:  []string{"https://mail.google.com/"},
		Subject: from,
	})
	if err != nil {
		panic(fmt.Errorf("building google oauth token source: %w", err))
	}

	limiter := rate.NewLimiter(rate.Every(time.Second*5), 1)
	return func(ctx context.Context, to, subj string, msg []byte) error {
		err := limiter.Wait(ctx)
		if err != nil {
			return err
		}

		tok, err := creds.TokenSource.Token()
		if err != nil {
			return fmt.Errorf("getting oauth token: %w", err)
		}
		auth := &googleSmtpOauth{From: from, AccessToken: tok.AccessToken}

		buf := &bytes.Buffer{}
		fmt.Fprintf(buf, "From: TheLab Makerspace\r\n")
		fmt.Fprintf(buf, "To: %s\r\n", to)
		fmt.Fprintf(buf, "Subject: %s\r\n\r\n", subj)
		buf.Write(msg)
		buf.WriteString("\r\n")

		return smtp.SendMail("smtp.gmail.com:587", auth, from, []string{to}, buf.Bytes())
	}
}

type googleSmtpOauth struct {
	From, AccessToken string
}

func (a *googleSmtpOauth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + a.From + "\x01" + "auth=Bearer " + a.AccessToken + "\x01\x01"), nil
}

func (a *googleSmtpOauth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return []byte(""), nil
	}
	return nil, nil
}
