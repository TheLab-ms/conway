package pruning

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

func New(db *sql.DB) *Module { return &Module{db: db} }

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour, m.runPruneJobs))
}

func (m *Module) runPruneJobs(ctx context.Context) bool {
	jobs, err := m.listPruneJobs(ctx)
	if err != nil {
		slog.Error("failed to list prune jobs", "error", err)
		return false
	}
	for table, job := range jobs {
		m.runPruneJob(ctx, table, job)
	}
	return false
}

func (m *Module) listPruneJobs(ctx context.Context) (map[string]string, error) {
	query, err := m.db.QueryContext(ctx, "SELECT table_name, column, ttl FROM pruning_jobs")
	if err != nil {
		return nil, err
	}
	defer query.Close()

	queries := map[string]string{}
	for query.Next() {
		var table, column string
		var ttl int // seconds
		if err := query.Scan(&table, &column, &ttl); err != nil {
			return nil, err
		}

		q := fmt.Sprintf("DELETE FROM %s WHERE %s < strftime('%%s', 'now') - %d", table, column, ttl)
		queries[table] = q
	}

	if err := query.Err(); err != nil {
		return nil, err
	}

	return queries, nil
}

func (m *Module) runPruneJob(ctx context.Context, table, query string) {
	start := time.Now()
	result, err := m.db.ExecContext(ctx, query)
	if err != nil {
		slog.Error("failed to run prune job", "table", table, "error", err)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	slog.Info("prune job completed", "table", table, "duration", time.Since(start), "rows", rowsAffected)
}
