package admin

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/skip2/go-qrcode"
	"github.com/wcharczuk/go-chart/v2"
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
		router.HandleFunc("GET /admin"+view.RelPath, router.WithAuthn(m.onlyLeadership(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			renderAdminList(m.nav, view.Title, "/admin/search"+view.RelPath).Render(r.Context(), w)
		})))

		router.HandleFunc("POST /admin/search"+view.RelPath, router.WithAuthn(m.onlyLeadership(func(w http.ResponseWriter, r *http.Request) {
			const limit = 20
			txn, err := m.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				engine.SystemError(w, err.Error())
				return
			}
			defer txn.Rollback()

			q, rowCountQuery, args := view.BuildQuery(r)

			// Get the row count
			var rowCount int64
			err = txn.QueryRowContext(r.Context(), rowCountQuery, args...).Scan(&rowCount)
			if err != nil {
				engine.SystemError(w, err.Error())
				return
			}
			currentPage, _ := strconv.ParseInt(r.FormValue("currentpage"), 10, 0)

			// Query
			args = append(args, sql.Named("limit", limit), sql.Named("offset", max(currentPage-1, 0)*limit))
			results, err := txn.QueryContext(r.Context(), q, args...)
			if err != nil {
				engine.SystemError(w, err.Error())
				return
			}
			defer results.Close()

			rows, err := view.BuildRows(results)
			if err != nil {
				engine.SystemError(w, err.Error())
				return
			}

			w.Header().Set("Content-Type", "text/html")
			renderAdminListElements(view.Rows, rows, max(currentPage, 1), max(rowCount/limit, 1)).Render(r.Context(), w)
		})))
	}

	router.HandleFunc("GET /admin", router.WithAuthn(m.onlyLeadership(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, m.nav[0].Path, http.StatusSeeOther)
	})))

	router.HandleFunc("GET /admin/members/{id}", router.WithAuthn(m.onlyLeadership(func(w http.ResponseWriter, r *http.Request) {
		mem, events, err := querySingleMember(r.Context(), m.db, r.PathValue("id"))
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/html")
		renderSingleMember(m.nav, mem, events).Render(r.Context(), w)
	})))

	router.HandleFunc("GET /admin/members/{id}/logincode", router.WithAuthn(m.onlyLeadership(func(w http.ResponseWriter, r *http.Request) {
		tok, err := m.links.Sign(&jwt.RegisteredClaims{
			Subject:   r.PathValue("id"),
			ExpiresAt: &jwt.NumericDate{Time: time.Now().Add(time.Minute * 5)},
		})
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}

		url := fmt.Sprintf("%s/login?t=%s", m.self, url.QueryEscape(tok))
		p, err := qrcode.Encode(url, qrcode.Medium, 512)
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}

		w.Header().Set("Content-Type", "image/png")
		w.Write(p)
	})))

	router.HandleFunc("GET /admin/export/{table}", router.WithAuthn(m.onlyLeadership(m.exportCSV)))
	router.HandleFunc("GET /admin/chart", router.WithAuthn(m.onlyLeadership(m.renderMetricsChart)))
	router.HandleFunc("GET /admin/metrics", router.WithAuthn(m.onlyLeadership(m.renderMetricsPageHandler)))

	for _, handle := range formHandlers {
		router.HandleFunc("POST "+handle.Path, router.WithAuthn(m.onlyLeadership(handle.BuildHandler(m.db))))
	}
}

func (m *Module) onlyLeadership(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if meta := auth.GetUserMeta(r.Context()); meta == nil || !meta.Leadership {
			http.Error(w, "You must be a member of leadership to access this page", 403)
			return
		}
		next(w, r)
	}
}

func (m *Module) exportCSV(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.QueryContext(r.Context(), fmt.Sprintf("SELECT * FROM %s", r.PathValue("table")))
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		engine.SystemError(w, err.Error())
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
		if err := rows.Scan(ptrs...); err != nil {
			engine.SystemError(w, err.Error())
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
			http.Error(w, "invalid window duration", 400)
			return
		}
	}

	const q = "SELECT timestamp, value FROM metrics WHERE series = $1 AND timestamp > strftime('%s', 'now') - $2"
	rows, err := m.db.QueryContext(r.Context(), q, r.URL.Query().Get("series"), windowDuration.Seconds())
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	defer rows.Close()

	x := []time.Time{}
	y := []float64{}
	for rows.Next() {
		var ts, val float64
		if err := rows.Scan(&ts, &val); err != nil {
			engine.SystemError(w, err.Error())
			return
		}
		x = append(x, time.Unix(int64(ts), 0))
		y = append(y, val)
	}

	graph := chart.Chart{
		Width: 800,
		Series: []chart.Series{
			chart.TimeSeries{XValues: x, YValues: y},
		},
	}
	width, err := strconv.Atoi(r.URL.Query().Get("width"))
	if err == nil {
		graph.Width = width
	}

	w.Header().Set("Content-Type", "image/png")
	err = graph.Render(chart.PNG, w)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
}

func (m *Module) renderMetricsPageHandler(w http.ResponseWriter, r *http.Request) {
	selected := r.URL.Query().Get("interval")
	if selected == "" {
		selected = "1440h" // default to 60 days
	}
	dur, err := time.ParseDuration(selected)
	if err != nil {
		http.Error(w, "invalid interval", 400)
		return
	}

	rows, err := m.db.QueryContext(r.Context(), `SELECT DISTINCT series FROM metrics WHERE timestamp > strftime('%s', 'now') - ? ORDER BY series`, int64(dur.Seconds()))
	if err != nil {
		engine.SystemError(w, err.Error())
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
