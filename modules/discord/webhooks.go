package discord

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"

	"github.com/TheLab-ms/conway/engine"
)

// webhookRow represents a row from the discord_webhooks table for the admin UI.
type webhookRow struct {
	ID              int64
	WebhookURL      string
	TriggerEvent    string
	MessageTemplate string
	Username        string
	Enabled         bool
}

// triggerEvents is the list of all supported trigger events for webhook configuration.
var triggerEvents = []string{
	"EmailConfirmed",
	"AccessStatusChanged",
	"WaiverSigned",
	"DiscountTypeModified",
	"LeadershipStatusAdded",
	"LeadershipStatusRemoved",
	"NonBillableStatusAdded",
	"NonBillableStatusRemoved",
	"FobChanged",
	"Signup",
	"PrintCompleted",
	"PrintFailed",
}

// placeholderHelp maps trigger events to the available placeholders.
var placeholderHelp = map[string]string{
	"EmailConfirmed":           "{event}, {details}, {email}, {name}, {member_id}, {access_status}",
	"AccessStatusChanged":      "{event}, {details}, {email}, {name}, {member_id}, {access_status}",
	"WaiverSigned":             "{event}, {details}, {email}, {name}, {member_id}, {access_status}",
	"DiscountTypeModified":     "{event}, {details}, {email}, {name}, {member_id}, {access_status}",
	"LeadershipStatusAdded":    "{event}, {details}, {email}, {name}, {member_id}, {access_status}",
	"LeadershipStatusRemoved":  "{event}, {details}, {email}, {name}, {member_id}, {access_status}",
	"NonBillableStatusAdded":   "{event}, {details}, {email}, {name}, {member_id}, {access_status}",
	"NonBillableStatusRemoved": "{event}, {details}, {email}, {name}, {member_id}, {access_status}",
	"FobChanged":               "{event}, {details}, {email}, {name}, {member_id}, {access_status}",
	"Signup":                   "{event}, {email}, {member_id}, {details}",
	"PrintCompleted":           "{event}, {mention}, {printer_name}, {file_name}",
	"PrintFailed":              "{event}, {mention}, {printer_name}, {file_name}, {error_code}",
}

func (m *Module) attachWebhookRoutes(router *engine.Router) {
	router.HandleFunc("GET /admin/discord/webhooks", router.WithLeadership(m.handleWebhookList))
	router.HandleFunc("GET /admin/discord/webhooks/new", router.WithLeadership(m.handleWebhookNew))
	router.HandleFunc("POST /admin/discord/webhooks/new", router.WithLeadership(m.handleWebhookCreate))
	router.HandleFunc("GET /admin/discord/webhooks/{id}/edit", router.WithLeadership(m.handleWebhookEdit))
	router.HandleFunc("POST /admin/discord/webhooks/{id}/edit", router.WithLeadership(m.handleWebhookUpdate))
	router.HandleFunc("POST /admin/discord/webhooks/{id}/delete", router.WithLeadership(m.handleWebhookDelete))
}

func (m *Module) loadAllWebhooks() ([]webhookRow, error) {
	rows, err := m.db.Query("SELECT id, webhook_url, trigger_event, message_template, username, enabled FROM discord_webhooks ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var webhooks []webhookRow
	for rows.Next() {
		var wh webhookRow
		var enabled int
		if err := rows.Scan(&wh.ID, &wh.WebhookURL, &wh.TriggerEvent, &wh.MessageTemplate, &wh.Username, &enabled); err != nil {
			return nil, err
		}
		wh.Enabled = enabled == 1
		webhooks = append(webhooks, wh)
	}
	return webhooks, rows.Err()
}

func (m *Module) loadWebhook(id int64) (*webhookRow, error) {
	var wh webhookRow
	var enabled int
	err := m.db.QueryRow(
		"SELECT id, webhook_url, trigger_event, message_template, username, enabled FROM discord_webhooks WHERE id = ?", id,
	).Scan(&wh.ID, &wh.WebhookURL, &wh.TriggerEvent, &wh.MessageTemplate, &wh.Username, &enabled)
	if err != nil {
		return nil, err
	}
	wh.Enabled = enabled == 1
	return &wh, nil
}

func (m *Module) handleWebhookList(w http.ResponseWriter, r *http.Request) {
	webhooks, err := m.loadAllWebhooks()
	if engine.HandleError(w, err) {
		return
	}
	w.Header().Set("Content-Type", "text/html")
	renderWebhookListPage(webhooks).Render(r.Context(), w)
}

func (m *Module) handleWebhookNew(w http.ResponseWriter, r *http.Request) {
	wh := &webhookRow{
		Username: "Conway",
		Enabled:  true,
	}
	w.Header().Set("Content-Type", "text/html")
	renderWebhookFormPage(wh, true, "", "").Render(r.Context(), w)
}

func (m *Module) handleWebhookCreate(w http.ResponseWriter, r *http.Request) {
	wh := parseWebhookForm(r)
	if wh.WebhookURL == "" || wh.TriggerEvent == "" {
		w.Header().Set("Content-Type", "text/html")
		renderWebhookFormPage(wh, true, "", "Webhook URL and trigger event are required.").Render(r.Context(), w)
		return
	}

	enabled := 0
	if wh.Enabled {
		enabled = 1
	}

	_, err := m.db.ExecContext(r.Context(),
		"INSERT INTO discord_webhooks (webhook_url, trigger_event, message_template, username, enabled) VALUES (?, ?, ?, ?, ?)",
		wh.WebhookURL, wh.TriggerEvent, wh.MessageTemplate, wh.Username, enabled)
	if engine.HandleError(w, err) {
		return
	}

	http.Redirect(w, r, "/admin/discord/webhooks", http.StatusSeeOther)
}

func (m *Module) handleWebhookEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "The webhook ID is not valid.", 400)
		return
	}

	wh, err := m.loadWebhook(id)
	if err == sql.ErrNoRows {
		engine.ClientError(w, "Not Found", "Webhook not found.", 404)
		return
	}
	if engine.HandleError(w, err) {
		return
	}

	w.Header().Set("Content-Type", "text/html")
	renderWebhookFormPage(wh, false, "", "").Render(r.Context(), w)
}

func (m *Module) handleWebhookUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "The webhook ID is not valid.", 400)
		return
	}

	wh := parseWebhookForm(r)
	wh.ID = id

	if wh.WebhookURL == "" || wh.TriggerEvent == "" {
		w.Header().Set("Content-Type", "text/html")
		renderWebhookFormPage(wh, false, "", "Webhook URL and trigger event are required.").Render(r.Context(), w)
		return
	}

	enabled := 0
	if wh.Enabled {
		enabled = 1
	}

	result, err := m.db.ExecContext(r.Context(),
		"UPDATE discord_webhooks SET webhook_url = ?, trigger_event = ?, message_template = ?, username = ?, enabled = ? WHERE id = ?",
		wh.WebhookURL, wh.TriggerEvent, wh.MessageTemplate, wh.Username, enabled, id)
	if engine.HandleError(w, err) {
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		engine.ClientError(w, "Not Found", "Webhook not found.", 404)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	renderWebhookFormPage(wh, false, "Webhook saved successfully.", "").Render(r.Context(), w)
}

func (m *Module) handleWebhookDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "The webhook ID is not valid.", 400)
		return
	}

	_, err = m.db.ExecContext(r.Context(), "DELETE FROM discord_webhooks WHERE id = ?", id)
	if engine.HandleError(w, err) {
		return
	}

	http.Redirect(w, r, "/admin/discord/webhooks", http.StatusSeeOther)
}

func parseWebhookForm(r *http.Request) *webhookRow {
	return &webhookRow{
		WebhookURL:      r.FormValue("webhook_url"),
		TriggerEvent:    r.FormValue("trigger_event"),
		MessageTemplate: r.FormValue("message_template"),
		Username:        r.FormValue("username"),
		Enabled:         r.FormValue("enabled") == "on" || r.FormValue("enabled") == "1",
	}
}

func webhookPlaceholders(triggerEvent string) string {
	if h, ok := placeholderHelp[triggerEvent]; ok {
		return fmt.Sprintf("Available placeholders: %s", h)
	}
	return ""
}
