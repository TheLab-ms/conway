package machines

import (
	"database/sql"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
	"github.com/julienschmidt/httprouter"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/machines", router.WithAuth(m.renderMachinesView))
}

func (m *Module) renderMachinesView(r *http.Request, ps httprouter.Params) engine.Response {
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
		return engine.Error(err)
	}
	defer rows.Close()

	events := []*printerStatus{}
	for rows.Next() {
		var ev printerStatus
		err := rows.Scan(&ev.Name, &ev.JobFinishedTimestamp, &ev.ErrorCode)
		if err != nil {
			return engine.Error(err)
		}
		events = append(events, &ev)
	}
	if err := rows.Err(); err != nil {
		return engine.Error(err)
	}

	return engine.Component(renderMachines(events))
}
