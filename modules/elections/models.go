package elections

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	statusDraft  = "draft"
	statusOpen   = "open"
	statusClosed = "closed"
)

type election struct {
	ID          string
	Created     time.Time
	Updated     time.Time
	CreatedBy   int64
	Title       string
	Description string
	Status      string
	Questions   []*question
	VoteCount   int
	LogCount    int
}

type question struct {
	ID         int64
	Position   int
	Text       string
	MaxChoices int
	Options    []*option
}

type option struct {
	ID         int64  `json:"id"`
	QuestionID int64  `json:"question_id"`
	Position   int    `json:"position"`
	Label      string `json:"label"`
}

type resultRow struct {
	Option *option
	Count  int
	Pct    int
}

type questionResults struct {
	Question *question
	Rows     []*resultRow
}

type voteLogEntry struct {
	ID           int64
	Created      time.Time
	MemberID     int64
	MemberEmail  string
	Selections   string
	BallotHash   string
	PreviousHash string
	LogHash      string
}

type editView struct {
	Election     *election
	Action       string
	ShareURL     string
	ErrorMessage string
}

func statusBadge(status string) string {
	switch status {
	case statusDraft:
		return "secondary"
	case statusOpen:
		return "success"
	case statusClosed:
		return "dark"
	default:
		return "secondary"
	}
}

func statusLabel(status string) string {
	switch status {
	case statusDraft:
		return "Draft"
	case statusOpen:
		return "Open"
	case statusClosed:
		return "Closed"
	default:
		return status
	}
}

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("Jan 2, 2006")
}

func fromUnix(sec int64) time.Time { return time.Unix(sec, 0) }

type scanner interface{ Scan(...any) error }

func scanElection(s scanner) (*election, error) {
	e := &election{}
	var created, updated int64
	if err := s.Scan(&e.ID, &created, &updated, &e.CreatedBy, &e.Title, &e.Description, &e.Status, &e.VoteCount, &e.LogCount); err != nil {
		return nil, err
	}
	e.Created = fromUnix(created)
	e.Updated = fromUnix(updated)
	return e, nil
}

func scanQuestion(rows *sql.Rows) (*question, error) {
	q := &question{}
	if err := rows.Scan(&q.ID, &q.Position, &q.Text, &q.MaxChoices); err != nil {
		return nil, err
	}
	return q, nil
}

func scanOption(rows *sql.Rows) (*option, error) {
	o := &option{}
	if err := rows.Scan(&o.ID, &o.QuestionID, &o.Position, &o.Label); err != nil {
		return nil, err
	}
	return o, nil
}
