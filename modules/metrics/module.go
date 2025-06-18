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
	mgr.Add(engine.Poll(time.Minute, m.visitSamplings))
}

func (m *Module) visitSamplings(ctx context.Context) bool {
	samplings, err := m.getSamplings(ctx)
	if err != nil {
		slog.Error("failed to get metric samplings", "error", err)
		return false
	}

	for _, sample := range samplings {
		m.evalSampling(ctx, sample)
	}
	return false
}

func (m *Module) getSamplings(ctx context.Context) ([]*sampling, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT name, query, interval_seconds, target_table FROM metrics_samplings`)
	if err != nil {
		return nil, fmt.Errorf("querying samplings: %w", err)
	}
	defer rows.Close()

	var samplings []*sampling
	for rows.Next() {
		var sample sampling
		var intervalSeconds int64
		if err := rows.Scan(&sample.Name, &sample.Query, &intervalSeconds, &sample.TargetTable); err != nil {
			return nil, fmt.Errorf("scanning sampling: %w", err)
		}
		sample.Interval = time.Duration(intervalSeconds) * time.Second
		samplings = append(samplings, &sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating samplings: %w", err)
	}

	return samplings, nil
}

func (m *Module) evalSampling(ctx context.Context, sample *sampling) bool {
	var since *float64
	var start float64
	query := fmt.Sprintf("SELECT unixepoch('subsec') - MAX(timestamp), COALESCE(MAX(timestamp), 0.0) FROM %s WHERE series = $1", sample.TargetTable)
	err := m.db.QueryRowContext(ctx, query, sample.Name).Scan(&since, &start)
	if err != nil && err != sql.ErrNoRows {
		slog.Error("failed to check for metric", "metric", sample.Name, "error", err)
		return false
	}
	if err == nil && since != nil && *since < sample.Interval.Seconds() {
		return true // not ready to be sampled yet
	}

	insertQuery := fmt.Sprintf("INSERT INTO %s (series, value) VALUES ($1, (%s))", sample.TargetTable, sample.Query)
	_, err = m.db.ExecContext(ctx, insertQuery, sample.Name, sql.Named("last", int64(start)))
	if err != nil {
		slog.Error("failed to insert sampled metric", "metric", sample.Name, "target", sample.TargetTable, "error", err)
		return false
	}

	slog.Info("sampled metric", "metric", sample.Name, "target", sample.TargetTable)
	return true
}

type sampling struct {
	Name        string
	Query       string
	Interval    time.Duration
	TargetTable string
}
