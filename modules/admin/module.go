package admin

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/golang-jwt/jwt/v5"
	"github.com/skip2/go-qrcode"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

type Module struct {
	db    *sql.DB
	self  *url.URL
	links *engine.TokenIssuer
	nav   []*navbarTab
}

func New(db *sql.DB, self *url.URL, linksIss *engine.TokenIssuer) *Module {
	nav := []*navbarTab{}
	for _, view := range listViews {
		nav = append(nav, &navbarTab{Title: view.Title, Path: "/admin" + view.RelPath})
	}
	nav = append(nav, &navbarTab{Title: "Metrics", Path: "/admin/metrics"})

	return &Module{db: db, self: self, links: linksIss, nav: nav}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	for _, view := range listViews {
		router.HandleFunc("GET /admin"+view.RelPath, router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			renderAdminList(m.nav, view.Title, "/admin/search"+view.RelPath).Render(r.Context(), w)
		}))

		router.HandleFunc("POST /admin/search"+view.RelPath, router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
			const limit = 20
			txn, err := m.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
			if engine.HandleError(w, err) {
				return
			}
			defer txn.Rollback()

			q, rowCountQuery, args := view.BuildQuery(r)

			// Get the row count
			var rowCount int64
			err = txn.QueryRowContext(r.Context(), rowCountQuery, args...).Scan(&rowCount)
			if engine.HandleError(w, err) {
				return
			}
			currentPage, _ := strconv.ParseInt(r.FormValue("currentpage"), 10, 0)

			// Query
			args = append(args, sql.Named("limit", limit), sql.Named("offset", max(currentPage-1, 0)*limit))
			results, err := txn.QueryContext(r.Context(), q, args...)
			if engine.HandleError(w, err) {
				return
			}
			defer results.Close()

			rows, err := view.BuildRows(results)
			if engine.HandleError(w, err) {
				return
			}

			w.Header().Set("Content-Type", "text/html")
			renderAdminListElements(view.Rows, rows, max(currentPage, 1), max(rowCount/limit, 1)).Render(r.Context(), w)
		}))
	}

	router.HandleFunc("GET /admin", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, m.nav[0].Path, http.StatusSeeOther)
	}))

	router.HandleFunc("GET /admin/members/{id}", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		mem, events, err := querySingleMember(r.Context(), m.db, r.PathValue("id"))
		if engine.HandleError(w, err) {
			return
		}
		w.Header().Set("Content-Type", "text/html")
		renderSingleMember(m.nav, mem, events).Render(r.Context(), w)
	}))

	router.HandleFunc("GET /admin/members/{id}/logincode", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		tok, err := m.links.Sign(&jwt.RegisteredClaims{
			Subject:   r.PathValue("id"),
			ExpiresAt: &jwt.NumericDate{Time: time.Now().Add(time.Minute * 5)},
		})
		if engine.HandleError(w, err) {
			return
		}

		url := fmt.Sprintf("%s/login?t=%s", m.self, url.QueryEscape(tok))
		p, err := qrcode.Encode(url, qrcode.Medium, 512)
		if engine.HandleError(w, err) {
			return
		}

		w.Header().Set("Content-Type", "image/png")
		w.Write(p)
	}))

	router.HandleFunc("GET /admin/export/{table}", router.WithLeadership(m.exportCSV))
	router.HandleFunc("GET /admin/chart", router.WithLeadership(m.renderMetricsChart))
	router.HandleFunc("GET /admin/metrics", router.WithLeadership(m.renderMetricsPageHandler))

	router.HandleFunc("POST /admin/members/{id}/delete", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		_, err := m.db.ExecContext(r.Context(), "DELETE FROM members WHERE id = $1", r.PathValue("id"))
		if engine.HandleError(w, err) {
			return
		}
		http.Redirect(w, r, "/admin/members", http.StatusSeeOther)
	}))

	for _, handle := range formHandlers {
		router.HandleFunc("POST "+handle.Path, router.WithLeadership(handle.BuildHandler(m.db)))
	}
}

func (m *Module) exportCSV(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.QueryContext(r.Context(), fmt.Sprintf("SELECT * FROM %s", r.PathValue("table")))
	if engine.HandleError(w, err) {
		return
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if engine.HandleError(w, err) {
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// Write header
	cw.Write(cols)

	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if engine.HandleError(w, rows.Scan(ptrs...)) {
			return
		}
		// Convert vals to strings
		strVals := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				strVals[i] = ""
			} else {
				strVals[i] = fmt.Sprint(v)
			}
		}
		cw.Write(strVals)
	}
}

func (m *Module) renderMetricsChart(w http.ResponseWriter, r *http.Request) {
	windowDuration := time.Hour * 24 * 7
	if window := r.URL.Query().Get("window"); window != "" {
		var err error
		windowDuration, err = time.ParseDuration(window)
		if err != nil {
			engine.ClientError(w, "Invalid Request", "Invalid window duration", 400)
			return
		}
	}

	const q = "SELECT timestamp, value FROM metrics WHERE series = $1 AND timestamp > strftime('%s', 'now') - $2"
	rows, err := m.db.QueryContext(r.Context(), q, r.URL.Query().Get("series"), windowDuration.Seconds())
	if engine.HandleError(w, err) {
		return
	}
	defer rows.Close()

	type dataPoint struct {
		Timestamp int64   `json:"t"`
		Value     float64 `json:"v"`
	}
	var data []dataPoint
	for rows.Next() {
		var ts, val float64
		if engine.HandleError(w, rows.Scan(&ts, &val)) {
			return
		}
		data = append(data, dataPoint{Timestamp: int64(ts), Value: val})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (m *Module) renderMetricsPageHandler(w http.ResponseWriter, r *http.Request) {
	selected := r.URL.Query().Get("interval")
	if selected == "" {
		selected = "1440h" // default to 60 days
	}
	dur, err := time.ParseDuration(selected)
	if err != nil {
		engine.ClientError(w, "Invalid Request", "Invalid interval", 400)
		return
	}

	rows, err := m.db.QueryContext(r.Context(), `SELECT DISTINCT series FROM metrics WHERE timestamp > strftime('%s', 'now') - ? ORDER BY series`, int64(dur.Seconds()))
	if engine.HandleError(w, err) {
		return
	}
	defer rows.Close()

	var metrics []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		metrics = append(metrics, name)
	}

	w.Header().Set("Content-Type", "text/html")
	renderMetricsAdminPage(m.nav, metrics, selected).Render(r.Context(), w)
}
