package metrics

import (
	"testing"

	"github.com/TheLab-ms/conway/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSamplingBasics(t *testing.T) {
	testDB := db.NewTest(t)
	m := &Module{db: testDB}

	// Create a custom metrics table
	_, err := testDB.Exec(`
		CREATE TABLE custom_metrics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec')),
			series TEXT NOT NULL,
			value REAL NOT NULL
		) STRICT;
	`)
	require.NoError(t, err)

	// Insert a sampling with the custom target
	_, err = testDB.Exec("INSERT INTO metrics_samplings (name, query, interval_seconds, target_table) VALUES ('custom-metric', 'SELECT 99', 60, 'custom_metrics')")
	require.NoError(t, err)

	assert.False(t, m.visitSamplings(t.Context()))
	assert.False(t, m.visitSamplings(t.Context()))

	// Check that the metric was inserted into the custom table
	var count int
	err = testDB.QueryRow("SELECT COUNT(*) FROM custom_metrics WHERE series = 'custom-metric'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Verify the value is correct
	var value float64
	err = testDB.QueryRow("SELECT value FROM custom_metrics WHERE series = 'custom-metric'").Scan(&value)
	require.NoError(t, err)
	assert.Equal(t, 99.0, value)
}
