package email

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/smtp"
	"os"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"golang.org/x/oauth2/google"
	"golang.org/x/time/rate"
)

type Sender func(ctx context.Context, to, subj string, msg []byte) error

type Module struct {
	db          *sql.DB
	Sender      Sender
	authLimiter *rate.Limiter
}

func New(db *sql.DB, es Sender) *Module {
	m := &Module{db: db, Sender: es, authLimiter: rate.NewLimiter(rate.Every(time.Second*3), 3)}
	if m.Sender == nil {
		m.Sender = newNoopSender()
	}
	return m
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Second, m.processLoginEmail))
}

func (m *Module) processLoginEmail(ctx context.Context) bool {
	var id int64
	var to, subj, body string
	err := m.db.QueryRowContext(ctx, "SELECT id, recipient, subject, body FROM outbound_mail WHERE send_at > strftime('%s', 'now') - 3600 ORDER BY send_at ASC LIMIT 1;").Scan(&id, &to, &subj, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		slog.Error("unable to dequeue outbound mail workitem", "error", err)
		return false
	}

	slog.Info("sending email", "id", id, "to", to, "subject", subj)
	err = m.Sender(ctx, to, subj, []byte(body))
	if err != nil {
		slog.Error("unable to send email", "error", err)
	}
	success := err == nil

	// Update the item's status
	if success {
		_, err = m.db.Exec("UPDATE outbound_mail SET send_at = NULL WHERE id = $1;", id)
	} else {
		_, err = m.db.Exec("UPDATE outbound_mail SET send_at = strftime('%s', 'now') + 10 WHERE id = $1;", id)
	}
	if err != nil {
		slog.Error("unable to update status of outbound email", "error", err)
		return false
	}

	return success
}

func newNoopSender() Sender {
	return func(ctx context.Context, to, subj string, msg []byte) error {
		fmt.Fprintf(os.Stdout, "--- START EMAIL TO %s WITH SUBJECT %q ---\n%s\n--- END EMAIL ---\n", to, subj, msg)
		return nil
	}
}

func NewGoogleSmtpSender(from string) Sender {
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
		fmt.Fprintf(buf, "Subject: %s\r\n", subj)
		fmt.Fprintf(buf, "MIME-version: 1.0;\r\n")
		fmt.Fprintf(buf, "Content-Type: text/html; charset=\"UTF-8\";\r\n\r\n")
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
