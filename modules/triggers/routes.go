package triggers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
)

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		engine.ClientError(w, "Bad Request", "Failed to parse form.", 400)
		return
	}

	tr, err := parseTriggerForm(r)
	if err != nil {
		engine.ClientError(w, "Invalid Input", err.Error(), 400)
		return
	}

	if tr.TriggerType == "event" && (tr.TriggerTable == "" || tr.TriggerOp == "" || tr.ActionSQL == "") {
		http.Redirect(w, r, "/admin/config/triggers", http.StatusSeeOther)
		return
	}
	if tr.TriggerType == "timed" && (tr.ActionSQL == "" || tr.IntervalSeconds <= 0) {
		http.Redirect(w, r, "/admin/config/triggers", http.StatusSeeOther)
		return
	}

	enabled := 0
	if tr.Enabled {
		enabled = 1
	}

	result, err := m.db.ExecContext(r.Context(),
		`INSERT INTO triggers (name, enabled, trigger_type, trigger_table, trigger_op, when_clause, action_sql, interval_seconds) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		tr.Name, enabled, tr.TriggerType, tr.TriggerTable, tr.TriggerOp, tr.WhenClause, tr.ActionSQL, tr.IntervalSeconds)
	if engine.HandleError(w, err) {
		return
	}

	id, err := result.LastInsertId()
	if engine.HandleError(w, err) {
		return
	}
	tr.ID = id

	// Only create a SQLite trigger for event triggers.
	if tr.TriggerType == "event" {
		if err := m.createTrigger(&tr); err != nil {
			engine.HandleError(w, fmt.Errorf("creating SQL trigger: %w", err))
			return
		}
	}

	http.Redirect(w, r, "/admin/config/triggers", http.StatusSeeOther)
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "The trigger ID is not valid.", 400)
		return
	}

	if err := r.ParseForm(); err != nil {
		engine.ClientError(w, "Bad Request", "Failed to parse form.", 400)
		return
	}

	tr, err := parseTriggerForm(r)
	if err != nil {
		engine.ClientError(w, "Invalid Input", err.Error(), 400)
		return
	}
	tr.ID = id

	if tr.TriggerType == "event" && (tr.TriggerTable == "" || tr.TriggerOp == "" || tr.ActionSQL == "") {
		http.Redirect(w, r, "/admin/config/triggers", http.StatusSeeOther)
		return
	}
	if tr.TriggerType == "timed" && (tr.ActionSQL == "" || tr.IntervalSeconds <= 0) {
		http.Redirect(w, r, "/admin/config/triggers", http.StatusSeeOther)
		return
	}

	enabled := 0
	if tr.Enabled {
		enabled = 1
	}

	result, err := m.db.ExecContext(r.Context(),
		`UPDATE triggers SET name = ?, enabled = ?, trigger_type = ?, trigger_table = ?, trigger_op = ?, when_clause = ?, action_sql = ?, interval_seconds = ? WHERE id = ?`,
		tr.Name, enabled, tr.TriggerType, tr.TriggerTable, tr.TriggerOp, tr.WhenClause, tr.ActionSQL, tr.IntervalSeconds, id)
	if engine.HandleError(w, err) {
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		engine.ClientError(w, "Not Found", "Trigger not found.", 404)
		return
	}

	if tr.TriggerType == "timed" {
		// Timed triggers don't use SQLite triggers, so drop any existing one
		// (in case the type was changed from event to timed).
		m.dropTrigger(id)
	} else {
		// Recreate the SQL trigger (handles enabled/disabled state).
		if err := m.createTrigger(&tr); err != nil {
			engine.HandleError(w, fmt.Errorf("recreating SQL trigger: %w", err))
			return
		}
	}

	http.Redirect(w, r, "/admin/config/triggers", http.StatusSeeOther)
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "The trigger ID is not valid.", 400)
		return
	}

	// Drop the SQL trigger first (no-op if it was a timed trigger).
	m.dropTrigger(id)

	_, err = m.db.ExecContext(r.Context(), "DELETE FROM triggers WHERE id = ?", id)
	if engine.HandleError(w, err) {
		return
	}

	http.Redirect(w, r, "/admin/config/triggers", http.StatusSeeOther)
}

// handleTableColumns returns the column names and types for a given table as JSON.
func (m *Module) handleTableColumns(w http.ResponseWriter, r *http.Request) {
	table := r.URL.Query().Get("table")
	if table == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}

	cols, err := tableColumnsInfo(m.db, table)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}

	type colJSON struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	out := make([]colJSON, len(cols))
	for i, c := range cols {
		out[i] = colJSON{Name: c.Name, Type: c.Type}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func parseTriggerForm(r *http.Request) (triggerRow, error) {
	triggerType := r.FormValue("trigger_type")
	if triggerType == "" {
		triggerType = "event"
	}
	if triggerType != "event" && triggerType != "timed" {
		return triggerRow{}, fmt.Errorf("Invalid trigger type: %q", triggerType)
	}

	tr := triggerRow{
		Name:        r.FormValue("name"),
		Enabled:     r.FormValue("enabled") == "on" || r.FormValue("enabled") == "1",
		TriggerType: triggerType,
		ActionSQL:   r.FormValue("action_sql"),
	}

	if triggerType == "event" {
		tr.TriggerTable = r.FormValue("trigger_table")
		tr.TriggerOp = r.FormValue("trigger_op")
		tr.WhenClause = r.FormValue("when_clause")
	} else {
		intervalStr := strings.TrimSpace(r.FormValue("interval"))
		if intervalStr == "" {
			return triggerRow{}, fmt.Errorf("Interval is required for timed triggers.")
		}
		d, err := time.ParseDuration(intervalStr)
		if err != nil {
			return triggerRow{}, fmt.Errorf("Invalid interval: %q is not a valid Go duration (e.g. 24h, 30m, 168h).", intervalStr)
		}
		if d <= 0 {
			return triggerRow{}, fmt.Errorf("Interval must be positive.")
		}
		tr.IntervalSeconds = int64(d.Seconds())
	}

	return tr, nil
}
