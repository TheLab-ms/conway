package metrics

import (
	"testing"

	"github.com/TheLab-ms/conway/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSampling(t *testing.T) {
	testDB := db.NewTest(t)
	m := &Module{db: testDB}

	_, err := testDB.Exec("INSERT INTO metrics_samplings (name, query, interval_seconds) VALUES ('test-metric', 'SELECT 42', 60)")
	require.NoError(t, err)

	assert.False(t, m.visitSamplings(t.Context()))
	assert.False(t, m.visitSamplings(t.Context()))

	var count int
	err = testDB.QueryRow("SELECT COUNT(*) FROM metrics WHERE series = 'test-metric'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}
