package fobapi

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMigration = `
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

// newTestSigner builds an Ed25519Signer rooted in a per-test temp dir.
func newTestSigner(t *testing.T) *engine.Ed25519Signer {
	t.Helper()
	return engine.NewEd25519Signer(filepath.Join(t.TempDir(), "fob-signing.ed25519"))
}

func TestListing(t *testing.T) {
	db := engine.OpenTestDB(t)
	_, err := db.Exec(testMigration)
	require.NoError(t, err)

	signer := newTestSigner(t)
	m := New(db, nil, signer)
	const etag = "3ac3b3f37064c09f3be2a0b733d93964ef41657dcabd00029149920e1d3939c4"

	// Happy path
	r := httptest.NewRequest("GET", "/", bytes.NewBufferString("[]"))
	w := httptest.NewRecorder()
	m.handle(w, r)
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "[123,234]\n", w.Body.String())
	assert.Equal(t, etag, w.Header().Get("ETag"))

	// Signature header must be present and verify against the public key
	sigB64 := w.Header().Get(SignatureHeader)
	require.NotEmpty(t, sigB64, "expected %s header", SignatureHeader)
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	require.NoError(t, err)
	pub, err := base64.StdEncoding.DecodeString(signer.PublicKeyBase64())
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(ed25519.PublicKey(pub), w.Body.Bytes(), sig),
		"signature must verify against the advertised public key")

	// ETag hit
	r = httptest.NewRequest("GET", "/", bytes.NewBufferString("[]"))
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	m.handle(w, r)
	assert.Equal(t, 304, w.Code)
	assert.Empty(t, w.Body.String())
	assert.Empty(t, w.Header().Get("ETag"))
	assert.Empty(t, w.Header().Get(SignatureHeader), "304 has no body to sign")
}

func TestNoSigner(t *testing.T) {
	// Tests can pass a nil signer; the handler must not panic and must
	// simply omit the signature header.
	db := engine.OpenTestDB(t)
	_, err := db.Exec(testMigration)
	require.NoError(t, err)

	m := New(db, nil, nil)
	r := httptest.NewRequest("GET", "/", bytes.NewBufferString("[]"))
	w := httptest.NewRecorder()
	m.handle(w, r)
	assert.Equal(t, 200, w.Code)
	assert.Empty(t, w.Header().Get(SignatureHeader))
	assert.Empty(t, m.PublicKeyBase64())
}

func TestEvents(t *testing.T) {
	db := engine.OpenTestDB(t)
	_, err := db.Exec(testMigration)
	require.NoError(t, err)

	m := New(db, nil, newTestSigner(t))

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

