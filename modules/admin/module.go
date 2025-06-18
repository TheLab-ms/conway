package admin

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/julienschmidt/httprouter"
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
		router.Handle("GET", "/admin"+view.RelPath, router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
			return engine.Component(renderAdminList(m.nav, view.Title, "/admin/search"+view.RelPath))
		})))

		router.Handle("POST", "/admin/search"+view.RelPath, router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
			const limit = 20
			txn, err := m.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return engine.Errorf("starting db transaction: %s", err)
			}
			defer txn.Rollback()

			q, rowCountQuery, args := view.BuildQuery(r)

			// Get the row count
			var rowCount int64
			err = txn.QueryRowContext(r.Context(), rowCountQuery, args...).Scan(&rowCount)
			if err != nil {
				return engine.Errorf("getting row count: %s", err)
			}
			currentPage, _ := strconv.ParseInt(r.FormValue("currentpage"), 10, 0)

			// Query
			args = append(args, sql.Named("limit", limit), sql.Named("offset", max(currentPage-1, 0)*limit))
			results, err := txn.QueryContext(r.Context(), q, args...)
			if err != nil {
				return engine.Errorf("querying the database: %s", err)
			}
			defer results.Close()

			rows, err := view.BuildRows(results)
			if err != nil {
				return engine.Errorf("scanning the query results: %s", err)
			}

			return engine.Component(renderAdminListElements(view.Rows, rows, max(currentPage, 1), max(rowCount/limit, 1)))
		})))
	}

	router.Handle("GET", "/admin", router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
		return engine.Redirect(m.nav[0].Path, http.StatusSeeOther)
	})))

	router.Handle("GET", "/admin/members/:id", router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
		mem, events, err := querySingleMember(r.Context(), m.db, ps.ByName("id"))
		if err != nil {
			return engine.Errorf("querying the database: %s", err)
		}
		return engine.Component(renderSingleMember(m.nav, mem, events))
	})))

	router.Handle("GET", "/admin/members/:id/logincode", router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
		tok, err := m.links.Sign(&jwt.RegisteredClaims{
			Subject:   ps.ByName("id"),
			ExpiresAt: &jwt.NumericDate{Time: time.Now().Add(time.Minute * 5)},
		})
		if err != nil {
			return engine.Error(err)
		}

		url := fmt.Sprintf("%s/login?t=%s", m.self, url.QueryEscape(tok))
		p, err := qrcode.Encode(url, qrcode.Medium, 512)
		if err != nil {
			return engine.Error(err)
		}

		return engine.PNG(p)
	})))

	router.Handle("GET", "/admin/export/:table", router.WithAuth(m.onlyLeadership(m.exportCSV)))
	router.Handle("GET", "/admin/chart", router.WithAuth(m.onlyLeadership(m.renderMetricsChart)))
	router.Handle("GET", "/admin/metrics", router.WithAuth(m.onlyLeadership(m.renderMetricsPageHandler)))

	for _, handle := range formHandlers {
		router.Handle("POST", handle.Path, router.WithAuth(m.onlyLeadership(handle.BuildHandler(m.db))))
	}
}

func (m *Module) onlyLeadership(next engine.Handler) engine.Handler {
	return func(r *http.Request, ps httprouter.Params) engine.Response {
		if meta := auth.GetUserMeta(r.Context()); meta == nil || !meta.Leadership {
			return engine.ClientErrorf(403, "You must be a member of leadership to access this page")
		}
		return next(r, ps)
	}
}

func (m *Module) exportCSV(r *http.Request, ps httprouter.Params) engine.Response {
	rows, err := m.db.QueryContext(r.Context(), fmt.Sprintf("SELECT * FROM %s", ps.ByName("table")))
	if err != nil {
		return engine.Errorf("querying table: %s", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return engine.Errorf("getting columns: %s", err)
	}

	w := &engine.CSVResponse{Rows: make([][]any, 1)}
	for _, col := range cols {
		w.Rows[0] = append(w.Rows[0], col)
	}

	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return engine.Errorf("scanning row: %s", err)
		}
		w.Rows = append(w.Rows, vals)
	}
	return w
}

func (m *Module) renderMetricsChart(r *http.Request, ps httprouter.Params) engine.Response {
	windowDuration := time.Hour * 24 * 7
	if window := r.URL.Query().Get("window"); window != "" {
		var err error
		windowDuration, err = time.ParseDuration(window)
		if err != nil {
			return engine.ClientErrorf(400, "invalid window duration: %s", err)
		}
	}

	const q = "SELECT timestamp, value FROM metrics WHERE series = $1 AND timestamp > strftime('%s', 'now') - $2"
	rows, err := m.db.QueryContext(r.Context(), q, r.URL.Query().Get("series"), windowDuration.Seconds())
	if err != nil {
		return engine.Errorf("querying table: %s", err)
	}
	defer rows.Close()

	x := []time.Time{}
	y := []float64{}
	for rows.Next() {
		var ts, val float64
		if err := rows.Scan(&ts, &val); err != nil {
			return engine.Errorf("scanning row: %s", err)
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

	buf := bytes.NewBuffer(nil)
	err = graph.Render(chart.PNG, buf)
	if err != nil {
		return engine.Errorf("converting chart to bytes: %s", err)
	}
	return engine.PNG(buf.Bytes())
}

func (m *Module) renderMetricsPageHandler(r *http.Request, ps httprouter.Params) engine.Response {
	selected := r.URL.Query().Get("interval")
	if selected == "" {
		selected = "720h" // default to 30 days
	}
	dur, err := time.ParseDuration(selected)
	if err != nil {
		return engine.ClientErrorf(400, "invalid interval: %s", err)
	}

	rows, err := m.db.QueryContext(r.Context(), `SELECT DISTINCT series FROM metrics WHERE timestamp > strftime('%s', 'now') - ? ORDER BY series`, int64(dur.Seconds()))
	if err != nil {
		return engine.Errorf("fetching metrics: %s", err)
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

	return engine.Component(renderMetricsAdminPage(m.nav, metrics, selected))
}
