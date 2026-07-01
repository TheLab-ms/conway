package elections

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/google/uuid"
)

func (m *Module) handleAdminList(w http.ResponseWriter, r *http.Request) {
	elections, err := m.listElections(r.Context())
	if engine.HandleError(w, err) {
		return
	}
	w.Header().Set("Content-Type", "text/html")
	renderAdminElectionList(elections).Render(r.Context(), w)
}

func (m *Module) handleAdminNew(w http.ResponseWriter, r *http.Request) {
	e := &election{Status: statusDraft, MaxChoices: 1, Options: []*option{{Position: 1}, {Position: 2}}}
	m.renderEdit(w, r, editView{Election: e, Action: "/admin/config/elections/new"})
}

func (m *Module) handleAdminCreate(w http.ResponseWriter, r *http.Request) {
	meta := auth.GetUserMeta(r.Context())
	e, err := parseElectionForm(r)
	if err != nil {
		e.Status = statusDraft
		m.renderEdit(w, r, editView{Election: e, Action: "/admin/config/elections/new", ErrorMessage: err.Error()})
		return
	}
	e.ID = uuid.NewString()
	e.CreatedBy = meta.ID
	e.Status = statusDraft
	if err := m.insertElection(r.Context(), e); err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	http.Redirect(w, r, adminElectionPath(e.ID), http.StatusSeeOther)
}

func (m *Module) handleAdminDetail(w http.ResponseWriter, r *http.Request) {
	e, err := m.getElection(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		engine.ClientError(w, "Not Found", "Election not found", http.StatusNotFound)
		return
	}
	if engine.HandleError(w, err) {
		return
	}
	m.renderEdit(w, r, editView{Election: e, Action: adminElectionPath(e.ID), ShareURL: m.shareURL(e.ID)})
}

func (m *Module) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := m.getElection(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		engine.ClientError(w, "Not Found", "Election not found", http.StatusNotFound)
		return
	}
	if engine.HandleError(w, err) {
		return
	}
	if existing.Status != statusDraft {
		engine.ClientError(w, "Election Locked", "Open or closed elections cannot be edited.", http.StatusBadRequest)
		return
	}
	e, err := parseElectionForm(r)
	e.ID = id
	e.Status = existing.Status
	if err != nil {
		m.renderEdit(w, r, editView{Election: e, Action: adminElectionPath(id), ShareURL: m.shareURL(id), ErrorMessage: err.Error()})
		return
	}
	if err := m.updateElection(r.Context(), e); err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	http.Redirect(w, r, adminElectionPath(id), http.StatusSeeOther)
}

func (m *Module) handleAdminOpen(w http.ResponseWriter, r *http.Request) {
	m.setElectionStatus(w, r, statusOpen)
}

func (m *Module) handleAdminClose(w http.ResponseWriter, r *http.Request) {
	m.setElectionStatus(w, r, statusClosed)
}

func (m *Module) setElectionStatus(w http.ResponseWriter, r *http.Request, status string) {
	id := r.PathValue("id")
	e, err := m.getElection(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		engine.ClientError(w, "Not Found", "Election not found", http.StatusNotFound)
		return
	}
	if engine.HandleError(w, err) {
		return
	}
	if status == statusOpen && len(e.Options) < 2 {
		engine.ClientError(w, "Needs Options", "Add at least two ballot options before opening.", http.StatusBadRequest)
		return
	}
	if status == statusOpen && e.Status == statusClosed {
		engine.ClientError(w, "Election Closed", "Closed elections cannot be reopened.", http.StatusBadRequest)
		return
	}
	_, err = m.db.ExecContext(r.Context(), "UPDATE elections SET status = $1, updated = strftime('%s', 'now') WHERE id = $2", status, id)
	if engine.HandleError(w, err) {
		return
	}
	http.Redirect(w, r, adminElectionPath(id), http.StatusSeeOther)
}

func (m *Module) handleAdminResults(w http.ResponseWriter, r *http.Request) {
	e, rows, total, err := m.results(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		engine.ClientError(w, "Not Found", "Election not found", http.StatusNotFound)
		return
	}
	if engine.HandleError(w, err) {
		return
	}
	w.Header().Set("Content-Type", "text/html")
	renderAdminElectionResults(e, rows, total).Render(r.Context(), w)
}

func (m *Module) handleAdminVotes(w http.ResponseWriter, r *http.Request) {
	e, entries, err := m.voteLog(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		engine.ClientError(w, "Not Found", "Election not found", http.StatusNotFound)
		return
	}
	if engine.HandleError(w, err) {
		return
	}
	w.Header().Set("Content-Type", "text/html")
	renderAdminElectionVotes(e, entries).Render(r.Context(), w)
}

func (m *Module) handleMemberBallot(w http.ResponseWriter, r *http.Request) {
	meta := auth.GetUserMeta(r.Context())
	if meta == nil || !meta.ActiveMember {
		engine.ClientError(w, "Members Only", "Only active members can vote in elections.", http.StatusForbidden)
		return
	}
	e, err := m.getElection(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && e.Status == statusDraft) {
		engine.ClientError(w, "Not Found", "Election not found", http.StatusNotFound)
		return
	}
	if engine.HandleError(w, err) {
		return
	}
	current, err := m.currentSelections(r.Context(), e.ID, meta.ID)
	if engine.HandleError(w, err) {
		return
	}
	w.Header().Set("Content-Type", "text/html")
	renderMemberBallot(e, current).Render(r.Context(), w)
}

func (m *Module) handleMemberVote(w http.ResponseWriter, r *http.Request) {
	meta := auth.GetUserMeta(r.Context())
	if meta == nil || !meta.ActiveMember {
		engine.ClientError(w, "Members Only", "Only active members can vote in elections.", http.StatusForbidden)
		return
	}
	e, err := m.getElection(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && e.Status == statusDraft) {
		engine.ClientError(w, "Not Found", "Election not found", http.StatusNotFound)
		return
	}
	if engine.HandleError(w, err) {
		return
	}
	if e.Status != statusOpen {
		engine.ClientError(w, "Voting Closed", "This election is not accepting votes.", http.StatusBadRequest)
		return
	}
	selected, err := parseSelections(r, e)
	if err != nil {
		current := map[int64]bool{}
		for _, id := range selected {
			current[id] = true
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadRequest)
		renderMemberBallotWithError(e, current, err.Error()).Render(r.Context(), w)
		return
	}
	if err := m.recordVote(r.Context(), e, meta.ID, selected); err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	http.Redirect(w, r, "/elections/"+e.ID+"?voted=1", http.StatusSeeOther)
}

func (m *Module) renderEdit(w http.ResponseWriter, r *http.Request, v editView) {
	w.Header().Set("Content-Type", "text/html")
	renderAdminElectionEdit(v).Render(r.Context(), w)
}

func parseElectionForm(r *http.Request) (*election, error) {
	_ = r.ParseForm()
	e := &election{
		Title:       strings.TrimSpace(r.FormValue("title")),
		Description: strings.TrimSpace(r.FormValue("description")),
		Question:    strings.TrimSpace(r.FormValue("question")),
		Status:      statusDraft,
		MaxChoices:  1,
	}
	if max := strings.TrimSpace(r.FormValue("max_choices")); max != "" {
		n, err := strconv.Atoi(max)
		if err != nil || n < 1 {
			return e, fmt.Errorf("Max choices must be a positive number.")
		}
		e.MaxChoices = n
	}
	labels := r.Form["option_label"]
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		e.Options = append(e.Options, &option{Position: len(e.Options) + 1, Label: label})
	}
	if e.Title == "" {
		return e, fmt.Errorf("Title is required.")
	}
	if e.Question == "" {
		return e, fmt.Errorf("Question is required.")
	}
	if len(e.Options) < 2 {
		return e, fmt.Errorf("Add at least two ballot options.")
	}
	if e.MaxChoices > len(e.Options) {
		return e, fmt.Errorf("Max choices cannot be greater than the number of options.")
	}
	return e, nil
}

func adminElectionPath(id string) string { return "/admin/config/elections/" + id }

func (m *Module) shareURL(id string) string {
	if m.self == nil {
		return "/elections/" + id
	}
	u := *m.self
	u.Path = "/elections/" + id
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (m *Module) listElections(ctx context.Context) ([]*election, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT e.id, e.created, e.updated, e.created_by, e.title, e.description, e.question, e.status, e.max_choices,
			COUNT(DISTINCT v.member_id), COUNT(DISTINCT l.id)
		FROM elections e
		LEFT JOIN election_votes v ON v.election_id = e.id
		LEFT JOIN election_vote_log l ON l.election_id = e.id
		GROUP BY e.id
		ORDER BY e.created DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*election
	for rows.Next() {
		e, err := scanElection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (m *Module) getElection(ctx context.Context, id string) (*election, error) {
	e, err := scanElection(m.db.QueryRowContext(ctx, `
		SELECT e.id, e.created, e.updated, e.created_by, e.title, e.description, e.question, e.status, e.max_choices,
			COUNT(DISTINCT v.member_id), COUNT(DISTINCT l.id)
		FROM elections e
		LEFT JOIN election_votes v ON v.election_id = e.id
		LEFT JOIN election_vote_log l ON l.election_id = e.id
		WHERE e.id = $1
		GROUP BY e.id`, id))
	if err != nil {
		return nil, err
	}
	e.Options, err = m.getOptions(ctx, id)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (m *Module) getOptions(ctx context.Context, electionID string) ([]*option, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT id, position, label FROM election_options WHERE election_id = $1 ORDER BY position", electionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*option
	for rows.Next() {
		o, err := scanOption(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (m *Module) insertElection(ctx context.Context, e *election) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO elections (id, created_by, title, description, question, status, max_choices)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, e.ID, e.CreatedBy, e.Title, e.Description, e.Question, e.Status, e.MaxChoices)
	if err != nil {
		return err
	}
	if err := replaceOptions(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Module) updateElection(ctx context.Context, e *election) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE elections SET title = $1, description = $2, question = $3,
		max_choices = $4, updated = strftime('%s', 'now') WHERE id = $5 AND status = 'draft'`, e.Title, e.Description, e.Question, e.MaxChoices, e.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("election is not editable")
	}
	if err := replaceOptions(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceOptions(ctx context.Context, tx *sql.Tx, e *election) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM election_options WHERE election_id = $1", e.ID); err != nil {
		return err
	}
	for i, opt := range e.Options {
		_, err := tx.ExecContext(ctx, "INSERT INTO election_options (election_id, position, label) VALUES ($1, $2, $3)", e.ID, i+1, opt.Label)
		if err != nil {
			return err
		}
	}
	return nil
}

func parseSelections(r *http.Request, e *election) ([]int64, error) {
	_ = r.ParseForm()
	allowed := map[int64]bool{}
	for _, opt := range e.Options {
		allowed[opt.ID] = true
	}
	seen := map[int64]bool{}
	var selected []int64
	for _, raw := range r.Form["option"] {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || !allowed[id] {
			return selected, fmt.Errorf("Choose a valid ballot option.")
		}
		if !seen[id] {
			selected = append(selected, id)
			seen[id] = true
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i] < selected[j] })
	if len(selected) == 0 {
		return selected, fmt.Errorf("Choose at least one option.")
	}
	if len(selected) > e.MaxChoices {
		return selected, fmt.Errorf("Choose no more than %d options.", e.MaxChoices)
	}
	return selected, nil
}

func (m *Module) currentSelections(ctx context.Context, electionID string, memberID int64) (map[int64]bool, error) {
	var ballot string
	err := m.db.QueryRowContext(ctx, "SELECT ballot_json FROM election_votes WHERE election_id = $1 AND member_id = $2", electionID, memberID).Scan(&ballot)
	if errors.Is(err, sql.ErrNoRows) {
		return map[int64]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids, err := decodeBallot(ballot)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (m *Module) recordVote(ctx context.Context, e *election, memberID int64, selected []int64) error {
	ballotJSON, ballotHash, err := encodeBallot(e.ID, memberID, selected)
	if err != nil {
		return err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO election_votes (election_id, member_id, ballot_json, ballot_hash)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(election_id, member_id) DO UPDATE SET
			updated = strftime('%s', 'now'), ballot_json = excluded.ballot_json, ballot_hash = excluded.ballot_hash`, e.ID, memberID, ballotJSON, ballotHash)
	if err != nil {
		return err
	}
	var previous string
	_ = tx.QueryRowContext(ctx, "SELECT log_hash FROM election_vote_log WHERE election_id = $1 ORDER BY id DESC LIMIT 1", e.ID).Scan(&previous)
	var now int64
	if err := tx.QueryRowContext(ctx, "SELECT strftime('%s', 'now')").Scan(&now); err != nil {
		return err
	}
	logHash := hashParts(previous, e.ID, strconv.FormatInt(memberID, 10), ballotHash, strconv.FormatInt(now, 10))
	_, err = tx.ExecContext(ctx, `INSERT INTO election_vote_log (created, election_id, member_id, ballot_json, ballot_hash, previous_hash, log_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, now, e.ID, memberID, ballotJSON, ballotHash, previous, logHash)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func encodeBallot(electionID string, memberID int64, selected []int64) (string, string, error) {
	ballot := struct {
		Selections []int64 `json:"selections"`
	}{Selections: selected}
	b, err := json.Marshal(ballot)
	if err != nil {
		return "", "", err
	}
	return string(b), hashParts(electionID, strconv.FormatInt(memberID, 10), string(b)), nil
}

func decodeBallot(ballotJSON string) ([]int64, error) {
	var ballot struct {
		Selections []int64 `json:"selections"`
	}
	if err := json.Unmarshal([]byte(ballotJSON), &ballot); err != nil {
		return nil, err
	}
	return ballot.Selections, nil
}

func hashParts(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{':'})
		h.Write([]byte(part))
		h.Write([]byte{'|'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (m *Module) results(ctx context.Context, id string) (*election, []*resultRow, int, error) {
	e, err := m.getElection(ctx, id)
	if err != nil {
		return nil, nil, 0, err
	}
	counts := map[int64]int{}
	var total int
	rows, err := m.db.QueryContext(ctx, "SELECT ballot_json FROM election_votes WHERE election_id = $1", id)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var ballot string
		if err := rows.Scan(&ballot); err != nil {
			return nil, nil, 0, err
		}
		ids, err := decodeBallot(ballot)
		if err != nil {
			return nil, nil, 0, err
		}
		total++
		for _, id := range ids {
			counts[id]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}
	out := make([]*resultRow, 0, len(e.Options))
	for _, opt := range e.Options {
		pct := 0
		if total > 0 {
			pct = int(float64(counts[opt.ID]) / float64(total) * 100)
		}
		out = append(out, &resultRow{Option: opt, Count: counts[opt.ID], Pct: pct})
	}
	return e, out, total, nil
}

func (m *Module) voteLog(ctx context.Context, id string) (*election, []*voteLogEntry, error) {
	e, err := m.getElection(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	labels := map[int64]string{}
	for _, opt := range e.Options {
		labels[opt.ID] = opt.Label
	}
	rows, err := m.db.QueryContext(ctx, `SELECT l.id, l.created, l.member_id, m.email, l.ballot_json, l.ballot_hash, l.previous_hash, l.log_hash
		FROM election_vote_log l
		JOIN members m ON m.id = l.member_id
		WHERE l.election_id = $1
		ORDER BY l.id DESC`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []*voteLogEntry
	for rows.Next() {
		entry := &voteLogEntry{}
		var created int64
		var ballot string
		if err := rows.Scan(&entry.ID, &created, &entry.MemberID, &entry.MemberEmail, &ballot, &entry.BallotHash, &entry.PreviousHash, &entry.LogHash); err != nil {
			return nil, nil, err
		}
		entry.Created = fromUnix(created)
		ids, err := decodeBallot(ballot)
		if err != nil {
			return nil, nil, err
		}
		var selected []string
		for _, id := range ids {
			selected = append(selected, labels[id])
		}
		entry.Selections = strings.Join(selected, ", ")
		out = append(out, entry)
	}
	return e, out, rows.Err()
}
