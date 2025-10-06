package peering

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApiIntegration(t *testing.T) {
	// Set up a fake conway instance that just runs the API
	db := db.NewTest(t)
	iss := engine.NewTokenIssuer(filepath.Join(t.TempDir(), "test.pem"))
	a := New(db, iss)
	router := engine.NewRouter(nil)
	a.AttachRoutes(router)
	svr := httptest.NewServer(router)
	defer svr.Close()

	c := NewClient(svr.URL, t.TempDir(), iss)

	// Get initial state
	state := c.GetState()
	require.Nil(t, state)

	// Get initial sync'd state
	require.NoError(t, c.WarmCache())
	state = c.GetState()
	require.NotNil(t, state)
	assert.Equal(t, 0, len(state.EnabledFobs))

	// Seed some data into the server
	_, err := db.Exec("INSERT INTO members (name, email, confirmed, waiver, non_billable) VALUES ('test', 'foo@bar.com', TRUE, 1, TRUE)")
	require.NoError(t, err)
	_, err = db.Exec("UPDATE members SET fob_id = 123")
	require.NoError(t, err)

	// Confirm that the member is active
	var status string
	err = db.QueryRow("SELECT access_status FROM members").Scan(&status)
	require.NoError(t, err)
	require.Equal(t, "Ready", status)

	// One fob should be visible now
	require.NoError(t, c.WarmCache())
	require.NoError(t, c.WarmCache())
	state = c.GetState()
	require.NotNil(t, state)
	assert.Equal(t, []int64{123}, state.EnabledFobs)

	// Buffer some invalid events
	c.BufferEvent(&Event{})
	c.BufferEvent(&Event{
		UID:       uuid.NewString(),
		Timestamp: time.Now().Unix(),
	})
	c.BufferEvent(&Event{
		Timestamp: time.Now().Unix(),
		FobSwipe:  &FobSwipeEvent{FobID: 1},
	})
	c.BufferEvent(&Event{
		UID:      uuid.NewString(),
		FobSwipe: &FobSwipeEvent{FobID: 2},
	})

	// Buffer a couple of valid events to disc
	c.BufferEvent(&Event{
		UID:       uuid.NewString(),
		Timestamp: time.Now().Unix(),
		FobSwipe:  &FobSwipeEvent{FobID: 101},
	})
	c.BufferEvent(&Event{
		UID:          uuid.NewString(),
		Timestamp:    time.Now().Unix(),
		PrinterEvent: &PrinterEvent{PrinterName: "test"},
	})
	valid102 := &Event{
		UID:       uuid.NewString(),
		Timestamp: time.Now().Unix(),
		FobSwipe:  &FobSwipeEvent{FobID: 102},
	}
	c.BufferEvent(valid102)
	c.BufferEvent(valid102)

	// Flush out the events and confirm that they were processed correctly
	require.NoError(t, c.FlushEvents())
	require.NoError(t, c.FlushEvents())

	var rows int
	err = db.QueryRow("SELECT COUNT(*) FROM fob_swipes").Scan(&rows)
	require.NoError(t, err)
	assert.Equal(t, 4, rows)

	err = db.QueryRow("SELECT COUNT(*) FROM printer_events").Scan(&rows)
	require.NoError(t, err)
	assert.Equal(t, 1, rows)
}

func TestEventHookIntegration(t *testing.T) {
	// Set up a fake conway instance that just runs the API
	db := db.NewTest(t)
	iss := engine.NewTokenIssuer(filepath.Join(t.TempDir(), "test.pem"))
	a := New(db, iss)
	router := engine.NewRouter(nil)
	a.AttachRoutes(router)
	svr := httptest.NewServer(router)
	defer svr.Close()

	c := NewClient(svr.URL, t.TempDir(), iss)

	c.RegisterEventHook(func() []*Event {
		return []*Event{
			{
				UID: "foo",
				PrinterEvent: &PrinterEvent{
					PrinterName: "test-printer",
					ErrorCode:   "test-error",
				},
			},
			{
				UID: "bar",
				PrinterEvent: &PrinterEvent{
					PrinterName: "test-printer",
					ErrorCode:   "test-error",
				},
			},
			{
				UID: "baz",
				PrinterEvent: &PrinterEvent{
					PrinterName: "test-printer",
					ErrorCode:   "test-error-2",
				},
			},
		}
	})
	require.NoError(t, c.FlushEvents())
	require.NoError(t, c.FlushEvents())

	var rows int
	err := db.QueryRow("SELECT COUNT(*) FROM printer_events").Scan(&rows)
	require.NoError(t, err)
	assert.Equal(t, 2, rows)
}
