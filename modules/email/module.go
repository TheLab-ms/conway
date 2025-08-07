package email

import (
	"bytes"
	"context"
	"database/sql"
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
	db     *sql.DB
	Sender Sender
}

func New(db *sql.DB, es Sender) *Module {
	m := &Module{db: db, Sender: es}
	if m.Sender == nil {
		m.Sender = newNoopSender()
	}
	return m
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, 1))))
}

func (m *Module) GetItem(ctx context.Context) (message, error) {
	var item message
	err := m.db.QueryRowContext(ctx, "SELECT id, recipient, subject, body, created FROM outbound_mail WHERE unixepoch() >= send_at AND unixepoch() - created < 3600 ORDER BY send_at ASC LIMIT 1;").Scan(&item.ID, &item.To, &item.Subject, &item.Body, &item.Created)
	return item, err
}

func (m *Module) ProcessItem(ctx context.Context, item message) error {
	slog.Info("sending email", "id", item.ID, "to", item.To, "subject", item.Subject)
	return m.Sender(ctx, item.To, item.Subject, []byte(item.Body))
}

func (m *Module) UpdateItem(ctx context.Context, item message, success bool) (err error) {
	if success {
		_, err = m.db.Exec("DELETE FROM outbound_mail WHERE id = $1;", item.ID)
	} else {
		_, err = m.db.Exec("UPDATE outbound_mail SET send_at = unixepoch() + ((send_at - created) * 2) WHERE id = $1;", item.ID)
	}
	return err
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

type message struct {
	ID      int64
	To      string
	Subject string
	Body    string
	Created int64
}

func (m *message) String() string { return fmt.Sprintf("id=%d", m.ID) }
