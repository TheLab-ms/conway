package admin

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/modules/auth"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

// migration ensures the bambu tables exist for the admin config pages,
// even if the machines module is not enabled.
const migration = `
CREATE TABLE IF NOT EXISTS bambu_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    printers_json TEXT NOT NULL DEFAULT '[]',
    poll_interval_seconds INTEGER NOT NULL DEFAULT 5
) STRICT;
`

type Module struct {
	db             *sql.DB
	self           *url.URL
	links          *engine.TokenIssuer
	authModule     *auth.Module
	eventLogger    *engine.EventLogger
	nav            []*navbarTab
	configRegistry *config.Registry
	configStore    *config.Store
}

func New(db *sql.DB, self *url.URL, linksIss *engine.TokenIssuer, eventLogger *engine.EventLogger) *Module {
	engine.MustMigrate(db, migration)

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

// SetAuthModule sets the auth module for generating login codes.
func (m *Module) SetAuthModule(a *auth.Module) {
	m.authModule = a
}

func (m *Module) AttachRoutes(router *engine.Router) {
	for _, view := range listViews {
		router.HandleFunc("GET /admin"+view.RelPath, router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			renderAdminList(m.nav, view.Title, "/admin/search"+view.RelPath, view.ExportTable, view.Searchable, view.FilterParam, view.Filters).Render(r.Context(), w)
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

	router.HandleFunc("GET /admin/members/{id}", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		mem, events, err := querySingleMember(r.Context(), m.db, r.PathValue("id"))
		if engine.HandleError(w, err) {
			return
		}
		w.Header().Set("Content-Type", "text/html")
		renderSingleMember(m.nav, mem, events).Render(r.Context(), w)
	}))

	router.HandleFunc("GET /admin/members/{id}/logincode", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		memberID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if engine.HandleError(w, err) {
			return
		}

		code, err := m.authModule.GenerateLoginCode(r.Context(), memberID)
		if engine.HandleError(w, err) {
			return
		}

		w.Header().Set("Content-Type", "text/html")
		renderLoginCodeResult(code).Render(r.Context(), w)
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

	// Configuration routes
	router.HandleFunc("GET /admin/config", router.WithLeadership(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/config/waiver", http.StatusSeeOther)
	}))
	router.HandleFunc("GET /admin/config/waiver", router.WithLeadership(m.renderWaiverConfigPage))
	router.HandleFunc("POST /admin/config/waiver", router.WithLeadership(m.handleWaiverConfigSave))
	router.HandleFunc("GET /admin/config/discord", router.WithLeadership(m.renderDiscordConfigPage))
	router.HandleFunc("POST /admin/config/discord", router.WithLeadership(m.handleDiscordConfigSave))
	router.HandleFunc("GET /admin/config/stripe", router.WithLeadership(m.renderStripeConfigPage))
	router.HandleFunc("POST /admin/config/stripe", router.WithLeadership(m.handleStripeConfigSave))
	router.HandleFunc("GET /admin/config/bambu", router.WithLeadership(m.renderBambuConfigPage))
	router.HandleFunc("POST /admin/config/bambu", router.WithLeadership(m.handleBambuConfigSave))
	router.HandleFunc("GET /admin/config/fobapi", router.WithLeadership(m.renderFobAPIConfigPage))

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

func (m *Module) renderWaiverConfigPage(w http.ResponseWriter, r *http.Request) {
	data := m.getWaiverConfigData(r)
	w.Header().Set("Content-Type", "text/html")
	renderConfigPage(m.nav, "Waiver", renderWaiverConfigContent(data)).Render(r.Context(), w)
}

func (m *Module) handleWaiverConfigSave(w http.ResponseWriter, r *http.Request) {
	content := r.FormValue("content")

	if content == "" {
		engine.ClientError(w, "Invalid Input", "Content is required", 400)
		return
	}

	_, err := m.db.ExecContext(r.Context(),
		"INSERT INTO waiver_content (content) VALUES ($1)",
		content)
	if engine.HandleError(w, err) {
		return
	}

	data := m.getWaiverConfigData(r)
	data.Saved = true
	w.Header().Set("Content-Type", "text/html")
	renderConfigPage(m.nav, "Waiver", renderWaiverConfigContent(data)).Render(r.Context(), w)
}

func (m *Module) getWaiverConfigData(r *http.Request) *waiverConfigData {
	row := m.db.QueryRowContext(r.Context(),
		"SELECT version, content FROM waiver_content ORDER BY version DESC LIMIT 1")

	data := &waiverConfigData{}
	err := row.Scan(&data.Version, &data.Content)
	if err != nil {
		return &waiverConfigData{Version: 1}
	}
	return data
}

func (m *Module) renderDiscordConfigPage(w http.ResponseWriter, r *http.Request) {
	data := m.getDiscordConfigData(r)
	w.Header().Set("Content-Type", "text/html")
	renderConfigPage(m.nav, "Discord", renderDiscordConfigContent(data, m.self.String())).Render(r.Context(), w)
}

func (m *Module) handleDiscordConfigSave(w http.ResponseWriter, r *http.Request) {
	clientID := r.FormValue("client_id")
	clientSecret := r.FormValue("client_secret")
	botToken := r.FormValue("bot_token")
	guildID := r.FormValue("guild_id")
	roleID := r.FormValue("role_id")
	printWebhookURL := r.FormValue("print_webhook_url")
	syncIntervalStr := r.FormValue("sync_interval_hours")

	syncIntervalHours := 24 // default
	if syncIntervalStr != "" {
		if parsed, err := strconv.Atoi(syncIntervalStr); err == nil && parsed >= 1 && parsed <= 168 {
			syncIntervalHours = parsed
		}
	}

	// Preserve existing secrets if the user didn't provide new values
	var existingClientSecret, existingBotToken, existingPrintWebhookURL string
	m.db.QueryRowContext(r.Context(),
		`SELECT client_secret, bot_token, print_webhook_url
		 FROM discord_config ORDER BY version DESC LIMIT 1`).
		Scan(&existingClientSecret, &existingBotToken, &existingPrintWebhookURL)

	if clientSecret == "" {
		clientSecret = existingClientSecret
	}
	if botToken == "" {
		botToken = existingBotToken
	}
	if printWebhookURL == "" {
		printWebhookURL = existingPrintWebhookURL
	}

	// Insert new version
	_, err := m.db.ExecContext(r.Context(),
		`INSERT INTO discord_config
		 (client_id, client_secret, bot_token, guild_id, role_id, print_webhook_url, sync_interval_hours)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		clientID, clientSecret, botToken, guildID, roleID, printWebhookURL, syncIntervalHours)
	if engine.HandleError(w, err) {
		return
	}

	data := m.getDiscordConfigData(r)
	data.Saved = true
	w.Header().Set("Content-Type", "text/html")
	renderConfigPage(m.nav, "Discord", renderDiscordConfigContent(data, m.self.String())).Render(r.Context(), w)
}

func (m *Module) getDiscordConfigData(r *http.Request) *discordConfigData {
	row := m.db.QueryRowContext(r.Context(),
		`SELECT version, client_id, client_secret != '', bot_token != '',
				guild_id, role_id, print_webhook_url != '', COALESCE(sync_interval_hours, 24)
		 FROM discord_config ORDER BY version DESC LIMIT 1`)

	data := &discordConfigData{SyncIntervalHours: 24}
	err := row.Scan(&data.Version, &data.ClientID, &data.HasClientSecret,
		&data.HasBotToken, &data.GuildID, &data.RoleID,
		&data.HasPrintWebhookURL, &data.SyncIntervalHours)
	if err != nil && err != sql.ErrNoRows {
		data.Error = "Error loading configuration: " + err.Error()
	}

	// Fetch status counts
	m.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM members WHERE discord_user_id IS NOT NULL").Scan(&data.TotalLinkedMembers)
	m.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM members WHERE discord_user_id IS NOT NULL AND discord_last_synced IS NULL").Scan(&data.PendingSyncs)

	// Fetch recent events
	data.RecentEvents = m.getRecentDiscordEvents(r.Context())

	return data
}

func (m *Module) getRecentDiscordEvents(ctx context.Context) []*discordEvent {
	rows, err := m.db.QueryContext(ctx, `
		SELECT e.created, e.event_type, COALESCE(e.entity_id, ''), e.success, e.details,
			   COALESCE(mem.name_override, mem.identifier, 'Unknown') AS member_name
		FROM module_events e
		LEFT JOIN members mem ON e.member = mem.id
		WHERE e.module = 'discord'
		ORDER BY e.created DESC
		LIMIT 20`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []*discordEvent
	for rows.Next() {
		var created int64
		var successInt int
		event := &discordEvent{}
		if rows.Scan(&created, &event.EventType, &event.DiscordUserID, &successInt, &event.Details, &event.MemberName) == nil {
			event.Created = time.Unix(created, 0)
			event.Success = successInt == 1
			events = append(events, event)
		}
	}
	return events
}

func (m *Module) renderStripeConfigPage(w http.ResponseWriter, r *http.Request) {
	data := m.getStripeConfigData(r)
	w.Header().Set("Content-Type", "text/html")
	renderConfigPage(m.nav, "Stripe", renderStripeConfigContent(data, m.self.String())).Render(r.Context(), w)
}

func (m *Module) handleStripeConfigSave(w http.ResponseWriter, r *http.Request) {
	apiKey := r.FormValue("api_key")
	webhookKey := r.FormValue("webhook_key")

	// Preserve existing secrets if the user didn't provide new values
	var existingAPIKey, existingWebhookKey string
	m.db.QueryRowContext(r.Context(),
		`SELECT api_key, webhook_key FROM stripe_config ORDER BY version DESC LIMIT 1`).
		Scan(&existingAPIKey, &existingWebhookKey)

	if apiKey == "" {
		apiKey = existingAPIKey
	}
	if webhookKey == "" {
		webhookKey = existingWebhookKey
	}

	// Insert new version
	_, err := m.db.ExecContext(r.Context(),
		`INSERT INTO stripe_config (api_key, webhook_key) VALUES ($1, $2)`,
		apiKey, webhookKey)
	if engine.HandleError(w, err) {
		return
	}

	data := m.getStripeConfigData(r)
	data.Saved = true
	w.Header().Set("Content-Type", "text/html")
	renderConfigPage(m.nav, "Stripe", renderStripeConfigContent(data, m.self.String())).Render(r.Context(), w)
}

func (m *Module) getStripeConfigData(r *http.Request) *stripeConfigData {
	row := m.db.QueryRowContext(r.Context(),
		`SELECT version, api_key != '', webhook_key != ''
		 FROM stripe_config ORDER BY version DESC LIMIT 1`)

	data := &stripeConfigData{}
	err := row.Scan(&data.Version, &data.HasAPIKey, &data.HasWebhookKey)
	if err != nil && err != sql.ErrNoRows {
		data.Error = "Error loading configuration: " + err.Error()
	}

	// Fetch status counts
	m.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM members WHERE stripe_subscription_state = 'active'").Scan(&data.ActiveSubscriptions)
	m.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM members WHERE stripe_customer_id IS NOT NULL").Scan(&data.TotalCustomers)

	// Fetch recent events
	data.RecentEvents = m.getRecentStripeEvents(r.Context())

	return data
}

func (m *Module) getRecentStripeEvents(ctx context.Context) []*stripeEvent {
	rows, err := m.db.QueryContext(ctx, `
		SELECT e.created, e.event_type, COALESCE(e.entity_id, ''), e.success, e.details,
			   COALESCE(mem.name_override, mem.identifier, 'Unknown') AS member_name
		FROM module_events e
		LEFT JOIN members mem ON e.member = mem.id
		WHERE e.module = 'stripe'
		ORDER BY e.created DESC
		LIMIT 20`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []*stripeEvent
	for rows.Next() {
		var created int64
		var successInt int
		event := &stripeEvent{}
		if rows.Scan(&created, &event.EventType, &event.StripeCustomerID, &successInt, &event.Details, &event.MemberName) == nil {
			event.Created = time.Unix(created, 0)
			event.Success = successInt == 1
			events = append(events, event)
		}
	}
	return events
}

func (m *Module) renderBambuConfigPage(w http.ResponseWriter, r *http.Request) {
	data := m.getBambuConfigData(r)
	w.Header().Set("Content-Type", "text/html")
	renderConfigPage(m.nav, "Bambu", renderBambuConfigContent(data)).Render(r.Context(), w)
}

func (m *Module) handleBambuConfigSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		data := m.getBambuConfigData(r)
		data.Error = "Failed to parse form: " + err.Error()
		w.Header().Set("Content-Type", "text/html")
		renderConfigPage(m.nav, "Bambu", renderBambuConfigContent(data)).Render(r.Context(), w)
		return
	}

	// Parse poll interval
	pollIntervalSecs := 5
	if pollIntervalStr := r.FormValue("poll_interval_seconds"); pollIntervalStr != "" {
		if parsed, err := strconv.Atoi(pollIntervalStr); err == nil && parsed >= 1 && parsed <= 60 {
			pollIntervalSecs = parsed
		}
	}

	// Load existing config to get current access codes
	existingPrinters := m.getExistingPrinters(r.Context())

	// Find all printer indices submitted
	seenIndices := make(map[int]bool)
	for key := range r.Form {
		// Match printer[N][field] pattern
		if strings.HasPrefix(key, "printer[") {
			// Extract index
			idxEnd := strings.Index(key[8:], "]")
			if idxEnd > 0 {
				if idx, err := strconv.Atoi(key[8 : 8+idxEnd]); err == nil {
					seenIndices[idx] = true
				}
			}
		}
	}

	// Sort indices to maintain order
	indices := make([]int, 0, len(seenIndices))
	for idx := range seenIndices {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	// Build printer list
	type printerJSON struct {
		Name         string `json:"name"`
		Host         string `json:"host"`
		AccessCode   string `json:"access_code"`
		SerialNumber string `json:"serial_number"`
	}
	printers := []printerJSON{}

	for _, idx := range indices {
		prefix := fmt.Sprintf("printer[%d]", idx)
		name := r.FormValue(prefix + "[name]")
		host := r.FormValue(prefix + "[host]")
		accessCode := r.FormValue(prefix + "[access_code]")
		serialNumber := r.FormValue(prefix + "[serial_number]")

		// Skip if required fields are empty
		if name == "" || host == "" || serialNumber == "" {
			continue
		}

		// Preserve existing access code if not provided
		if accessCode == "" {
			if existing, ok := existingPrinters[serialNumber]; ok {
				accessCode = existing
			}
		}

		// Skip new printers without access code
		if accessCode == "" {
			continue
		}

		printers = append(printers, printerJSON{
			Name:         name,
			Host:         host,
			AccessCode:   accessCode,
			SerialNumber: serialNumber,
		})
	}

	// Convert to JSON
	printersJSONBytes, err := json.Marshal(printers)
	if err != nil {
		data := m.getBambuConfigData(r)
		data.Error = "Failed to encode printers: " + err.Error()
		w.Header().Set("Content-Type", "text/html")
		renderConfigPage(m.nav, "Bambu", renderBambuConfigContent(data)).Render(r.Context(), w)
		return
	}

	// Insert new version
	_, err = m.db.ExecContext(r.Context(),
		`INSERT INTO bambu_config (printers_json, poll_interval_seconds) VALUES ($1, $2)`,
		string(printersJSONBytes), pollIntervalSecs)
	if engine.HandleError(w, err) {
		return
	}

	data := m.getBambuConfigData(r)
	data.Saved = true
	w.Header().Set("Content-Type", "text/html")
	renderConfigPage(m.nav, "Bambu", renderBambuConfigContent(data)).Render(r.Context(), w)
}

// getExistingPrinters returns existing printer access codes keyed by serial number
func (m *Module) getExistingPrinters(ctx context.Context) map[string]string {
	result := make(map[string]string)

	row := m.db.QueryRowContext(ctx,
		`SELECT printers_json FROM bambu_config ORDER BY version DESC LIMIT 1`)

	var printersJSON string
	if row.Scan(&printersJSON) != nil {
		return result
	}

	var printers []struct {
		AccessCode   string `json:"access_code"`
		SerialNumber string `json:"serial_number"`
	}
	if json.Unmarshal([]byte(printersJSON), &printers) == nil {
		for _, p := range printers {
			result[p.SerialNumber] = p.AccessCode
		}
	}

	return result
}

func (m *Module) getBambuConfigData(r *http.Request) *bambuConfigData {
	row := m.db.QueryRowContext(r.Context(),
		`SELECT version, printers_json, COALESCE(poll_interval_seconds, 5)
		 FROM bambu_config ORDER BY version DESC LIMIT 1`)

	var printersJSON string
	data := &bambuConfigData{PollIntervalSecs: 5, Printers: []*bambuPrinterFormData{}}
	err := row.Scan(&data.Version, &printersJSON, &data.PollIntervalSecs)
	if err != nil && err != sql.ErrNoRows {
		data.Error = "Error loading configuration: " + err.Error()
		return data
	}

	// Parse printers JSON into structured form data
	if printersJSON != "" && printersJSON != "[]" {
		var printers []struct {
			Name         string `json:"name"`
			Host         string `json:"host"`
			AccessCode   string `json:"access_code"`
			SerialNumber string `json:"serial_number"`
		}
		if json.Unmarshal([]byte(printersJSON), &printers) == nil {
			for i, p := range printers {
				data.Printers = append(data.Printers, &bambuPrinterFormData{
					Index:         i,
					Name:          p.Name,
					Host:          p.Host,
					HasAccessCode: p.AccessCode != "",
					SerialNumber:  p.SerialNumber,
				})
			}
		}
	}

	data.ConfiguredPrinters = len(data.Printers)
	data.RecentEvents = m.getRecentBambuEvents(r.Context())

	return data
}

func (m *Module) getRecentBambuEvents(ctx context.Context) []*bambuEvent {
	rows, err := m.db.QueryContext(ctx, `
		SELECT created, event_type, COALESCE(entity_name, ''), COALESCE(entity_id, ''), success, details
		FROM module_events
		WHERE module = 'bambu'
		ORDER BY created DESC
		LIMIT 20`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []*bambuEvent
	for rows.Next() {
		var created int64
		var successInt int
		event := &bambuEvent{}
		if rows.Scan(&created, &event.EventType, &event.PrinterName, &event.PrinterSerial, &successInt, &event.Details) == nil {
			event.Created = time.Unix(created, 0)
			event.Success = successInt == 1
			events = append(events, event)
		}
	}
	return events
}

func (m *Module) renderFobAPIConfigPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	renderConfigPage(m.nav, "Fob API", renderFobAPIConfigContent(m.self.String())).Render(r.Context(), w)
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
	cfg, version, err := m.configStore.Load(r.Context(), spec.Module)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	events := m.loadModuleEvents(r.Context(), spec.Module)

	w.Header().Set("Content-Type", "text/html")
	renderGenericConfigPage(m.nav, spec, cfg, version, events, m.getConfigSections(), saved, errMsg, m.self.String()).Render(r.Context(), w)
}

// loadModuleEvents loads recent events for a module.
func (m *Module) loadModuleEvents(ctx context.Context, module string) []*config.Event {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, created, module, member, event_type, COALESCE(entity_id, ''), COALESCE(entity_name, ''), success, COALESCE(details, '')
		FROM module_events
		WHERE module = $1
		ORDER BY created DESC
		LIMIT 20`, module)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []*config.Event
	for rows.Next() {
		var created int64
		var successInt int
		event := &config.Event{}
		if rows.Scan(&event.ID, &created, &event.Module, &event.MemberID, &event.EventType, &event.EntityID, &event.EntityName, &successInt, &event.Details) == nil {
			event.Created = time.Unix(created, 0)
			event.Success = successInt == 1
			events = append(events, event)
		}
	}
	return events
}

// getConfigSections returns the list of config sections for the sidebar.
// This combines the hardcoded sections with any dynamically registered modules.
func (m *Module) getConfigSections() []*configSection {
	sections := make([]*configSection, len(configSections))
	copy(sections, configSections)

	if m.configRegistry != nil {
		for _, spec := range m.configRegistry.List() {
			// Check if this module already has a hardcoded section
			exists := false
			for _, s := range sections {
				if s.Name == spec.Title {
					exists = true
					break
				}
			}
			if !exists {
				sections = append(sections, &configSection{
					Name: spec.Title,
					Path: "/admin/config/" + spec.Module,
				})
			}
		}
	}

	return sections
}
