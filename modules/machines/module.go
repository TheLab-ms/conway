package machines

import (
	"database/sql"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /machines", router.WithAuthn(m.renderView))
}

func (m *Module) renderView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := m.db.QueryContext(ctx, `
		SELECT pe.printer_name,
		CASE WHEN pe.job_finished_timestamp < strftime('%s','now') THEN NULL ELSE pe.job_finished_timestamp END AS job_finished_timestamp,
		pe.error_code
		FROM printer_events pe
		INNER JOIN (
			SELECT printer_name, MAX(timestamp) AS max_ts
			FROM printer_events
			GROUP BY printer_name
		) latest ON pe.printer_name = latest.printer_name AND pe.timestamp = latest.max_ts
		ORDER BY pe.printer_name ASC
	`)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	defer rows.Close()

	events := []*printerStatus{}
	for rows.Next() {
		var ev printerStatus
		err := rows.Scan(&ev.Name, &ev.JobFinishedTimestamp, &ev.ErrorCode)
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}
		events = append(events, &ev)
	}
	if err := rows.Err(); err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/html")
	renderMachines(events).Render(r.Context(), w)
}
