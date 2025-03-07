package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAggregate(t *testing.T) {
	ctx := context.Background()
	db := db.NewTest(t)
	m := &Module{db: db}

	// Basics
	for range 50 {
		time.Sleep(time.Millisecond)
		m.aggregate(ctx, &aggregate{
			Name:     "test",
			Query:    "SELECT COUNT(*) FROM metrics",
			Interval: time.Millisecond * 10,
		})
	}
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM metrics WHERE series = 'test'").Scan(&count)
	require.NoError(t, err)
	assert.Greater(t, count, 2)
	assert.Less(t, count, 11)

	// Make sure configured aggregates are valid sql
	for _, agg := range aggregates {
		assert.True(t, m.aggregate(ctx, agg))
		assert.True(t, m.aggregate(ctx, agg))
	}
}
