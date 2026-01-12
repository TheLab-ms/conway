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
			renderAdminList(m.nav, view.Title, "/admin/search"+view.RelPath, view.ExportTable, view.Searchable).Render(r.Context(), w)
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
	router.HandleFunc("GET /admin/discord-chart", router.WithLeadership(m.renderDiscordEventsChart))
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
	router.HandleFunc("GET /admin/stripe-chart", router.WithLeadership(m.renderStripeEventsChart))

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

func (m *Module) renderDiscordEventsChart(w http.ResponseWriter, r *http.Request) {
	series := r.URL.Query().Get("series")

	windowDuration := time.Hour * 24
	if window := r.URL.Query().Get("window"); window != "" {
		if parsed, err := time.ParseDuration(window); err == nil && parsed <= 24*time.Hour {
			windowDuration = parsed
		}
	}

	var whereClause string
	switch series {
	case "sync-successes":
		whereClause = "success = 1 AND event_type IN ('RoleSync', 'OAuthCallback', 'WebhookSent')"
	case "sync-errors":
		whereClause = "success = 0"
	case "api-requests":
		whereClause = "1=1"
	default:
		engine.ClientError(w, "Invalid Request", "Unknown series", 400)
		return
	}

	query := fmt.Sprintf(`
		SELECT (created / 900) * 900 AS bucket, COUNT(*)
		FROM discord_events
		WHERE created > unixepoch() - ? AND %s
		GROUP BY bucket
		ORDER BY bucket ASC`, whereClause)

	rows, err := m.db.QueryContext(r.Context(), query, int64(windowDuration.Seconds()))
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
		var ts int64
		var count float64
		if engine.HandleError(w, rows.Scan(&ts, &count)) {
			return
		}
		data = append(data, dataPoint{Timestamp: ts, Value: count})
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
		SELECT e.created, e.event_type, COALESCE(e.discord_user_id, ''), e.success, e.details,
			   COALESCE(mem.name_override, mem.identifier, 'Unknown') AS member_name
		FROM discord_events e
		LEFT JOIN members mem ON e.member = mem.id
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
		SELECT e.created, e.event_type, COALESCE(e.stripe_customer_id, ''), e.success, e.details,
			   COALESCE(mem.name_override, mem.identifier, 'Unknown') AS member_name
		FROM stripe_events e
		LEFT JOIN members mem ON e.member = mem.id
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

func (m *Module) renderStripeEventsChart(w http.ResponseWriter, r *http.Request) {
	series := r.URL.Query().Get("series")

	windowDuration := time.Hour * 24
	if window := r.URL.Query().Get("window"); window != "" {
		if parsed, err := time.ParseDuration(window); err == nil && parsed <= 24*time.Hour {
			windowDuration = parsed
		}
	}

	var whereClause string
	switch series {
	case "successes":
		whereClause = "success = 1"
	case "errors":
		whereClause = "success = 0"
	case "api-requests":
		whereClause = "1=1"
	default:
		engine.ClientError(w, "Invalid Request", "Unknown series", 400)
		return
	}

	query := fmt.Sprintf(`
		SELECT (created / 900) * 900 AS bucket, COUNT(*)
		FROM stripe_events
		WHERE created > unixepoch() - ? AND %s
		GROUP BY bucket
		ORDER BY bucket ASC`, whereClause)

	rows, err := m.db.QueryContext(r.Context(), query, int64(windowDuration.Seconds()))
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
		var ts int64
		var count float64
		if engine.HandleError(w, rows.Scan(&ts, &count)) {
			return
		}
		data = append(data, dataPoint{Timestamp: ts, Value: count})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
