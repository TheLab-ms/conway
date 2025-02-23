package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/db"
	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/api"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApiIntegration(t *testing.T) {
	// Set up a fake conway instance that just runs the API
	db := db.NewTest(t)
	a, err := api.New(db)
	require.NoError(t, err)
	router := engine.NewRouter(nil)
	a.AttachRoutes(router)
	svr := httptest.NewServer(router)
	defer svr.Close()

	// Build the Glider config
	conf := &Config{
		ConwayURL: svr.URL,
		StateDir:  t.TempDir(),
	}
	err = db.QueryRow("SELECT token FROM api_tokens").Scan(&conf.ConwayToken)
	require.NoError(t, err)
	err = os.MkdirAll(filepath.Join(conf.StateDir, "events"), 0755)
	require.NoError(t, err)

	// Get initial state
	state, err := getState(conf, 0)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, 0, len(state.EnabledFobs))

	// Seed some data into the server
	_, err = db.Exec("INSERT INTO members (name, email, confirmed, waiver, non_billable) VALUES ('test', 'foo@bar.com', TRUE, 1, TRUE)")
	require.NoError(t, err)
	_, err = db.Exec("UPDATE members SET fob_id = 123")
	require.NoError(t, err)

	// Confirm that the member is active
	var status string
	err = db.QueryRow("SELECT access_status FROM members").Scan(&status)
	require.NoError(t, err)
	require.Equal(t, "Ready", status)

	// One fob should be visible now
	state, err = getState(conf, state.Revision)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, []int64{123}, state.EnabledFobs)

	// Empty response since we have the latest version
	state, err = getState(conf, state.Revision)
	require.NoError(t, err)
	assert.Nil(t, state)

	// Buffer some invalid events
	bufferEvent(conf, &api.GliderEvent{})
	bufferEvent(conf, &api.GliderEvent{
		UID:       uuid.NewString(),
		Timestamp: time.Now().Unix(),
	})
	bufferEvent(conf, &api.GliderEvent{
		Timestamp: time.Now().Unix(),
		FobSwipe:  &api.FobSwipeEvent{FobID: 1},
	})
	bufferEvent(conf, &api.GliderEvent{
		UID:      uuid.NewString(),
		FobSwipe: &api.FobSwipeEvent{FobID: 2},
	})

	// Buffer a couple of valid events to disc
	bufferEvent(conf, &api.GliderEvent{
		UID:       uuid.NewString(),
		Timestamp: time.Now().Unix(),
		FobSwipe:  &api.FobSwipeEvent{FobID: 101},
	})
	valid102 := &api.GliderEvent{
		UID:       uuid.NewString(),
		Timestamp: time.Now().Unix(),
		FobSwipe:  &api.FobSwipeEvent{FobID: 102},
	}
	bufferEvent(conf, valid102)
	bufferEvent(conf, valid102)

	// Flush out the events and confirm that they were processed correctly
	require.NoError(t, flushEvents(conf))
	require.NoError(t, flushEvents(conf))

	var rows int
	err = db.QueryRow("SELECT COUNT(*) FROM fob_swipes").Scan(&rows)
	require.NoError(t, err)
	assert.Equal(t, 4, rows)
}
