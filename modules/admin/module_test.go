package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/TheLab-ms/conway/db"
	"github.com/TheLab-ms/conway/engine"
	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExport(t *testing.T) {
	db := db.NewTest(t)
	m := &Module{db: db}

	_, err := db.Exec(`INSERT INTO members (name, email) VALUES (?, ?)`, "Test User", "test@example.com")
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO members (name, email) VALUES (?, ?)`, "Test User 2", "test-2@example.com")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	p := httprouter.Params{{Key: "table", Value: "members"}}
	engine.Handle(w, r, p, m.exportCSV)

	t.Log(w.Body)
	assert.Contains(t, w.Body.String(), "Test User 2")
	assert.Contains(t, w.Body.String(), "test@example.com")
}
