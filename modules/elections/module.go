// Package elections provides invite-only member elections and vote logging.
package elections

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"database/sql"
	"fmt"
	"net/url"

	"github.com/TheLab-ms/conway/engine"
)

const migration = `
CREATE TABLE IF NOT EXISTS elections (
	id TEXT PRIMARY KEY,
	created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
	updated INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
	created_by INTEGER NOT NULL REFERENCES members(id),
	title TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	-- Retained for pre-multi-question databases; runtime ballots use election_questions.
	question TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'open', 'closed')),
	max_choices INTEGER NOT NULL DEFAULT 1 CHECK (max_choices >= 1)
) STRICT;

CREATE INDEX IF NOT EXISTS elections_created_idx ON elections (created DESC);
CREATE INDEX IF NOT EXISTS elections_status_idx ON elections (status);

CREATE TABLE IF NOT EXISTS election_questions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	election_id TEXT NOT NULL REFERENCES elections(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	question TEXT NOT NULL,
	max_choices INTEGER NOT NULL DEFAULT 1 CHECK (max_choices >= 1),
	UNIQUE (election_id, position)
) STRICT;

CREATE INDEX IF NOT EXISTS election_questions_election_idx ON election_questions (election_id, position);

CREATE TABLE IF NOT EXISTS election_options (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	election_id TEXT NOT NULL REFERENCES elections(id) ON DELETE CASCADE,
	question_id INTEGER REFERENCES election_questions(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	label TEXT NOT NULL,
	UNIQUE (question_id, position)
) STRICT;

CREATE TABLE IF NOT EXISTS election_votes (
	election_id TEXT NOT NULL REFERENCES elections(id) ON DELETE CASCADE,
	member_id INTEGER NOT NULL REFERENCES members(id),
	created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
	updated INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
	ballot_json TEXT NOT NULL,
	ballot_hash TEXT NOT NULL,
	PRIMARY KEY (election_id, member_id)
) STRICT;

CREATE INDEX IF NOT EXISTS election_votes_election_idx ON election_votes (election_id);
CREATE INDEX IF NOT EXISTS election_votes_member_idx ON election_votes (member_id);

CREATE TABLE IF NOT EXISTS election_vote_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
	election_id TEXT NOT NULL REFERENCES elections(id) ON DELETE CASCADE,
	member_id INTEGER NOT NULL REFERENCES members(id),
	ballot_json TEXT NOT NULL,
	ballot_hash TEXT NOT NULL,
	previous_hash TEXT NOT NULL DEFAULT '',
	log_hash TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS election_vote_log_election_idx ON election_vote_log (election_id, id);
CREATE INDEX IF NOT EXISTS election_vote_log_member_idx ON election_vote_log (member_id);
`

type Module struct {
	db   *sql.DB
	self *url.URL
}

func New(db *sql.DB, self *url.URL) *Module {
	engine.MustMigrate(db, migration)
	migrateMultiQuestionElections(db)
	return &Module{db: db, self: self}
}

func migrateMultiQuestionElections(db *sql.DB) {
	legacyOptions := false
	if !columnExists(db, "election_options", "question_id") {
		legacyOptions = true
		engine.MustMigrate(db, `
DROP INDEX IF EXISTS election_options_election_idx;
ALTER TABLE election_options RENAME TO election_options_old;

CREATE TABLE election_options (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	election_id TEXT NOT NULL REFERENCES elections(id) ON DELETE CASCADE,
	question_id INTEGER REFERENCES election_questions(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	label TEXT NOT NULL,
	UNIQUE (question_id, position)
) STRICT;
`)
	}

	engine.MustMigrate(db, `
INSERT INTO election_questions (election_id, position, question, max_choices)
SELECT e.id, 1, e.question, e.max_choices
FROM elections e
WHERE NOT EXISTS (SELECT 1 FROM election_questions q WHERE q.election_id = e.id);

CREATE INDEX IF NOT EXISTS election_options_election_idx ON election_options (election_id, question_id, position);
`)
	if legacyOptions {
		engine.MustMigrate(db, `
INSERT OR IGNORE INTO election_options (id, election_id, question_id, position, label)
SELECT o.id, o.election_id, q.id, o.position, o.label
FROM election_options_old o
JOIN election_questions q ON q.election_id = o.election_id AND q.position = 1;
`)
	}
}

func columnExists(db *sql.DB, table, column string) bool {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /admin/config/elections/new", router.WithLeadership(m.handleAdminNew))
	router.HandleFunc("POST /admin/config/elections/new", router.WithLeadership(m.handleAdminCreate))
	router.HandleFunc("GET /admin/config/elections/{id}", router.WithLeadership(m.handleAdminDetail))
	router.HandleFunc("POST /admin/config/elections/{id}", router.WithLeadership(m.handleAdminUpdate))
	router.HandleFunc("POST /admin/config/elections/{id}/delete", router.WithLeadership(m.handleAdminDelete))
	router.HandleFunc("POST /admin/config/elections/{id}/open", router.WithLeadership(m.handleAdminOpen))
	router.HandleFunc("POST /admin/config/elections/{id}/close", router.WithLeadership(m.handleAdminClose))
	router.HandleFunc("GET /admin/config/elections/{id}/results", router.WithLeadership(m.handleAdminResults))
	router.HandleFunc("GET /admin/config/elections/{id}/votes", router.WithLeadership(m.handleAdminVotes))

	router.HandleFunc("GET /elections/{id}", router.WithAuthn(m.handleMemberBallot))
	router.HandleFunc("POST /elections/{id}/vote", router.WithAuthn(m.handleMemberVote))
}
