package email

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/db"
	"github.com/TheLab-ms/conway/engine/settings"
)

const migration = `
CREATE TABLE IF NOT EXISTS outbound_mail (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    send_at INTEGER DEFAULT (strftime('%s', 'now')),
    recipient TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS outbound_mail_send_at_idx ON outbound_mail (send_at);
`

const maxRPS = 1

type Sender func(ctx context.Context, to, subj string, msg []byte) error

type Module struct {
	db       *sql.DB
	settings *settings.Store
	sender   atomic.Pointer[Sender]
}

func New(d *sql.DB, settingsStore *settings.Store) *Module {
	db.MustMigrate(d, migration)

	settingsStore.RegisterSection(settings.Section{
		Title: "Gmail",
		Fields: []settings.Field{
			{Key: "email.from", Label: "From Address", Description: "Sender email address (e.g., noreply@example.com)"},
			{Key: "email.google_service_account", Label: "Google Service Account JSON", Description: "Service account credentials for sending emails via Gmail API", Sensitive: true, Type: settings.FieldTypeTextArea},
		},
	})

	m := &Module{db: d, settings: settingsStore}

	// Set default noop sender
	noopSender := newNoopSender()
	m.sender.Store(&noopSender)

	return m
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	ctx := context.Background()

	// Watch for email.from changes
	m.settings.Watch(ctx, "email.from", func(from string) {
		if from != "" {
			sender := NewGoogleSmtpSender(from)
			m.sender.Store(&sender)
			slog.Info("email sender configured", "from", from)
		} else {
			noopSender := newNoopSender()
			m.sender.Store(&noopSender)
			slog.Info("email sender using noop (no from address configured)")
		}
	})

	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, maxRPS))))
}

func (m *Module) GetItem(ctx context.Context) (message, error) {
	var item message
	err := m.db.QueryRowContext(ctx, "SELECT id, recipient, subject, body, created FROM outbound_mail WHERE unixepoch() >= send_at AND unixepoch() - created < 3600 ORDER BY send_at ASC LIMIT 1;").Scan(&item.ID, &item.To, &item.Subject, &item.Body, &item.Created)
	return item, err
}

func (m *Module) ProcessItem(ctx context.Context, item message) error {
	slog.Info("sending email", "id", item.ID, "to", item.To, "subject", item.Subject)
	sender := *m.sender.Load()
	return sender(ctx, item.To, item.Subject, []byte(item.Body))
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

type message struct {
	ID      int64
	To      string
	Subject string
	Body    string
	Created int64
}

func (m *message) String() string { return fmt.Sprintf("id=%d", m.ID) }
