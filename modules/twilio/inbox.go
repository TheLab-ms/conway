package twilio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/admin"
)

// message is one row of the unified inbox.
type message struct {
	ID                   int64
	Created              time.Time
	Kind                 string
	From                 string
	To                   string
	Body                 string
	RecordingSid         string
	HasRecording         bool
	RecordingContentType string
	DurationSeconds      int
	ReadAt               *time.Time
	DownloadAttempts     int
}

// IsUnread returns true when no leadership user has marked this message read.
func (m *message) IsUnread() bool { return m.ReadAt == nil }

// CreatedRel returns a friendly relative time for templ rendering.
func (m *message) CreatedRel() string { return relTime(m.Created) }

// KindLabel returns a human label.
func (m *message) KindLabel() string {
	switch m.Kind {
	case kindSMS:
		return "SMS"
	case kindVoicemail:
		return "Voicemail"
	}
	return m.Kind
}

// Preview returns the first ~80 chars of the body for the list view.
func (m *message) Preview() string {
	const max = 80
	body := m.Body
	if body == "" {
		if m.Kind == kindVoicemail {
			if !m.HasRecording {
				return "(downloading recording…)"
			}
			return "(no transcription)"
		}
		return ""
	}
	if len(body) > max {
		return body[:max] + "…"
	}
	return body
}

// Duration returns a friendly mm:ss for voicemail rows.
func (m *message) Duration() string {
	if m.DurationSeconds <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%02d", m.DurationSeconds/60, m.DurationSeconds%60)
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

// handleInboxList renders the inbox.
func (m *Module) handleInboxList(w http.ResponseWriter, r *http.Request) {
	unreadOnly := r.URL.Query().Get("unread") == "1"

	q := `SELECT id, created, kind, from_number, to_number, body, recording_sid,
		(recording_data IS NOT NULL), recording_content_type, duration_seconds, read_at, download_attempts
		FROM twilio_messages`
	if unreadOnly {
		q += ` WHERE read_at IS NULL`
	}
	q += ` ORDER BY created DESC LIMIT 200`

	rows, err := m.db.QueryContext(r.Context(), q)
	if engine.HandleError(w, err) {
		return
	}
	defer rows.Close()

	var msgs []*message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if engine.HandleError(w, err) {
			return
		}
		msgs = append(msgs, msg)
	}

	var unread int
	m.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM twilio_messages WHERE read_at IS NULL").Scan(&unread)

	w.Header().Set("Content-Type", "text/html")
	renderInboxList(m.adminNav(), msgs, unread, unreadOnly).Render(r.Context(), w)
}

// handleInboxDetail shows a single message and auto-marks it read.
func (m *Module) handleInboxDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid Request", "bad id", 400)
		return
	}

	row := m.db.QueryRowContext(r.Context(), `
		SELECT id, created, kind, from_number, to_number, body, recording_sid,
			(recording_data IS NOT NULL), recording_content_type, duration_seconds, read_at, download_attempts
		FROM twilio_messages WHERE id = $1`, id)
	msg, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		engine.ClientError(w, "Not Found", "message not found", 404)
		return
	}
	if engine.HandleError(w, err) {
		return
	}

	// Auto-mark read on first view.
	if msg.IsUnread() {
		m.db.ExecContext(r.Context(),
			"UPDATE twilio_messages SET read_at = unixepoch() WHERE id = $1", id)
		now := time.Now()
		msg.ReadAt = &now
	}

	w.Header().Set("Content-Type", "text/html")
	renderInboxDetail(m.adminNav(), msg).Render(r.Context(), w)
}

// handleInboxToggleRead toggles the read state.
func (m *Module) handleInboxToggleRead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := m.db.ExecContext(r.Context(), `
		UPDATE twilio_messages
		SET read_at = CASE WHEN read_at IS NULL THEN unixepoch() ELSE NULL END
		WHERE id = $1`, id)
	if engine.HandleError(w, err) {
		return
	}
	// Redirect back to the inbox list rather than the detail page, since
	// the detail handler auto-marks read on view (which would immediately
	// undo a "mark unread" action).
	http.Redirect(w, r, "/admin/inbox", http.StatusSeeOther)
}

// handleInboxDelete permanently deletes a message and its recording.
func (m *Module) handleInboxDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := m.db.ExecContext(r.Context(),
		"DELETE FROM twilio_messages WHERE id = $1", id)
	if engine.HandleError(w, err) {
		return
	}
	http.Redirect(w, r, "/admin/inbox", http.StatusSeeOther)
}

// handleInboxAudio streams the stored voicemail recording.
func (m *Module) handleInboxAudio(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var data []byte
	var contentType string
	err := m.db.QueryRowContext(r.Context(),
		"SELECT recording_data, recording_content_type FROM twilio_messages WHERE id = $1", id).
		Scan(&data, &contentType)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if engine.HandleError(w, err) {
		return
	}
	if len(data) == 0 {
		http.Error(w, "recording not yet downloaded", http.StatusNotFound)
		return
	}
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Write(data)
}

// adminNav returns the admin navbar tabs (so the inbox renders the same nav
// as the rest of the admin UI). Returns nil before SetNavProvider is called,
// in which case the bootstrap layout simply omits the navbar.
func (m *Module) adminNav() []*admin.NavTab {
	if m.navProvider != nil {
		return m.navProvider()
	}
	return nil
}

// scanRow is the shared interface QueryRow / *Rows both satisfy via Scan.
type scanRow interface {
	Scan(...any) error
}

func scanMessage(s scanRow) (*message, error) {
	msg := &message{}
	var created int64
	var readAt sql.NullInt64
	var hasRec int
	if err := s.Scan(&msg.ID, &created, &msg.Kind, &msg.From, &msg.To, &msg.Body,
		&msg.RecordingSid, &hasRec, &msg.RecordingContentType,
		&msg.DurationSeconds, &readAt, &msg.DownloadAttempts); err != nil {
		return nil, err
	}
	msg.Created = time.Unix(created, 0)
	msg.HasRecording = hasRec == 1
	if readAt.Valid {
		t := time.Unix(readAt.Int64, 0)
		msg.ReadAt = &t
	}
	return msg, nil
}

// ---------------- Recording-download workqueue ----------------

// downloadJob is a row chosen by GetItem for the workqueue.
type downloadJob struct {
	ID           int64
	RecordingURL string
	Attempts     int
}

func (d downloadJob) String() string { return fmt.Sprintf("id=%d", d.ID) }

// GetItem finds the next voicemail row whose recording hasn't been downloaded
// yet, respecting the per-row backoff schedule.
func (m *Module) GetItem(ctx context.Context) (downloadJob, error) {
	var job downloadJob
	err := m.db.QueryRowContext(ctx, `
		SELECT id, recording_url, download_attempts
		FROM twilio_messages
		WHERE kind = 'voicemail'
			AND recording_url <> ''
			AND recording_data IS NULL
			AND unixepoch() >= download_next_at
		ORDER BY id ASC LIMIT 1`).Scan(&job.ID, &job.RecordingURL, &job.Attempts)
	return job, err
}

// ProcessItem downloads the recording bytes from Twilio.
func (m *Module) ProcessItem(ctx context.Context, job downloadJob) error {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.AccountSid == "" || cfg.AuthToken == "" {
		return fmt.Errorf("twilio credentials not configured")
	}

	// Twilio's recording URL serves WAV by default; appending .mp3 yields a
	// smaller file that browsers play natively.
	url := job.RecordingURL + ".mp3"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(cfg.AccountSid, cfg.AuthToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Twilio sometimes serves a 404 briefly while the recording is
		// being finalized — surface as a transient error so we back off.
		return fmt.Errorf("recording not yet available (404)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRecordingBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxRecordingBytes {
		return fmt.Errorf("recording exceeds %d bytes", maxRecordingBytes)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "audio/mpeg"
	}

	_, err = m.db.ExecContext(ctx, `
		UPDATE twilio_messages
		SET recording_data = $1, recording_content_type = $2
		WHERE id = $3`, body, contentType, job.ID)
	return err
}

// UpdateItem records success/failure and schedules retries with capped
// exponential backoff.
func (m *Module) UpdateItem(ctx context.Context, job downloadJob, success bool) error {
	if success {
		m.eventLogger.LogEvent(ctx, 0, "RecordingDownloaded",
			strconv.FormatInt(job.ID, 10), "", true,
			fmt.Sprintf("attempts=%d", job.Attempts+1))
		// Clear retry counters on success.
		_, err := m.db.ExecContext(ctx, `
			UPDATE twilio_messages
			SET download_attempts = 0, download_next_at = 0
			WHERE id = $1`, job.ID)
		return err
	}

	// Capped exponential backoff: 30s, 1m, 2m, 4m, …, max 1h.
	backoff := 30 * (1 << job.Attempts)
	if backoff > 3600 {
		backoff = 3600
	}
	// Give up after ~20 attempts; mark URL empty so we stop trying.
	if job.Attempts >= 20 {
		m.eventLogger.LogEvent(ctx, 0, "RecordingDownloadAbandoned",
			strconv.FormatInt(job.ID, 10), "", false,
			fmt.Sprintf("gave up after %d attempts", job.Attempts))
		_, err := m.db.ExecContext(ctx, `
			UPDATE twilio_messages
			SET recording_url = '', download_next_at = 0
			WHERE id = $1`, job.ID)
		return err
	}
	_, err := m.db.ExecContext(ctx, `
		UPDATE twilio_messages
		SET download_attempts = download_attempts + 1,
			download_next_at = unixepoch() + $1
		WHERE id = $2`, backoff, job.ID)
	return err
}
