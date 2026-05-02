package e2e

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMachines_StopRequiresAuth verifies POST /machines/{serial}/stop without
// a session redirects to /login.
func TestMachines_StopRequiresAuth(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	req, err := http.NewRequest("POST", env.baseURL+"/machines/test-001/stop", nil)
	require.NoError(t, err)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "/login")
}

// TestMachines_StopFlipsFlag verifies an authenticated member can flip
// stop_requested for a printer.
func TestMachines_StopFlipsFlag(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "stopper@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, memberID)

	req, err := http.NewRequest("POST", env.baseURL+"/machines/test-001/stop", nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "token", Value: tok})

	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)

	var stopRequested int
	require.NoError(t, env.db.QueryRow(
		`SELECT stop_requested FROM bambu_printer_state WHERE serial_number = ?`, "test-001").Scan(&stopRequested))
	assert.Equal(t, 1, stopRequested)
}

// TestMachines_StreamRequiresAuth verifies the MJPEG stream endpoint requires
// authentication.
func TestMachines_StreamRequiresAuth(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp, err := noRedirectClient().Get(env.baseURL + "/machines/stream/test-001")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "/login")
}

// fakeFrameReader implements io.ReadCloser. It produces a few canned chunks
// and then blocks until closed.
type fakeFrameReader struct {
	chunks chan []byte
	pos    []byte
	closed chan struct{}
}

func newFakeFrameReader() *fakeFrameReader {
	r := &fakeFrameReader{
		chunks: make(chan []byte, 4),
		closed: make(chan struct{}),
	}
	r.chunks <- []byte("--frame\r\nContent-Type: image/jpeg\r\n\r\nfakejpegbytes\r\n")
	return r
}

func (r *fakeFrameReader) Read(p []byte) (int, error) {
	if len(r.pos) == 0 {
		select {
		case <-r.closed:
			return 0, io.EOF
		case chunk := <-r.chunks:
			r.pos = chunk
		}
	}
	n := copy(p, r.pos)
	r.pos = r.pos[n:]
	return n, nil
}

func (r *fakeFrameReader) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}

// TestMachines_StreamReturnsContent injects a synthetic stream source and
// verifies the handler returns 200 with a multipart content-type. We close the
// connection after observing the response headers so we don't read forever.
func TestMachines_StreamReturnsContent(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	require.NotNil(t, env.MachinesModule)

	env.MachinesModule.SetTestStream("test-001", func(ctx context.Context) (io.ReadCloser, error) {
		return newFakeFrameReader(), nil
	})

	memberID := seedMember(t, env, "streamer@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, memberID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", env.baseURL+"/machines/stream/test-001", nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "token", Value: tok})

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/"),
		"unexpected content type: %s", resp.Header.Get("Content-Type"))

	// Read a small amount to confirm bytes flow, then cancel.
	buf := make([]byte, 16)
	_, _ = resp.Body.Read(buf)
	cancel()
}

// TestMachines_StaleStateHidden verifies printers whose updated_at is older
// than 3x the poll interval are excluded from the rendered /machines page.
func TestMachines_StaleStateHidden(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "viewer@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, memberID)

	// Make test-001 very old (default poll interval is 5s, TTL is 15s; 1 hour ago is well past).
	_, err := env.db.Exec(
		`UPDATE bambu_printer_state SET updated_at = strftime('%s','now') - 3600 WHERE serial_number = ?`,
		"test-001")
	require.NoError(t, err)

	req, err := http.NewRequest("GET", env.baseURL+"/machines", nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "token", Value: tok})
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.NotContains(t, string(body), "Printer A", "stale Printer A should be hidden")
	// Fresh printers should still be visible.
	assert.Contains(t, string(body), "Printer B", "fresh Printer B should still render")
}
