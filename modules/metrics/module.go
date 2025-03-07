package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
)

var aggregates = map[string]string{
	"active-members": "SELECT COUNT(*) FROM members WHERE access_status = 'Ready'",
}

type Module struct {
	db                *sql.DB
	aggregateInterval time.Duration
}

func New(db *sql.DB) *Module {
	return &Module{db: db, aggregateInterval: 24 * time.Hour}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	// TODO
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Second, m.visitAggregates))
}

func (m *Module) visitAggregates(ctx context.Context) bool {
	for name, query := range aggregates {
		m.aggregate(ctx, name, query)
	}
	return false
}

func (m *Module) aggregate(ctx context.Context, name, query string) bool {
	var since *float64
	err := m.db.QueryRowContext(ctx, "SELECT unixepoch('subsec') - MAX(timestamp) FROM metrics WHERE series = $1", name).Scan(&since)
	if err != nil && err != sql.ErrNoRows {
		slog.Error("failed to check for metric", "metric", name, "error", err)
		return false
	}
	if err == nil && since != nil && *since < m.aggregateInterval.Seconds() {
		return false
	}

	_, err = m.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO metrics (series, value) VALUES ($1, (%s))", query), name)
	if err != nil {
		slog.Error("failed to insert metric", "metric", name, "error", err)
		return false
	}
	slog.Info("aggregated metric", "metric", name)

	return false
}
