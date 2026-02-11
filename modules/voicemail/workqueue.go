package voicemail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

const maxRecordingSize = 10 * 1024 * 1024 // 10MB

type recordingItem struct {
	ID           int64
	RecordingSID string
	RecordingURL string
	CallSID      string
	FromNumber   string
	Duration     int
}

func (r *recordingItem) String() string {
	return fmt.Sprintf("recording=%s call=%s from=%s", r.RecordingSID, r.CallSID, r.FromNumber)
}

// GetItem returns the next pending recording to download.
func (m *Module) GetItem(ctx context.Context) (*recordingItem, error) {
	item := &recordingItem{}
	err := m.db.QueryRowContext(ctx,
		`SELECT id, recording_sid, recording_url, call_sid, from_number, recording_duration
		 FROM voicemail_recordings
		 WHERE status = 'pending'
		 ORDER BY id ASC LIMIT 1`).Scan(
		&item.ID, &item.RecordingSID, &item.RecordingURL, &item.CallSID, &item.FromNumber, &item.Duration)
	return item, err
}

// ProcessItem downloads a recording from Twilio, stores it locally, deletes it
// from Twilio, and queues a Discord notification.
func (m *Module) ProcessItem(ctx context.Context, item *recordingItem) error {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if cfg.accountSID == "" || cfg.authToken == "" {
		return fmt.Errorf("twilio credentials not configured")
	}

	// Download recording from Twilio
	audioData, err := m.downloadRecording(ctx, cfg, item.RecordingURL)
	if err != nil {
		return fmt.Errorf("downloading recording: %w", err)
	}

	// Store in database
	_, err = m.db.ExecContext(ctx,
		`UPDATE voicemail_recordings SET recording_data = $1, status = 'downloaded' WHERE id = $2`,
		audioData, item.ID)
	if err != nil {
		return fmt.Errorf("storing recording: %w", err)
	}

	// Delete from Twilio (best-effort -- recording is safely stored)
	if err := m.deleteRecordingFromTwilio(ctx, cfg, item.RecordingSID); err != nil {
		slog.Error("failed to delete recording from twilio", "error", err, "recordingSid", item.RecordingSID)
		m.eventLogger.LogEvent(ctx, 0, "TwilioDeleteError", item.RecordingSID, item.FromNumber, false, err.Error())
	}

	// Queue Discord notification
	if cfg.leadershipWebhookURL != "" {
		playbackURL := fmt.Sprintf("%s/voicemail/recordings/%d", m.self.String(), item.ID)
		payload, _ := json.Marshal(map[string]string{
			"content":  fmt.Sprintf("New voicemail from %s (%d seconds)\nListen: %s", item.FromNumber, item.Duration, playbackURL),
			"username": "Conway Voicemail",
		})
		_, err := m.db.ExecContext(ctx,
			"INSERT INTO discord_webhook_queue (webhook_url, payload) VALUES ($1, $2)",
			cfg.leadershipWebhookURL, string(payload))
		if err != nil {
			slog.Error("failed to queue discord notification", "error", err)
		}
	}

	m.eventLogger.LogEvent(ctx, 0, "RecordingDownloaded", item.RecordingSID, item.FromNumber, true,
		fmt.Sprintf("callSid=%s duration=%ds size=%d", item.CallSID, item.Duration, len(audioData)))

	return nil
}

// UpdateItem handles success/failure of recording processing.
func (m *Module) UpdateItem(ctx context.Context, item *recordingItem, success bool) error {
	// On success, status was already set to 'downloaded' in ProcessItem.
	// On failure, leave as 'pending' for retry on next poll.
	return nil
}

// downloadRecording fetches the recording audio from Twilio as MP3.
func (m *Module) downloadRecording(ctx context.Context, cfg *twilioConfig, recordingURL string) ([]byte, error) {
	// Twilio RecordingUrl is a relative path like /2010-04-01/Accounts/AC.../Recordings/RE...
	url := "https://api.twilio.com" + recordingURL + ".mp3"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(cfg.accountSID, cfg.authToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("twilio returned %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxRecordingSize))
}

// deleteRecordingFromTwilio removes the recording from Twilio's storage.
func (m *Module) deleteRecordingFromTwilio(ctx context.Context, cfg *twilioConfig, recordingSID string) error {
	url := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Recordings/%s.json",
		cfg.accountSID, recordingSID)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(cfg.accountSID, cfg.authToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("twilio delete returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
