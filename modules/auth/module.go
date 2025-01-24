package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
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
	"golang.org/x/time/rate"
)

// TODO: Key rotation

//go:generate templ generate

type EmailConfig struct {
	Addr string
	From string
	Auth smtp.Auth
}

type Module struct {
	Mailer func(ctx context.Context, to, subj string, msg []byte) bool

	SigningKey   *rsa.PrivateKey
	signingKeyID int64

	db   *sql.DB
	self *url.URL
}

func New(db *sql.DB, self *url.URL, ec *EmailConfig) (*Module, error) {
	m := &Module{db: db, self: self}

	// SMTP is optional - log to the console if not configured
	if ec == nil {
		m.Mailer = devEmailSender
	} else {
		m.Mailer = m.newEmailSender(ec)
	}

	// Generate or load the JWT signing key
read:
	var keyPEM string
	err := db.QueryRow("SELECT id, key_pem FROM keys WHERE label = 'jwt' ORDER BY id DESC LIMIT 1").Scan(&m.signingKeyID, &keyPEM)
	if errors.Is(err, sql.ErrNoRows) {
		slog.Info("generating jwt signing key...")
		pkey, err := rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			return nil, err
		}
		_, err = db.Exec("INSERT INTO keys (key_pem, label) VALUES (?, 'jwt')", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(pkey)})))
		if err != nil {
			return nil, err
		}
		goto read
	}
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode([]byte(keyPEM))
	m.SigningKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	return m, nil
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Minute, m.cleanupLogins))
	mgr.Add(engine.Poll(time.Second, m.processLoginEmail))
	mgr.Add(engine.Poll(time.Minute, m.pruneSpamMembers))
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/login", func(r *http.Request, ps httprouter.Params) engine.Response {
		callback := r.URL.Query().Get("callback_uri")
		return engine.Component(renderLoginPage(callback))
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
		claims := &jwt.RegisteredClaims{}
		tok, err := jwt.ParseWithClaims(cook.Value, claims, func(token *jwt.Token) (interface{}, error) { return &m.SigningKey.PublicKey, nil })
		if err != nil || !tok.Valid || len(claims.Audience) == 0 || claims.Audience[0] != "conway" {
			return engine.Redirect("/login?"+q.Encode(), http.StatusFound)
		}

		// Get the member from the DB
		var meta UserMetadata
		err = m.db.QueryRowContext(r.Context(), "SELECT email, active, leadership FROM members WHERE id = ? LIMIT 1", claims.Subject).Scan(&meta.Email, &meta.ActiveMember, &meta.Leadership)
		if err != nil {
			return engine.Redirect("/login?"+q.Encode(), http.StatusFound)
		}

		r = r.WithContext(withUserMeta(r.Context(), &meta))
		return next(r, p)
	}
}

// handleLoginFormPost starts a login flow for the given member (by email).
func (s *Module) handleLoginFormPost(r *http.Request, p httprouter.Params) engine.Response {
	email := r.FormValue("email")

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

// handleLoginCodeFormPost allows the user to enter an auth code to get redirected back to where they're headed but with token(s).
func (s *Module) handleLoginCodeFormPost(r *http.Request, p httprouter.Params) engine.Response {
	code, _ := strconv.ParseInt(r.FormValue("code"), 10, 0)

	var memberID int64
	err := s.db.QueryRowContext(r.Context(), "DELETE FROM logins WHERE code = ? RETURNING member;", code).Scan(&memberID)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.Errorf("attempt to log in with unknown code")
	}
	if err != nil {
		return engine.Errorf("invalidating login code: %s", err)
	}

	_, err = s.db.ExecContext(r.Context(), "UPDATE members SET confirmed = true WHERE id = ? AND confirmed = false;", memberID)
	if err != nil {
		return engine.Errorf("confirming member email: %s", err)
	}

	exp := time.Now().Add(time.Hour * 24 * 30)
	token, err := s.signToken(strconv.FormatInt(memberID, 10), exp)
	if err != nil {
		return engine.Errorf("signing jwt: %s", err)
	}
	cook := &http.Cookie{
		Name:     "token",
		Value:    token,
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
		Expires:  exp,
		Secure:   strings.Contains(s.self.Scheme, "s"),
	}
	return engine.WithCookie(cook,
		engine.Redirect(r.FormValue("callback_uri"), http.StatusFound))
}

func (s *Module) signToken(subj string, exp time.Time) (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodRS512, &jwt.RegisteredClaims{
		Issuer:    "conway",
		Subject:   subj,
		Audience:  jwt.ClaimStrings{"conway"},
		ExpiresAt: &jwt.NumericDate{Time: exp},
	})
	tok.Header["kid"] = strconv.FormatInt(s.signingKeyID, 10)
	return tok.SignedString(s.SigningKey)
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

// TODO: Emit events for pruned items
func (m *Module) pruneSpamMembers(ctx context.Context) bool {
	_, err := m.db.ExecContext(ctx, "DELETE FROM members WHERE created <= strftime('%s', 'now') - 86400 AND confirmed = 0;")
	if err != nil {
		slog.Error("unable to clean up spam members", "error", err)
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
	success := m.Mailer(ctx, email, "Makerspace Login Code", m.newLoginEmail(code))

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
		"Here is your login code:",
		fmt.Sprintf("%d", code),
	}, "\n"))
}

func (m *Module) newEmailSender(conf *EmailConfig) func(c context.Context, to, subj string, msg []byte) bool {
	limiter := rate.NewLimiter(rate.Every(time.Second*5), 1)
	return func(ctx context.Context, to, subj string, msg []byte) bool {
		buf := &bytes.Buffer{}
		fmt.Fprintf(buf, "To: %s\r\n", to)
		fmt.Fprintf(buf, "Subject: %s\r\n\r\n", subj)
		buf.Write(msg)
		buf.WriteString("\r\n")

		err := smtp.SendMail(conf.Addr, conf.Auth, conf.From, []string{to}, buf.Bytes())
		if err != nil {
			slog.Error("error while sending email", "to", to, "error", err)
			return false
		}

		limiter.Wait(ctx)
		return true
	}
}

// devEmailSender just "sends" emails by logging them to stdout.
func devEmailSender(ctx context.Context, to, subj string, msg []byte) bool {
	fmt.Fprintf(os.Stdout, "--- START EMAIL TO %s WITH SUBJECT %q ---\n%s\n--- END EMAIL ---\n", to, subj, msg)
	return true
}
