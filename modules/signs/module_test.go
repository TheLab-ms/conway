package signs

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/stretchr/testify/require"
)

// fakePrinter captures Print calls for assertion in tests.
type fakePrinter struct {
	mu   sync.Mutex
	jobs []PrintJob
	err  error
}

func (f *fakePrinter) Print(ctx context.Context, job PrintJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Copy PDF bytes so callers can mutate the original safely.
	cp := PrintJob{JobName: job.JobName, PDF: append([]byte(nil), job.PDF...)}
	f.jobs = append(f.jobs, cp)
	return f.err
}

func (f *fakePrinter) Jobs() []PrintJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]PrintJob, len(f.jobs))
	copy(out, f.jobs)
	return out
}

// newTestModule returns a fresh in-memory module wired to the given printer.
func newTestModule(t *testing.T, printer Printer) (*Module, *engine.EventLogger) {
	t.Helper()
	db := engine.OpenTestDB(t)
	logger := engine.NewEventLogger(db, "signs")
	m := New(db, logger)
	if printer != nil {
		m.SetPrinter(printer)
	}
	return m, logger
}

// queueRow inserts a row directly into signs_print_queue.
func queueRow(t *testing.T, m *Module, slug, machine, issue, discord string) {
	t.Helper()
	_, err := m.db.Exec(`
		INSERT INTO signs_print_queue
		    (member_id, discord_username, template_slug, machine_name, issue, fields_json)
		VALUES (NULL, ?, ?, ?, ?, '{}')`,
		discord, slug, machine, issue)
	require.NoError(t, err)
}

func queueCount(t *testing.T, m *Module) int {
	t.Helper()
	var n int
	require.NoError(t, m.db.QueryRow("SELECT COUNT(*) FROM signs_print_queue").Scan(&n))
	return n
}

// eventCount returns the number of module_events rows matching event_type.
func eventCount(t *testing.T, m *Module, eventType string) int {
	t.Helper()
	var n int
	require.NoError(t, m.db.QueryRow(
		`SELECT COUNT(*) FROM module_events WHERE module = 'signs' AND event_type = ?`,
		eventType).Scan(&n))
	return n
}

func TestModule_ProcessOne_PrintsQueuedJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	printer := &fakePrinter{}
	m, _ := newTestModule(t, printer)

	queueRow(t, m, "maintenance", "Bambu X1C #2", "Nozzle clogged. Do not use.", "alice")

	processed := m.ProcessOne(ctx)
	require.True(t, processed, "expected ProcessOne to return true when an item was processed")

	jobs := printer.Jobs()
	require.Len(t, jobs, 1)
	require.NotEmpty(t, jobs[0].PDF)
	require.True(t, bytes.HasPrefix(jobs[0].PDF, []byte("%PDF-")), "expected PDF bytes")
	require.Contains(t, jobs[0].JobName, "Bambu X1C #2")

	require.Equal(t, 0, queueCount(t, m), "row should be deleted after successful print")
	require.Equal(t, 1, eventCount(t, m, "Printed"), "Printed event should be logged")
}

func TestModule_ProcessOne_NoopWhenQueueEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	printer := &fakePrinter{}
	m, _ := newTestModule(t, printer)

	processed := m.ProcessOne(ctx)
	require.False(t, processed, "expected ProcessOne to return false on empty queue")
	require.Empty(t, printer.Jobs())
}

func TestModule_ProcessOne_PrinterError_RetainsItem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	printer := &fakePrinter{err: context.DeadlineExceeded}
	m, _ := newTestModule(t, printer)

	// Insert with a known created/send_at so we can verify backoff advanced send_at.
	base := time.Now().Unix() - 30
	_, err := m.db.Exec(`
		INSERT INTO signs_print_queue
		    (created, send_at, member_id, discord_username, template_slug, machine_name, issue, fields_json)
		VALUES (?, ?, NULL, ?, ?, ?, ?, '{}')`,
		base, base+5, "alice", "maintenance", "Drill", "Broken")
	require.NoError(t, err)

	processed := m.ProcessOne(ctx)
	require.True(t, processed, "ProcessOne returns true when work was attempted")
	require.Len(t, printer.Jobs(), 1, "printer should have been called once")

	require.Equal(t, 1, queueCount(t, m), "failed item should remain in queue")

	var newSendAt, attempts int64
	require.NoError(t, m.db.QueryRow("SELECT send_at, attempts FROM signs_print_queue").Scan(&newSendAt, &attempts))
	require.Greater(t, newSendAt, base+5, "send_at should be advanced (backoff) after failure")
	require.Equal(t, int64(1), attempts, "attempts column should be incremented")
	require.Equal(t, 1, eventCount(t, m, "PrintError"), "PrintError event should be logged")
}

func TestModule_ProcessOne_BackoffDoubles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	printer := &fakePrinter{err: context.DeadlineExceeded}
	m, _ := newTestModule(t, printer)

	// Seed an item that's ready immediately (created recently so it's
	// well within the TTL).
	now := time.Now().Unix()
	_, err := m.db.Exec(`
		INSERT INTO signs_print_queue
		    (created, send_at, member_id, discord_username, template_slug, machine_name, issue, fields_json)
		VALUES (?, ?, NULL, ?, ?, ?, ?, '{}')`,
		now-1, now-1, "alice", "maintenance", "Drill", "Broken")
	require.NoError(t, err)

	// First failure → attempts=1, send_at = now + 2.
	require.True(t, m.ProcessOne(ctx))
	var attempts1, send1 int64
	require.NoError(t, m.db.QueryRow("SELECT attempts, send_at FROM signs_print_queue").Scan(&attempts1, &send1))
	require.EqualValues(t, 1, attempts1)

	// Make it eligible again and process; backoff should double (4s).
	_, err = m.db.Exec("UPDATE signs_print_queue SET send_at = unixepoch() - 1")
	require.NoError(t, err)
	require.True(t, m.ProcessOne(ctx))
	var attempts2, send2 int64
	require.NoError(t, m.db.QueryRow("SELECT attempts, send_at FROM signs_print_queue").Scan(&attempts2, &send2))
	require.EqualValues(t, 2, attempts2)
	// 2^2 = 4 seconds
	require.GreaterOrEqual(t, send2-time.Now().Unix(), int64(3), "backoff should grow to at least ~4s")
}

func TestModule_ProcessOne_UnknownTemplateLogsRenderError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	printer := &fakePrinter{}
	m, _ := newTestModule(t, printer)
	// Replace any seeded templates with empty so the slug is unknown.
	m.snapMu.Lock()
	m.templates = nil
	m.snapMu.Unlock()

	queueRow(t, m, "no-such-template", "Drill", "Broken", "alice")

	processed := m.ProcessOne(ctx)
	require.True(t, processed, "ProcessOne handles the row even though template is missing")
	require.Empty(t, printer.Jobs(), "printer should not be invoked when template is unknown")
	require.Equal(t, 1, eventCount(t, m, "RenderError"), "RenderError event should be logged")
	// We currently still leave the row for retry/cleanup; that's OK since
	// the picker prevents new rows for unknown slugs.
	require.Equal(t, 1, queueCount(t, m))
}

func TestModule_RenderError_IsSentinel(t *testing.T) {
	t.Parallel()
	err := &RenderError{Err: context.DeadlineExceeded}
	var re *RenderError
	require.ErrorAs(t, err, &re)
}
