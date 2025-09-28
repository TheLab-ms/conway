package fobapi

import (
	"net/http/httptest"
	"testing"

	"github.com/TheLab-ms/conway/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const migration = `
CREATE TABLE IF NOT EXISTS members (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    fob_id INTEGER
) STRICT;

CREATE VIEW IF NOT EXISTS active_keyfobs AS SELECT fob_id FROM members;

INSERT INTO members (fob_id) VALUES (123);
INSERT INTO members (fob_id) VALUES (234);
`

func TestBasics(t *testing.T) {
	db := db.OpenTest(t)
	_, err := db.Exec(migration)
	require.NoError(t, err)

	m := New(db)
	const etag = "3ac3b3f37064c09f3be2a0b733d93964ef41657dcabd00029149920e1d3939c4"

	// Happy path
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	m.handleListFobs(w, r)
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "[123,234]\n", w.Body.String())
	assert.Equal(t, etag, w.Header().Get("ETag"))

	// ETag hit
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	m.handleListFobs(w, r)
	assert.Equal(t, 304, w.Code)
	assert.Empty(t, w.Body.String())
	assert.Empty(t, w.Header().Get("ETag"))
}
