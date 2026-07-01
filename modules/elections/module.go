// Package elections provides invite-only member elections and vote logging.
package elections

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"database/sql"
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
	question TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'open', 'closed')),
	max_choices INTEGER NOT NULL DEFAULT 1 CHECK (max_choices >= 1)
) STRICT;

CREATE INDEX IF NOT EXISTS elections_created_idx ON elections (created DESC);
CREATE INDEX IF NOT EXISTS elections_status_idx ON elections (status);

CREATE TABLE IF NOT EXISTS election_options (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	election_id TEXT NOT NULL REFERENCES elections(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	label TEXT NOT NULL,
	UNIQUE (election_id, position)
) STRICT;

CREATE INDEX IF NOT EXISTS election_options_election_idx ON election_options (election_id, position);

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
	return &Module{db: db, self: self}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /admin/config/elections/new", router.WithLeadership(m.handleAdminNew))
	router.HandleFunc("POST /admin/config/elections/new", router.WithLeadership(m.handleAdminCreate))
	router.HandleFunc("GET /admin/config/elections/{id}", router.WithLeadership(m.handleAdminDetail))
	router.HandleFunc("POST /admin/config/elections/{id}", router.WithLeadership(m.handleAdminUpdate))
	router.HandleFunc("POST /admin/config/elections/{id}/open", router.WithLeadership(m.handleAdminOpen))
	router.HandleFunc("POST /admin/config/elections/{id}/close", router.WithLeadership(m.handleAdminClose))
	router.HandleFunc("GET /admin/config/elections/{id}/results", router.WithLeadership(m.handleAdminResults))
	router.HandleFunc("GET /admin/config/elections/{id}/votes", router.WithLeadership(m.handleAdminVotes))

	router.HandleFunc("GET /elections/{id}", router.WithAuthn(m.handleMemberBallot))
	router.HandleFunc("POST /elections/{id}/vote", router.WithAuthn(m.handleMemberVote))
}
