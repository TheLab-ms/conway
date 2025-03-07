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

func (m *Module) AttachRoutes(router *engine.Router) {
	// TODO
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Second, m.visitAggregates))
}

func (m *Module) visitAggregates(ctx context.Context) bool {
	for _, agg := range aggregates {
		m.aggregate(ctx, agg)
	}
	return false
}

func (m *Module) aggregate(ctx context.Context, agg *aggregate) bool {
	var since *float64
	var start float64
	err := m.db.QueryRowContext(ctx, "SELECT unixepoch('subsec') - MAX(timestamp), COALESCE(MAX(timestamp), 0.0) FROM metrics WHERE series = $1", agg.Name).Scan(&since, &start)
	if err != nil && err != sql.ErrNoRows {
		slog.Error("failed to check for metric", "metric", agg.Name, "error", err)
		return false
	}
	if err == nil && since != nil && *since < agg.Interval.Seconds() {
		return true
	}

	query := fmt.Sprintf("INSERT INTO metrics (series, value) VALUES ($1, (%s))", agg.Query)
	_, err = m.db.ExecContext(ctx, query, agg.Name, sql.Named("last", int64(start)))
	if err != nil {
		slog.Error("failed to insert metric", "metric", agg.Name, "error", err)
		return false
	}
	slog.Info("aggregated metric", "metric", agg.Name)
	return true
}
