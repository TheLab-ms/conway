package fobapi

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const migration = `
CREATE TABLE members (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    fob_id INTEGER
) STRICT;

CREATE VIEW active_keyfobs AS SELECT fob_id FROM members;

INSERT INTO members (fob_id) VALUES (123);
INSERT INTO members (fob_id) VALUES (234);

CREATE TABLE fob_swipes (
    uid TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,
    fob_id INTEGER NOT NULL,
    member INTEGER
) STRICT;

CREATE UNIQUE INDEX fob_swipes_uniq ON fob_swipes (fob_id, timestamp);
`

func TestListing(t *testing.T) {
	db := engine.OpenTestDB(t)
	_, err := db.Exec(migration)
	require.NoError(t, err)

	m := New(db, nil)
	const etag = "3ac3b3f37064c09f3be2a0b733d93964ef41657dcabd00029149920e1d3939c4"

	// Happy path
	r := httptest.NewRequest("GET", "/", bytes.NewBufferString("[]"))
	w := httptest.NewRecorder()
	m.handle(w, r)
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "[123,234]\n", w.Body.String())
	assert.Equal(t, etag, w.Header().Get("ETag"))

	// ETag hit
	r = httptest.NewRequest("GET", "/", bytes.NewBufferString("[]"))
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	m.handle(w, r)
	assert.Equal(t, 304, w.Code)
	assert.Empty(t, w.Body.String())
	assert.Empty(t, w.Header().Get("ETag"))
}

func TestEvents(t *testing.T) {
	db := engine.OpenTestDB(t)
	_, err := db.Exec(migration)
	require.NoError(t, err)

	m := New(db, nil)

	r := httptest.NewRequest("GET", "/", bytes.NewBufferString(`[{ "fob": 123, "ts": 1000 }, { "fob": 123, "ts": 1000 }, { "fob": 123, "ts": 1001 }, { "fob": 345, "ts": 10001 }]`))
	w := httptest.NewRecorder()
	m.handle(w, r)
	assert.Equal(t, 200, w.Code)

	results, err := db.Query("SELECT timestamp, fob_id, member FROM fob_swipes")
	require.NoError(t, err)

	resultStrings := []string{}
	for results.Next() {
		var ts, fob, member int64
		results.Scan(&ts, &fob, &member)
		require.NotZero(t, ts)
		resultStrings = append(resultStrings, fmt.Sprintf("%d for member %d", fob, member))
	}
	require.NoError(t, results.Err())

	assert.Equal(t, []string{
		"123 for member 1",
		"345 for member 0",
	}, resultStrings)
}
