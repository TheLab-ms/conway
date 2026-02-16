package discord

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/TheLab-ms/conway/engine"
)

// webhookRow represents a row from the discord_webhooks table for the admin UI.
type webhookRow struct {
	ID              int64
	WebhookURL      string
	MessageTemplate string
	Enabled         bool
	TriggerTable    string         // SQL trigger: the table to watch.
	TriggerOp       string         // SQL trigger: INSERT, UPDATE, or DELETE.
	Conditions      []conditionRow // Optional conditions that gate when the trigger fires.
}

func (m *Module) attachWebhookRoutes(router *engine.Router) {
	router.HandleFunc("POST /admin/discord/webhooks/new", router.WithLeadership(m.handleWebhookCreate))
	router.HandleFunc("POST /admin/discord/webhooks/{id}/edit", router.WithLeadership(m.handleWebhookUpdate))
	router.HandleFunc("POST /admin/discord/webhooks/{id}/delete", router.WithLeadership(m.handleWebhookDelete))
	router.HandleFunc("GET /admin/discord/webhooks/columns", router.WithLeadership(m.handleTableColumns))
}

func (m *Module) loadAllWebhooks() ([]webhookRow, error) {
	rows, err := m.db.Query("SELECT id, webhook_url, message_template, enabled, trigger_table, trigger_op FROM discord_webhooks ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var webhooks []webhookRow
	for rows.Next() {
		var wh webhookRow
		var enabled int
		if err := rows.Scan(&wh.ID, &wh.WebhookURL, &wh.MessageTemplate, &enabled, &wh.TriggerTable, &wh.TriggerOp); err != nil {
			return nil, err
		}
		wh.Enabled = enabled == 1
		webhooks = append(webhooks, wh)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	// Load conditions for each webhook. Done after closing the rows iterator
	// to avoid deadlock with SQLite's MaxOpenConns=1.
	for i := range webhooks {
		conditions, err := m.loadConditions(webhooks[i].ID)
		if err != nil {
			return nil, fmt.Errorf("loading conditions for webhook %d: %w", webhooks[i].ID, err)
		}
		webhooks[i].Conditions = conditions
	}

	return webhooks, nil
}

func (m *Module) handleWebhookCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		engine.ClientError(w, "Bad Request", "Failed to parse form.", 400)
		return
	}

	wh := parseWebhookForm(r)
	if wh.WebhookURL == "" || wh.TriggerTable == "" || wh.TriggerOp == "" {
		http.Redirect(w, r, "/admin/config/discord", http.StatusSeeOther)
		return
	}

	enabled := 0
	if wh.Enabled {
		enabled = 1
	}

	result, err := m.db.ExecContext(r.Context(),
		`INSERT INTO discord_webhooks (webhook_url, message_template, enabled, trigger_table, trigger_op)
		 VALUES (?, ?, ?, ?, ?)`,
		wh.WebhookURL, wh.MessageTemplate, enabled, wh.TriggerTable, wh.TriggerOp)
	if engine.HandleError(w, err) {
		return
	}

	id, err := result.LastInsertId()
	if engine.HandleError(w, err) {
		return
	}
	wh.ID = id

	if err := m.saveConditions(wh.ID, wh.Conditions); err != nil {
		slog.Error("failed to save webhook conditions", "error", err, "webhookID", wh.ID)
	}

	if err := m.createTrigger(&wh, wh.Conditions); err != nil {
		slog.Error("failed to create webhook trigger", "error", err, "webhookID", wh.ID)
	}

	http.Redirect(w, r, "/admin/config/discord", http.StatusSeeOther)
}

func (m *Module) handleWebhookUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "The webhook ID is not valid.", 400)
		return
	}

	if err := r.ParseForm(); err != nil {
		engine.ClientError(w, "Bad Request", "Failed to parse form.", 400)
		return
	}

	wh := parseWebhookForm(r)
	wh.ID = id

	if wh.WebhookURL == "" || wh.TriggerTable == "" || wh.TriggerOp == "" {
		http.Redirect(w, r, "/admin/config/discord", http.StatusSeeOther)
		return
	}

	enabled := 0
	if wh.Enabled {
		enabled = 1
	}

	result, err := m.db.ExecContext(r.Context(),
		`UPDATE discord_webhooks SET webhook_url = ?, message_template = ?, enabled = ?, trigger_table = ?, trigger_op = ? WHERE id = ?`,
		wh.WebhookURL, wh.MessageTemplate, enabled, wh.TriggerTable, wh.TriggerOp, id)
	if engine.HandleError(w, err) {
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		engine.ClientError(w, "Not Found", "Webhook not found.", 404)
		return
	}

	if err := m.saveConditions(wh.ID, wh.Conditions); err != nil {
		slog.Error("failed to save webhook conditions", "error", err, "webhookID", wh.ID)
	}

	// Recreate the SQL trigger.
	if err := m.createTrigger(&wh, wh.Conditions); err != nil {
		slog.Error("failed to recreate webhook trigger", "error", err, "webhookID", wh.ID)
	}

	http.Redirect(w, r, "/admin/config/discord", http.StatusSeeOther)
}

func (m *Module) handleWebhookDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "The webhook ID is not valid.", 400)
		return
	}

	// Drop the SQL trigger first.
	m.dropTrigger(id)

	_, err = m.db.ExecContext(r.Context(), "DELETE FROM discord_webhooks WHERE id = ?", id)
	if engine.HandleError(w, err) {
		return
	}

	http.Redirect(w, r, "/admin/config/discord", http.StatusSeeOther)
}

// handleTableColumns returns the column names and types for a given table as JSON.
// Used by the admin UI to dynamically show available placeholders.
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

func parseWebhookForm(r *http.Request) webhookRow {
	return webhookRow{
		WebhookURL:      r.FormValue("webhook_url"),
		MessageTemplate: r.FormValue("message_template"),
		Enabled:         r.FormValue("enabled") == "on" || r.FormValue("enabled") == "1",
		TriggerTable:    r.FormValue("trigger_table"),
		TriggerOp:       r.FormValue("trigger_op"),
		Conditions:      parseConditionsForm(r),
	}
}

// parseConditionsForm extracts the dynamic condition rows from the form.
// Conditions are submitted as parallel arrays: cond_column[], cond_operator[],
// cond_value[], and cond_logic[].
func parseConditionsForm(r *http.Request) []conditionRow {
	columns := r.Form["cond_column"]
	operators := r.Form["cond_operator"]
	values := r.Form["cond_value"]
	logics := r.Form["cond_logic"]

	var conditions []conditionRow
	for i := range columns {
		col := columns[i]
		if col == "" {
			continue
		}
		op := "="
		if i < len(operators) {
			op = operators[i]
		}
		val := ""
		if i < len(values) {
			val = values[i]
		}
		logic := "AND"
		if i < len(logics) {
			logic = logics[i]
		}
		conditions = append(conditions, conditionRow{
			ColumnName: col,
			Operator:   op,
			Value:      val,
			Logic:      logic,
		})
	}
	return conditions
}

// loadConditions returns all conditions for a given webhook, ordered by id.
func (m *Module) loadConditions(webhookID int64) ([]conditionRow, error) {
	rows, err := m.db.Query(
		"SELECT id, webhook_id, column_name, operator, value, logic FROM discord_webhook_conditions WHERE webhook_id = ? ORDER BY id",
		webhookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conditions []conditionRow
	for rows.Next() {
		var c conditionRow
		if err := rows.Scan(&c.ID, &c.WebhookID, &c.ColumnName, &c.Operator, &c.Value, &c.Logic); err != nil {
			return nil, err
		}
		conditions = append(conditions, c)
	}
	return conditions, rows.Err()
}

// saveConditions replaces all conditions for a webhook. It deletes existing
// conditions and inserts the new set within a transaction.
func (m *Module) saveConditions(webhookID int64, conditions []conditionRow) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM discord_webhook_conditions WHERE webhook_id = ?", webhookID); err != nil {
		return err
	}

	for _, c := range conditions {
		if c.ColumnName == "" {
			continue
		}
		if _, err := tx.Exec(
			"INSERT INTO discord_webhook_conditions (webhook_id, column_name, operator, value, logic) VALUES (?, ?, ?, ?, ?)",
			webhookID, c.ColumnName, c.Operator, c.Value, c.Logic,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}
