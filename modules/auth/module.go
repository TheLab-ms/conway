package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
	links       *engine.TokenIssuer
	tokens      *engine.TokenIssuer
}

func New(db *sql.DB, self *url.URL, tso *TurnstileOptions, links, tokens *engine.TokenIssuer) *Module {
	return &Module{db: db, self: self, turnstile: tso, authLimiter: rate.NewLimiter(rate.Every(time.Second), 5), links: links, tokens: tokens}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/login", func(r *http.Request) engine.Response {
		if r.URL.Query().Get("t") != "" {
			return m.handleLoginCallbackLink(r)
		}
		callback := r.URL.Query().Get("callback_uri")
		return engine.Component(renderLoginPage(callback, m.turnstile))
	})

	router.Handle("GET", "/login/sent", func(r *http.Request) engine.Response {
		return engine.Component(renderLoginSentPage())
	})

	router.Handle("GET", "/whoami", m.WithAuth(func(r *http.Request) engine.Response {
		return engine.JSON(GetUserMeta(r.Context()))
	}))

	router.Handle("GET", "/logout", func(r *http.Request) engine.Response {
		callback := r.URL.Query().Get("callback_uri")
		cook := &http.Cookie{Name: "token"}
		return engine.WithCookie(cook, engine.Redirect(callback, http.StatusTemporaryRedirect))
	})

	router.Handle("POST", "/login", m.handleLoginFormPost)
}

// WithAuth authenticates incoming requests, or redirects them to the login page.
func (m *Module) WithAuth(next engine.Handler) engine.Handler {
	return func(r *http.Request) engine.Response {
		q := url.Values{}
		q.Add("callback_uri", r.URL.String())

		// Parse the JWT (if provided)
		cook, err := r.Cookie("token")
		if err != nil {
			return engine.Redirect("/login?"+q.Encode(), http.StatusFound)
		}
		claims, err := m.tokens.Verify(cook.Value)
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
		return next(r)
	}
}

// handleLoginFormPost starts a login flow for the given member (by email).
func (s *Module) handleLoginFormPost(r *http.Request) engine.Response {
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

	// Send the login email
	body, err := s.newLoginEmail(memberID, r.FormValue("callback_uri"))
	if err != nil {
		return engine.Errorf("generating login email message: %s", err)
	}
	_, err = s.db.ExecContext(r.Context(), "INSERT INTO outbound_mail (recipient, subject, body) VALUES ($1, 'Makerspace Login', $2);", email, body)
	if err != nil {
		return engine.Errorf("creating login: %s", err)
	}

	return engine.Redirect("/login/sent", http.StatusSeeOther)
}

func (m *Module) newLoginEmail(memberID int64, callback string) (string, error) {
	tok, err := m.links.Sign(&jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(memberID, 10),
		ExpiresAt: &jwt.NumericDate{Time: time.Now().Add(time.Minute * 5)},
	})
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = renderLoginEmail(m.self, tok, callback).Render(context.Background(), &buf)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
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
	if err != nil || resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("unable to verify turnstile response - failing open", "error", err, "status", resp.StatusCode, "body", string(body))
		return true
	}
	defer resp.Body.Close()

	result := &struct {
		Success bool `json:"success"`
	}{}
	json.NewDecoder(resp.Body).Decode(result)
	return result.Success
}

// handleLoginCallbackLink handles requests to the URL sent in login emails.
func (s *Module) handleLoginCallbackLink(r *http.Request) engine.Response {
	s.authLimiter.Wait(r.Context())

	claims, err := s.links.Verify(r.FormValue("t"))
	if err != nil {
		return engine.ClientErrorf(400, "invalid login link")
	}

	_, err = s.db.ExecContext(r.Context(), "UPDATE members SET confirmed = true WHERE id = CAST($1 AS INTEGER) AND confirmed = false;", claims.Subject)
	if err != nil {
		return engine.Errorf("confirming member email: %s", err)
	}

	exp := time.Now().Add(time.Hour * 24 * 30)
	token, err := s.tokens.Sign(&jwt.RegisteredClaims{
		Issuer:    "conway",
		Subject:   claims.Subject,
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
		engine.Redirect(r.FormValue("n"), http.StatusFound))
}
