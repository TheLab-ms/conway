package email

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/db"
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
	db     *sql.DB
	Sender Sender
}

func New(d *sql.DB, es Sender) *Module {
	db.MustMigrate(d, migration)
	m := &Module{db: d, Sender: es}
	if m.Sender == nil {
		m.Sender = newNoopSender()
	}
	return m
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, maxRPS))))
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

type message struct {
	ID      int64
	To      string
	Subject string
	Body    string
	Created int64
}

func (m *message) String() string { return fmt.Sprintf("id=%d", m.ID) }
