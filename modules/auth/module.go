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
	tokens      *engine.TokenIssuer
}

func New(db *sql.DB, self *url.URL, tso *TurnstileOptions, tokens *engine.TokenIssuer) *Module {
	return &Module{db: db, self: self, turnstile: tso, authLimiter: rate.NewLimiter(rate.Every(time.Second), 5), tokens: tokens}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "" {
			m.handleLoginCallbackLink(w, r)
			return
		}
		callback := r.URL.Query().Get("callback_uri")
		w.Header().Set("Content-Type", "text/html")
		renderLoginPage(callback, m.turnstile).Render(r.Context(), w)
	})

	router.HandleFunc("GET /login/sent", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		renderLoginSentPage().Render(r.Context(), w)
	})

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
			http.Error(w, "You must be a member of leadership to access this page", 403)
			return
		}
		next(w, r)
	})
}

// handleLoginFormPost starts a login flow for the given member (by email).
func (s *Module) handleLoginFormPost(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(r.FormValue("email"))

	if !s.verifyTurnstileResponse(r) {
		http.Error(w, "We weren't able to verify that you are a human", 401)
		return
	}

	// Find the corresponding member ID or insert a new row if one doesn't exist for this email address
	var memberID int64
	err := s.db.QueryRowContext(r.Context(), "INSERT INTO members (email) VALUES ($1) ON CONFLICT (email) DO UPDATE SET email=email RETURNING id", email).Scan(&memberID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Send the login email
	body, err := s.newLoginEmail(memberID, r.FormValue("callback_uri"))
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	_, err = s.db.ExecContext(r.Context(), "INSERT INTO outbound_mail (recipient, subject, body) VALUES ($1, 'Makerspace Login', $2);", email, body)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.Redirect(w, r, "/login/sent", http.StatusSeeOther)
}

func (m *Module) newLoginEmail(memberID int64, callback string) (string, error) {
	tok, err := m.tokens.Sign(&jwt.RegisteredClaims{
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
func (s *Module) handleLoginCallbackLink(w http.ResponseWriter, r *http.Request) {
	s.authLimiter.Wait(r.Context())

	claims, err := s.tokens.Verify(r.FormValue("t"))
	if err != nil {
		http.Error(w, "invalid login link", 400)
		return
	}

	_, err = s.db.ExecContext(r.Context(), "UPDATE members SET confirmed = true WHERE id = CAST($1 AS INTEGER) AND confirmed = false;", claims.Subject)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	exp := time.Now().Add(time.Hour * 24 * 30)
	token, err := s.tokens.Sign(&jwt.RegisteredClaims{
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
		Value:    token,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
		Secure:   strings.Contains(s.self.Scheme, "s"),
	}
	http.SetCookie(w, cook)
	http.Redirect(w, r, r.FormValue("n"), http.StatusFound)
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
