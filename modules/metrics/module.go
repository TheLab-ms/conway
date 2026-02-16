package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/a-h/templ"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

const defaultTTL = 2 * 365 * 24 * 60 * 60 // 2 years in seconds

const migration = `
CREATE TABLE IF NOT EXISTS metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec')),
    series TEXT NOT NULL,
    value REAL NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS metrics_timestamp_idx ON metrics (series, timestamp);

CREATE TABLE IF NOT EXISTS metrics_samplings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    query TEXT NOT NULL,
    interval_seconds INTEGER NOT NULL,
    target_table TEXT NOT NULL,
    created_at REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec'))
) STRICT;

CREATE INDEX IF NOT EXISTS metrics_samplings_name_idx ON metrics_samplings (name);

CREATE TRIGGER IF NOT EXISTS validate_metrics_sampling_target_table_insert
BEFORE INSERT ON metrics_samplings
FOR EACH ROW
BEGIN
    -- Check if the table exists
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM sqlite_master 
            WHERE type='table' AND name = NEW.target_table
        ) THEN RAISE(ABORT, 'Target table does not exist')
    END;

    
    -- Check if the table has the required 'series' column of type TEXT
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'series' AND type = 'TEXT'
        ) THEN RAISE(ABORT, 'Target table must have a series column of type TEXT')
    END;

    
    -- Check if the table has the required 'value' column of type REAL
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'value' AND type = 'REAL'
        ) THEN RAISE(ABORT, 'Target table must have a value column of type REAL')
    END;

    
    -- Check if the table has the required 'timestamp' column of type REAL
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'timestamp' AND type = 'REAL'
        ) THEN RAISE(ABORT, 'Target table must have a timestamp column of type REAL')
    END;
END;

CREATE TRIGGER IF NOT EXISTS validate_metrics_sampling_target_table_update
BEFORE UPDATE OF target_table ON metrics_samplings
FOR EACH ROW
BEGIN
    -- Check if the table exists
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM sqlite_master 
            WHERE type='table' AND name = NEW.target_table
        ) THEN RAISE(ABORT, 'Target table does not exist')
    END;

    
    -- Check if the table has the required 'series' column of type TEXT
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'series' AND type = 'TEXT'
        ) THEN RAISE(ABORT, 'Target table must have a series column of type TEXT')
    END;

    
    -- Check if the table has the required 'value' column of type REAL
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'value' AND type = 'REAL'
        ) THEN RAISE(ABORT, 'Target table must have a value column of type REAL')
    END;

    
    -- Check if the table has the required 'timestamp' column of type REAL
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'timestamp' AND type = 'REAL'
        ) THEN RAISE(ABORT, 'Target table must have a timestamp column of type REAL')
    END;
END;

INSERT OR IGNORE INTO metrics_samplings (name, query, interval_seconds, target_table) VALUES
    ('active-members', 'SELECT COUNT(*) FROM members WHERE access_status = ''Ready''', 86400, 'metrics'),
    ('daily-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 86400, 'metrics'),
    ('weekly-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 604800, 'metrics'),
    ('monthly-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 2592000, 'metrics');
`

type Module struct {
	db *sql.DB
}

func New(d *sql.DB) *Module {
	engine.MustMigrate(d, migration)
	return &Module{db: d}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Minute, m.visitSamplings))
	mgr.Add(engine.Poll(time.Hour*24, engine.Cleanup(m.db, "old metrics",
		"DELETE FROM metrics WHERE timestamp < unixepoch('subsec') - ?", defaultTTL)))
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

// samplingRow represents a metrics_samplings row for the admin UI.
type samplingRow struct {
	ID        int64
	Name      string
	Query     string
	Interval  string // Go duration string (e.g. "24h", "168h")
	CreatedAt float64
}

// ConfigSpec returns the Metric Samplings configuration specification (read-only with ExtraContent).
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "metrics",
		Title:       "Metric Samplings",
		ReadOnly:    true,
		Order:       50,
		Description: configDescription(),
		ExtraContent: func(ctx context.Context) templ.Component {
			samplings, err := m.loadAllSamplings()
			if err != nil {
				slog.Error("failed to load samplings for config page", "error", err)
				samplings = nil
			}
			return renderSamplingsCard(samplings)
		},
	}
}

// AttachRoutes registers admin routes for managing metric samplings.
func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("POST /admin/metrics/samplings/new", router.WithLeadership(m.handleSamplingCreate))
	router.HandleFunc("POST /admin/metrics/samplings/{id}/edit", router.WithLeadership(m.handleSamplingUpdate))
	router.HandleFunc("POST /admin/metrics/samplings/{id}/delete", router.WithLeadership(m.handleSamplingDelete))
}

// loadAllSamplings returns all metrics_samplings rows for display.
func (m *Module) loadAllSamplings() ([]samplingRow, error) {
	rows, err := m.db.Query("SELECT id, name, query, interval_seconds, created_at FROM metrics_samplings ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var samplings []samplingRow
	for rows.Next() {
		var s samplingRow
		var intervalSeconds int64
		if err := rows.Scan(&s.ID, &s.Name, &s.Query, &intervalSeconds, &s.CreatedAt); err != nil {
			return nil, err
		}
		s.Interval = (time.Duration(intervalSeconds) * time.Second).String()
		samplings = append(samplings, s)
	}
	return samplings, rows.Err()
}

const hardcodedTargetTable = "metrics"

func (m *Module) handleSamplingCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		engine.ClientError(w, "Bad Request", "Failed to parse form.", 400)
		return
	}

	name, query, intervalSeconds, err := parseSamplingForm(r)
	if err != nil {
		engine.ClientError(w, "Invalid Input", err.Error(), 400)
		return
	}

	_, err = m.db.ExecContext(r.Context(),
		`INSERT INTO metrics_samplings (name, query, interval_seconds, target_table) VALUES (?, ?, ?, ?)`,
		name, query, intervalSeconds, hardcodedTargetTable)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			engine.ClientError(w, "Duplicate Name", "A sampling with that name already exists.", 400)
			return
		}
		if engine.HandleError(w, err) {
			return
		}
	}

	http.Redirect(w, r, "/admin/config/metrics", http.StatusSeeOther)
}

func (m *Module) handleSamplingUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "The sampling ID is not valid.", 400)
		return
	}

	if err := r.ParseForm(); err != nil {
		engine.ClientError(w, "Bad Request", "Failed to parse form.", 400)
		return
	}

	name, query, intervalSeconds, err := parseSamplingForm(r)
	if err != nil {
		engine.ClientError(w, "Invalid Input", err.Error(), 400)
		return
	}

	result, err := m.db.ExecContext(r.Context(),
		`UPDATE metrics_samplings SET name = ?, query = ?, interval_seconds = ?, target_table = ? WHERE id = ?`,
		name, query, intervalSeconds, hardcodedTargetTable, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			engine.ClientError(w, "Duplicate Name", "A sampling with that name already exists.", 400)
			return
		}
		if engine.HandleError(w, err) {
			return
		}
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		engine.ClientError(w, "Not Found", "Sampling not found.", 404)
		return
	}

	http.Redirect(w, r, "/admin/config/metrics", http.StatusSeeOther)
}

func (m *Module) handleSamplingDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "The sampling ID is not valid.", 400)
		return
	}

	_, err = m.db.ExecContext(r.Context(), "DELETE FROM metrics_samplings WHERE id = ?", id)
	if engine.HandleError(w, err) {
		return
	}

	http.Redirect(w, r, "/admin/config/metrics", http.StatusSeeOther)
}

// parseSamplingForm extracts and validates form fields, returning (name, query, intervalSeconds, error).
// The interval field accepts Go duration strings (e.g. "24h", "30m", "168h").
func parseSamplingForm(r *http.Request) (string, string, int64, error) {
	name := strings.TrimSpace(r.FormValue("name"))
	query := strings.TrimSpace(r.FormValue("query"))
	intervalStr := strings.TrimSpace(r.FormValue("interval"))

	if name == "" || query == "" || intervalStr == "" {
		return "", "", 0, fmt.Errorf("Name, query, and interval are required.")
	}

	d, err := time.ParseDuration(intervalStr)
	if err != nil {
		return "", "", 0, fmt.Errorf("Invalid interval: %q is not a valid Go duration (e.g. 24h, 30m, 168h).", intervalStr)
	}
	if d <= 0 {
		return "", "", 0, fmt.Errorf("Interval must be positive.")
	}

	return name, query, int64(d.Seconds()), nil
}

// truncateQuery truncates a SQL query for display in the table.
func truncateQuery(q string) string {
	if len(q) > 60 {
		return q[:57] + "..."
	}
	return q
}
