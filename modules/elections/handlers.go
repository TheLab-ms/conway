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
	"modernc.org/sqlite"
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
	e := &election{Status: statusDraft, Questions: []*question{{Position: 1, MaxChoices: 1, Options: []*option{{Position: 1}, {Position: 2}}}}}
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

func (m *Module) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := m.db.ExecContext(r.Context(), "DELETE FROM elections WHERE id = $1", id)
	if engine.HandleError(w, err) {
		return
	}
	n, err := res.RowsAffected()
	if engine.HandleError(w, err) {
		return
	}
	if n == 0 {
		engine.ClientError(w, "Not Found", "Election not found", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/admin/config/elections", http.StatusSeeOther)
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
	if status == statusOpen && len(e.Questions) == 0 {
		engine.ClientError(w, "Needs Questions", "Add at least one ballot question before opening.", http.StatusBadRequest)
		return
	}
	if status == statusOpen {
		for _, q := range e.Questions {
			if len(q.Options) < 2 {
				engine.ClientError(w, "Needs Options", "Add at least two ballot options to every question before opening.", http.StatusBadRequest)
				return
			}
		}
	}
	if status == statusOpen && e.Status == statusClosed {
		engine.ClientError(w, "Election Closed", "Closed elections cannot be reopened.", http.StatusBadRequest)
		return
	}
	if status == statusClosed && e.Status == statusDraft {
		engine.ClientError(w, "Election Draft", "Draft elections cannot be closed.", http.StatusBadRequest)
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
	current, err := m.currentSelections(r.Context(), e.ID, meta.ID)
	if engine.HandleError(w, err) {
		return
	}
	if len(current) > 0 {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadRequest)
		renderMemberBallotWithError(e, current, "Your vote has already been submitted and cannot be changed.").Render(r.Context(), w)
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
		if isDuplicateVote(err) {
			current, currentErr := m.currentSelections(r.Context(), e.ID, meta.ID)
			if engine.HandleError(w, currentErr) {
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			renderMemberBallotWithError(e, current, "Your vote has already been submitted and cannot be changed.").Render(r.Context(), w)
			return
		}
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
		Status:      statusDraft,
	}
	questionIndexes := r.Form["question_index"]
	questionTexts := r.Form["question_text"]
	questionMaxes := r.Form["question_max_choices"]
	for i, rawIndex := range questionIndexes {
		text := ""
		if i < len(questionTexts) {
			text = questionTexts[i]
		}
		q := &question{Position: len(e.Questions) + 1, Text: strings.TrimSpace(text), MaxChoices: 1}
		if i < len(questionMaxes) {
			max := strings.TrimSpace(questionMaxes[i])
			if max != "" {
				n, err := strconv.Atoi(max)
				if err != nil || n < 1 {
					return e, fmt.Errorf("Max choices must be a positive number.")
				}
				q.MaxChoices = n
			}
		}
		for _, label := range r.Form["option_label_"+rawIndex] {
			label = strings.TrimSpace(label)
			if label == "" {
				continue
			}
			q.Options = append(q.Options, &option{Position: len(q.Options) + 1, Label: label})
		}
		e.Questions = append(e.Questions, q)
	}
	if len(e.Questions) == 0 && strings.TrimSpace(r.FormValue("question")) != "" {
		q := &question{Position: 1, Text: strings.TrimSpace(r.FormValue("question")), MaxChoices: 1}
		if max := strings.TrimSpace(r.FormValue("max_choices")); max != "" {
			n, err := strconv.Atoi(max)
			if err != nil || n < 1 {
				return e, fmt.Errorf("Max choices must be a positive number.")
			}
			q.MaxChoices = n
		}
		for _, label := range r.Form["option_label"] {
			label = strings.TrimSpace(label)
			if label == "" {
				continue
			}
			q.Options = append(q.Options, &option{Position: len(q.Options) + 1, Label: label})
		}
		e.Questions = append(e.Questions, q)
	}
	if e.Title == "" {
		return e, fmt.Errorf("Title is required.")
	}
	if len(e.Questions) == 0 {
		return e, fmt.Errorf("Add at least one ballot question.")
	}
	for _, q := range e.Questions {
		if q.Text == "" {
			return e, fmt.Errorf("Question is required.")
		}
		if len(q.Options) < 2 {
			return e, fmt.Errorf("Add at least two ballot options to every question.")
		}
		if q.MaxChoices > len(q.Options) {
			return e, fmt.Errorf("Max choices cannot be greater than the number of options.")
		}
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
		SELECT e.id, e.created, e.updated, e.created_by, e.title, e.description, e.status,
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
		SELECT e.id, e.created, e.updated, e.created_by, e.title, e.description, e.status,
			COUNT(DISTINCT v.member_id), COUNT(DISTINCT l.id)
		FROM elections e
		LEFT JOIN election_votes v ON v.election_id = e.id
		LEFT JOIN election_vote_log l ON l.election_id = e.id
		WHERE e.id = $1
		GROUP BY e.id`, id))
	if err != nil {
		return nil, err
	}
	e.Questions, err = m.getQuestions(ctx, id)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (m *Module) getQuestions(ctx context.Context, electionID string) ([]*question, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT id, position, question, max_choices FROM election_questions WHERE election_id = $1 ORDER BY position", electionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*question
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, q := range out {
		options, err := m.getOptions(ctx, q.ID)
		if err != nil {
			return nil, err
		}
		q.Options = options
	}
	return out, nil
}

func (m *Module) getOptions(ctx context.Context, questionID int64) ([]*option, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT id, question_id, position, label FROM election_options WHERE question_id = $1 ORDER BY position", questionID)
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
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, e.ID, e.CreatedBy, e.Title, e.Description, firstQuestionText(e), e.Status, firstMaxChoices(e))
	if err != nil {
		return err
	}
	if err := replaceQuestions(ctx, tx, e); err != nil {
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
		max_choices = $4, updated = strftime('%s', 'now') WHERE id = $5 AND status = 'draft'`, e.Title, e.Description, firstQuestionText(e), firstMaxChoices(e), e.ID)
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
	if err := replaceQuestions(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceQuestions(ctx context.Context, tx *sql.Tx, e *election) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM election_questions WHERE election_id = $1", e.ID); err != nil {
		return err
	}
	for i, q := range e.Questions {
		res, err := tx.ExecContext(ctx, "INSERT INTO election_questions (election_id, position, question, max_choices) VALUES ($1, $2, $3, $4)", e.ID, i+1, q.Text, q.MaxChoices)
		if err != nil {
			return err
		}
		questionID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for j, opt := range q.Options {
			_, err := tx.ExecContext(ctx, "INSERT INTO election_options (election_id, question_id, position, label) VALUES ($1, $2, $3, $4)", e.ID, questionID, j+1, opt.Label)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func firstQuestionText(e *election) string {
	if len(e.Questions) == 0 {
		return ""
	}
	return e.Questions[0].Text
}

func firstMaxChoices(e *election) int {
	if len(e.Questions) == 0 {
		return 1
	}
	return e.Questions[0].MaxChoices
}

func parseSelections(r *http.Request, e *election) ([]int64, error) {
	_ = r.ParseForm()
	seen := map[int64]bool{}
	var selected []int64
	for _, q := range e.Questions {
		allowed := map[int64]bool{}
		for _, opt := range q.Options {
			allowed[opt.ID] = true
		}
		var questionSelected []int64
		for _, raw := range r.Form[questionFieldName(q)] {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || !allowed[id] {
				return selected, fmt.Errorf("Choose a valid ballot option.")
			}
			if !seen[id] {
				questionSelected = append(questionSelected, id)
				selected = append(selected, id)
				seen[id] = true
			}
		}
		if len(questionSelected) == 0 {
			return selected, fmt.Errorf("Choose at least one option for every question.")
		}
		if len(questionSelected) > q.MaxChoices {
			return selected, fmt.Errorf("Choose no more than %d options for %q.", q.MaxChoices, q.Text)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i] < selected[j] })
	return selected, nil
}

func questionFieldName(q *question) string {
	return "question_" + strconv.FormatInt(q.ID, 10)
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
		VALUES ($1, $2, $3, $4)`, e.ID, memberID, ballotJSON, ballotHash)
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

func isDuplicateVote(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && (sqliteErr.Code() == 1555 || sqliteErr.Code() == 2067)
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

func (m *Module) results(ctx context.Context, id string) (*election, []*questionResults, int, error) {
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
	out := make([]*questionResults, 0, len(e.Questions))
	for _, q := range e.Questions {
		qr := &questionResults{Question: q}
		for _, opt := range q.Options {
			pct := 0
			if total > 0 {
				pct = int(float64(counts[opt.ID]) / float64(total) * 100)
			}
			qr.Rows = append(qr.Rows, &resultRow{Option: opt, Count: counts[opt.ID], Pct: pct})
		}
		out = append(out, qr)
	}
	return e, out, total, nil
}

func (m *Module) voteLog(ctx context.Context, id string) (*election, []*voteLogEntry, error) {
	e, err := m.getElection(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	labels := map[int64]string{}
	for _, q := range e.Questions {
		for _, opt := range q.Options {
			labels[opt.ID] = q.Text + ": " + opt.Label
		}
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
