package pruning

import (
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasics(t *testing.T) {
	ctx := t.Context()
	ts := time.Now()
	db := db.OpenTest(t)
	m := New(db)

	_, err := db.ExecContext(ctx, `CREATE TABLE test_items (id INTEGER PRIMARY KEY, timestamp INTEGER)`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO pruning_jobs (table_name) VALUES ('test_items')`)
	require.NoError(t, err)

	// 3 years in the future
	_, err = db.ExecContext(ctx, `INSERT INTO test_items (id, timestamp) VALUES (1, ?)`, ts.Add(time.Hour*24*365*3).Unix())
	require.NoError(t, err)

	// 3 years in the past
	_, err = db.ExecContext(ctx, `INSERT INTO test_items (id, timestamp) VALUES (2, ?)`, ts.Add(-(time.Hour * 24 * 365 * 3)).Unix())
	require.NoError(t, err)

	// 1 year in the future
	_, err = db.ExecContext(ctx, `INSERT INTO test_items (id, timestamp) VALUES (3, ?)`, ts.Add(time.Hour*24*365).Unix())
	require.NoError(t, err)

	// 1 year in the past
	_, err = db.ExecContext(ctx, `INSERT INTO test_items (id, timestamp) VALUES (4, ?)`, ts.Add(-(time.Hour * 24 * 365)).Unix())
	require.NoError(t, err)

	m.runPruneJobs(ctx)
	m.runPruneJobs(ctx)

	rows, err := db.Query("SELECT id FROM test_items")
	require.NoError(t, err)
	defer rows.Close()

	ids := []int{}
	for rows.Next() {
		var id int
		rows.Scan(&id)
		ids = append(ids, id)
	}

	assert.ElementsMatch(t, []int{1, 3, 4}, ids)
}
