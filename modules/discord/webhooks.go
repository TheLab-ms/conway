package discord

import (
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
	TriggerTable    string // SQL trigger: the table to watch.
	TriggerOp       string // SQL trigger: INSERT, UPDATE, or DELETE.
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
	return webhooks, rows.Err()
}

func (m *Module) handleWebhookCreate(w http.ResponseWriter, r *http.Request) {
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

	if err := m.createTrigger(&wh); err != nil {
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

	// Recreate the SQL trigger.
	if err := m.createTrigger(&wh); err != nil {
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

// handleTableColumns returns the column names for a given table as JSON.
// Used by the admin UI to dynamically show available placeholders.
func (m *Module) handleTableColumns(w http.ResponseWriter, r *http.Request) {
	table := r.URL.Query().Get("table")
	if table == "" {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(""))
		return
	}

	cols, err := tableColumns(m.db, table)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(""))
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("{" + joinPlaceholders(cols) + "}"))
}

// joinPlaceholders formats column names as {col1}, {col2}, ... for display.
func joinPlaceholders(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c + "}"
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += ", {"
		}
		result += p
	}
	return result
}

func parseWebhookForm(r *http.Request) webhookRow {
	return webhookRow{
		WebhookURL:      r.FormValue("webhook_url"),
		MessageTemplate: r.FormValue("message_template"),
		Enabled:         r.FormValue("enabled") == "on" || r.FormValue("enabled") == "1",
		TriggerTable:    r.FormValue("trigger_table"),
		TriggerOp:       r.FormValue("trigger_op"),
	}
}
