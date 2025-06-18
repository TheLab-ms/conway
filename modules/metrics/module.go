package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
)

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Minute, m.visitAggregates))
}

func (m *Module) visitAggregates(ctx context.Context) bool {
	aggregates, err := m.getAggregates(ctx)
	if err != nil {
		slog.Error("failed to get metric aggregates", "error", err)
		return false
	}

	for _, agg := range aggregates {
		m.evalAggregate(ctx, agg)
	}
	return false
}

func (m *Module) getAggregates(ctx context.Context) ([]*aggregate, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT name, query, interval_seconds FROM metrics_aggregates`)
	if err != nil {
		return nil, fmt.Errorf("querying aggregations: %w", err)
	}
	defer rows.Close()

	var aggregates []*aggregate
	for rows.Next() {
		var agg aggregate
		var intervalSeconds int64
		if err := rows.Scan(&agg.Name, &agg.Query, &intervalSeconds); err != nil {
			return nil, fmt.Errorf("scanning aggregate: %w", err)
		}
		agg.Interval = time.Duration(intervalSeconds) * time.Second
		aggregates = append(aggregates, &agg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating aggregates: %w", err)
	}

	return aggregates, nil
}

func (m *Module) evalAggregate(ctx context.Context, agg *aggregate) bool {
	var since *float64
	var start float64
	err := m.db.QueryRowContext(ctx, "SELECT unixepoch('subsec') - MAX(timestamp), COALESCE(MAX(timestamp), 0.0) FROM metrics WHERE series = $1", agg.Name).Scan(&since, &start)
	if err != nil && err != sql.ErrNoRows {
		slog.Error("failed to check for metric", "metric", agg.Name, "error", err)
		return false
	}
	if err == nil && since != nil && *since < agg.Interval.Seconds() {
		return true // not ready to be sampled yet
	}

	query := fmt.Sprintf("INSERT INTO metrics (series, value) VALUES ($1, (%s))", agg.Query)
	_, err = m.db.ExecContext(ctx, query, agg.Name, sql.Named("last", int64(start)))
	if err != nil {
		slog.Error("failed to insert aggregated metric", "metric", agg.Name, "error", err)
		return false
	}

	slog.Info("aggregated metric", "metric", agg.Name)
	return true
}

type aggregate struct {
	Name     string
	Query    string
	Interval time.Duration
}
