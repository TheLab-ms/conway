package admin

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/skip2/go-qrcode"
	"github.com/stripe/stripe-go/v78"
	"github.com/stripe/stripe-go/v78/customer"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

type Module struct {
	db             *sql.DB
	self           *url.URL
	links          *engine.TokenIssuer
	eventLogger    *engine.EventLogger
	nav            []*navbarTab
	configRegistry *config.Registry
	configStore    *config.Store
}

func New(db *sql.DB, self *url.URL, linksIss *engine.TokenIssuer, eventLogger *engine.EventLogger) *Module {
	nav := []*navbarTab{}
	for _, view := range listViews {
		nav = append(nav, &navbarTab{Title: view.Title, Path: "/admin" + view.RelPath})
	}
	nav = append(nav, &navbarTab{Title: "Metrics", Path: "/admin/metrics"})

	return &Module{
		db:          db,
		self:        self,
		links:       linksIss,
		eventLogger: eventLogger,
		nav:         nav,
	}
}

// SetConfigRegistry sets the config registry for dynamic configuration UI.
// This should be called after all modules are registered with the App.
func (m *Module) SetConfigRegistry(registry *config.Registry) {
	m.configRegistry = registry
	if registry != nil {
		m.configStore = config.NewStore(m.db, registry)
	}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	for _, view := range listViews {
		router.HandleFunc("GET /admin"+view.RelPath, router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			renderAdminList(m.nav, view.Title, "/admin/search"+view.RelPath, view.ExportTable, view.NewItemURL, view.Searchable, view.FilterParam, view.Filters).Render(r.Context(), w)
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

			// Query first page
			args = append(args, sql.Named("limit", limit), sql.Named("offset", 0))
			results, err := txn.QueryContext(r.Context(), q, args...)
			if engine.HandleError(w, err) {
				return
			}
			defer results.Close()

			rows, err := view.BuildRows(results)
			if engine.HandleError(w, err) {
				return
			}

			hasMore := rowCount > limit
			moreURL := ""
			if hasMore {
				search := r.PostFormValue("search")
				moreURL = fmt.Sprintf("/admin/more%s?page=2&search=%s", view.RelPath, url.QueryEscape(search))
				// Include any filter params in the moreURL
				r.ParseForm()
				if view.FilterParam != "" {
					for _, f := range r.Form[view.FilterParam] {
						moreURL += "&" + view.FilterParam + "=" + url.QueryEscape(f)
					}
				}
			}
			colCount := len(view.Rows)
			if colCount == 0 {
				colCount = 1
			}

			w.Header().Set("Content-Type", "text/html")
			renderAdminListElements(view.Rows, rows, moreURL, hasMore, colCount).Render(r.Context(), w)
		}))

		router.HandleFunc("GET /admin/more"+view.RelPath, router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
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

			page, _ := strconv.ParseInt(r.URL.Query().Get("page"), 10, 0)
			if page < 1 {
				page = 1
			}
			offset := (page - 1) * limit

			// Query
			args = append(args, sql.Named("limit", limit), sql.Named("offset", offset))
			results, err := txn.QueryContext(r.Context(), q, args...)
			if engine.HandleError(w, err) {
				return
			}
			defer results.Close()

			rows, err := view.BuildRows(results)
			if engine.HandleError(w, err) {
				return
			}

			hasMore := rowCount > offset+limit
			moreURL := ""
			if hasMore {
				search := r.URL.Query().Get("search")
				moreURL = fmt.Sprintf("/admin/more%s?page=%d&search=%s", view.RelPath, page+1, url.QueryEscape(search))
				// Include any filter params in the moreURL
				if view.FilterParam != "" {
					for _, f := range r.URL.Query()[view.FilterParam] {
						moreURL += "&" + view.FilterParam + "=" + url.QueryEscape(f)
					}
				}
			}
			colCount := len(view.Rows)
			if colCount == 0 {
				colCount = 1
			}

			w.Header().Set("Content-Type", "text/html")
			renderAdminListRows(rows, moreURL, hasMore, colCount).Render(r.Context(), w)
		}))
	}

	router.HandleFunc("GET /admin", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, m.nav[0].Path, http.StatusSeeOther)
	}))

	router.HandleFunc("POST /admin/members/new", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
		if email == "" {
			engine.ClientError(w, "Invalid Input", "Email address is required", 400)
			return
		}

		var id int64
		err := m.db.QueryRowContext(r.Context(),
			"INSERT INTO members (email) VALUES ($1) ON CONFLICT (email) DO UPDATE SET email=email RETURNING id", email).Scan(&id)
		if engine.HandleError(w, err) {
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/admin/members/%d", id), http.StatusSeeOther)
	}))

	router.HandleFunc("POST /admin/members/{id}/stripe-customer", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		memberID := r.PathValue("id")

		// Load Stripe API key
		var apiKey string
		err := m.db.QueryRowContext(r.Context(),
			"SELECT api_key FROM stripe_config ORDER BY version DESC LIMIT 1").Scan(&apiKey)
		if err != nil || apiKey == "" {
			engine.ClientError(w, "Configuration Error", "Stripe API key is not configured", 400)
			return
		}
		stripe.Key = apiKey

		// Query member email
		var email string
		err = m.db.QueryRowContext(r.Context(), "SELECT email FROM members WHERE id = $1", memberID).Scan(&email)
		if engine.HandleError(w, err) {
			return
		}

		// Create Stripe customer
		cust, err := customer.New(&stripe.CustomerParams{
			Email: &email,
		})
		if engine.HandleError(w, err) {
			return
		}

		// Store customer ID
		_, err = m.db.ExecContext(r.Context(),
			"UPDATE members SET stripe_customer_id = $1 WHERE id = $2", cust.ID, memberID)
		if engine.HandleError(w, err) {
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/admin/members/%s", memberID), http.StatusSeeOther)
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

	router.HandleFunc("GET /admin/members/{id}/events", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		const limit = 10
		memberID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if engine.HandleError(w, err) {
			return
		}

		page, _ := strconv.ParseInt(r.URL.Query().Get("page"), 10, 0)
		if page < 1 {
			page = 1
		}
		offset := int((page - 1) * limit)

		events, err := queryMemberEvents(r.Context(), m.db, memberID, limit+1, offset)
		if engine.HandleError(w, err) {
			return
		}

		hasMore := len(events) > limit
		if hasMore {
			events = events[:limit]
		}

		w.Header().Set("Content-Type", "text/html")
		renderMemberEventRows(events, memberID, int(page), hasMore).Render(r.Context(), w)
	}))

	router.HandleFunc("GET /admin/export/{table}", router.WithLeadership(m.exportCSV))
	router.HandleFunc("GET /admin/chart", router.WithLeadership(m.renderMetricsChart))
	router.HandleFunc("GET /admin/metrics", router.WithLeadership(m.renderMetricsPageHandler))

	// Dev tools routes
	router.HandleFunc("GET /admin/config/dev/db", router.WithLeadership(m.handleDBConsole))
	router.HandleFunc("POST /admin/config/dev/db", router.WithLeadership(m.handleDBConsoleExec))

	// Configuration routes
	router.HandleFunc("GET /admin/config", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to the first config section from the registry
		if m.configRegistry != nil {
			specs := m.configRegistry.List()
			if len(specs) > 0 {
				http.Redirect(w, r, "/admin/config/"+specs[0].Module, http.StatusSeeOther)
				return
			}
		}
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}))

	// Generic config routes for registered modules
	if m.configRegistry != nil {
		router.HandleFunc("GET /admin/config/{module}", router.WithLeadership(m.handleGenericConfigPage))
		router.HandleFunc("POST /admin/config/{module}", router.WithLeadership(m.handleGenericConfigSave))
	}

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

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	// Cleanup old module events, keeping only the 100 most recent per module (FIFO)
	const maxEventsPerModule = 100
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "old module events",
		`DELETE FROM module_events WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY module ORDER BY created DESC) as rn
				FROM module_events
			) WHERE rn > ?
		)`, maxEventsPerModule)))
}

func (m *Module) exportCSV(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("table")

	// Whitelist of tables that may be exported as CSV.
	allowedExports := map[string]bool{
		"members":       true,
		"waivers":       true,
		"fob_swipes":    true,
		"member_events": true,
	}
	if !allowedExports[table] {
		engine.ClientError(w, "Forbidden", "Export is not allowed for this table", http.StatusForbidden)
		return
	}

	rows, err := m.db.QueryContext(r.Context(), fmt.Sprintf("SELECT * FROM %s", table))
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

// metricsChart represents a chart to render on the metrics page.
type metricsChart struct {
	Title  string
	Series string
	Color  string // hex color e.g. "#0d6efd", empty for default
}

func (m *Module) renderMetricsPageHandler(w http.ResponseWriter, r *http.Request) {
	selected := r.URL.Query().Get("interval")
	if selected == "" {
		selected = "1440h" // default to 60 days
	}

	// Load chart configuration from the metrics config table
	var configuredCharts []metricsChart
	if m.configStore != nil {
		cfg, _, loadErr := m.configStore.Load(r.Context(), "metrics")
		if loadErr == nil && cfg != nil {
			// Use reflection-free approach: query the JSON directly
			var chartsJSON string
			qErr := m.db.QueryRowContext(r.Context(),
				"SELECT charts_json FROM metrics_config ORDER BY version DESC LIMIT 1").Scan(&chartsJSON)
			if qErr == nil && chartsJSON != "" && chartsJSON != "[]" {
				var items []struct {
					Title  string `json:"title"`
					Series string `json:"series"`
					Color  string `json:"color"`
				}
				if json.Unmarshal([]byte(chartsJSON), &items) == nil {
					for _, item := range items {
						if item.Series != "" {
							configuredCharts = append(configuredCharts, metricsChart{
								Title:  item.Title,
								Series: item.Series,
								Color:  item.Color,
							})
						}
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "text/html")
	renderMetricsAdminPage(m.nav, configuredCharts, selected).Render(r.Context(), w)
}

// handleGenericConfigPage renders a configuration page for a registered module.
func (m *Module) handleGenericConfigPage(w http.ResponseWriter, r *http.Request) {
	moduleName := r.PathValue("module")

	spec, ok := m.configRegistry.Get(moduleName)
	if !ok {
		engine.ClientError(w, "Not Found", "Unknown configuration module", http.StatusNotFound)
		return
	}

	m.renderGenericConfig(w, r, spec, false, "")
}

// handleGenericConfigSave saves configuration for a registered module.
func (m *Module) handleGenericConfigSave(w http.ResponseWriter, r *http.Request) {
	moduleName := r.PathValue("module")

	spec, ok := m.configRegistry.Get(moduleName)
	if !ok {
		engine.ClientError(w, "Not Found", "Unknown configuration module", http.StatusNotFound)
		return
	}

	// Parse form into config
	cfg, err := m.configStore.ParseFormIntoConfig(r, moduleName)
	if err != nil {
		m.renderGenericConfig(w, r, spec, false, fmt.Sprintf("Failed to parse form: %v", err))
		return
	}

	// Save with secret preservation
	if err := m.configStore.Save(r.Context(), moduleName, cfg, true); err != nil {
		m.renderGenericConfig(w, r, spec, false, err.Error())
		return
	}

	m.renderGenericConfig(w, r, spec, true, "")
}

// renderGenericConfig renders a generic config page.
func (m *Module) renderGenericConfig(w http.ResponseWriter, r *http.Request, spec *config.ParsedSpec, saved bool, errMsg string) {
	var cfg any

	// ReadOnly specs (like fobapi) have no Type and no database table,
	// so skip loading config data for them.
	if !spec.ReadOnly {
		var err error
		cfg, _, err = m.configStore.Load(r.Context(), spec.Module)
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}
	}

	events := m.loadModuleEvents(r.Context(), spec.Module)

	w.Header().Set("Content-Type", "text/html")
	renderGenericConfigPage(m.nav, spec, cfg, events, m.getConfigSections(), saved, errMsg).Render(r.Context(), w)
}

// loadModuleEvents loads recent events for a module.
func (m *Module) loadModuleEvents(ctx context.Context, module string) []*config.Event {
	rows, err := m.db.QueryContext(ctx, `
		SELECT e.id, e.created, e.module, e.member, e.event_type, COALESCE(e.entity_id, ''), COALESCE(e.entity_name, ''), e.success, COALESCE(e.details, ''),
			   COALESCE(mem.name_override, mem.identifier, '') AS member_name
		FROM module_events e
		LEFT JOIN members mem ON e.member = mem.id
		WHERE e.module = $1
		ORDER BY e.created DESC
		LIMIT 20`, module)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []*config.Event
	for rows.Next() {
		var created int64
		var successInt int
		var memberName string
		event := &config.Event{}
		if rows.Scan(&event.ID, &created, &event.Module, &event.MemberID, &event.EventType, &event.EntityID, &event.EntityName, &successInt, &event.Details, &memberName) == nil {
			event.Created = time.Unix(created, 0)
			event.Success = successInt == 1
			// Use member name when available, fall back to entity name
			if memberName != "" {
				event.EntityName = memberName
			}
			events = append(events, event)
		}
	}
	return events
}

// getConfigSections returns the list of config sections for the sidebar,
// built entirely from the config registry.
func (m *Module) getConfigSections() []*configSection {
	if m.configRegistry == nil {
		return nil
	}

	var sections []*configSection
	for _, spec := range m.configRegistry.List() {
		sections = append(sections, &configSection{
			Name: spec.Title,
			Path: "/admin/config/" + spec.Module,
		})
	}
	return sections
}

// handleDBConsole renders the DB Console page.
func (m *Module) handleDBConsole(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	renderDBConsolePage(m.nav, m.getConfigSections(), "", "", nil, nil, 0, false).Render(r.Context(), w)
}

// handleDBConsoleExec executes a SQL query and renders the results.
func (m *Module) handleDBConsoleExec(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.FormValue("query"))
	if query == "" {
		m.handleDBConsole(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	sections := m.getConfigSections()

	// Determine if this is a query that returns rows.
	upper := strings.ToUpper(query)
	isSelect := strings.HasPrefix(upper, "SELECT") ||
		strings.HasPrefix(upper, "PRAGMA") ||
		strings.HasPrefix(upper, "EXPLAIN") ||
		strings.HasPrefix(upper, "WITH")

	if isSelect {
		rows, err := m.db.QueryContext(r.Context(), query)
		if err != nil {
			renderDBConsolePage(m.nav, sections, query, err.Error(), nil, nil, 0, true).Render(r.Context(), w)
			return
		}
		defer rows.Close()

		cols, err := rows.Columns()
		if err != nil {
			renderDBConsolePage(m.nav, sections, query, err.Error(), nil, nil, 0, true).Render(r.Context(), w)
			return
		}

		var resultRows [][]string
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				renderDBConsolePage(m.nav, sections, query, err.Error(), nil, nil, 0, true).Render(r.Context(), w)
				return
			}
			row := make([]string, len(vals))
			for i, v := range vals {
				if v == nil {
					row[i] = "NULL"
				} else {
					row[i] = fmt.Sprint(v)
				}
			}
			resultRows = append(resultRows, row)
		}

		renderDBConsolePage(m.nav, sections, query, "", cols, resultRows, 0, true).Render(r.Context(), w)
	} else {
		result, err := m.db.ExecContext(r.Context(), query)
		if err != nil {
			renderDBConsolePage(m.nav, sections, query, err.Error(), nil, nil, 0, true).Render(r.Context(), w)
			return
		}
		affected, _ := result.RowsAffected()
		renderDBConsolePage(m.nav, sections, query, "", nil, nil, affected, true).Render(r.Context(), w)
	}
}
