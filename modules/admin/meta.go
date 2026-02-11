package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/TheLab-ms/conway/engine"
)

// TODO: Support search for pages other than members

type listView struct {
	Title       string
	RelPath     string
	Searchable  bool
	ExportTable string   // If set, shows a CSV export link for this table
	NewItemURL  string   // If set, shows a "New" button that posts to this URL
	FilterParam string   // The form parameter name for filters (e.g., "event_type", "access_status")
	Filters     []string // If set, shows a filter dropdown with these options
	Rows        []*tableRowMeta
	BuildQuery  func(*http.Request) (query, rowCountQuery string, args []any)
	BuildRows   func(*sql.Rows) ([]*tableRow, error)
}

var listViews = []listView{
	{
		Title:       "Members",
		RelPath:     "/members",
		Searchable:  true,
		ExportTable: "members",
		NewItemURL:  "/admin/members/new",
		FilterParam: "access_status",
		Filters: []string{
			"Ready",
			"UnconfirmedEmail",
			"MissingWaiver",
			"PaymentInactive",
			"MissingKeyFob",
			"FamilyInactive",
		},
		Rows: []*tableRowMeta{
			{Title: "Member", Width: 5},
		},
		BuildQuery: func(r *http.Request) (q, rowCountQuery string, args []any) {
			q = "SELECT id, COALESCE(name_override, identifier) AS identifier, COALESCE(payment_status, 'Inactive') AS payment_status, access_status FROM members"
			rowCountQuery = "SELECT COUNT(*) FROM members"

			// Parse filter values from form
			r.ParseForm()
			filters := r.Form["access_status"]

			search := r.FormValue("search")

			// Build WHERE clause based on search and filters
			whereClauses := []string{}
			if search != "" {
				whereClauses = append(whereClauses, "(name LIKE '%' || $1 || '%' OR name_override LIKE '%' || $1 || '%' OR email LIKE '%' || $1 || '%' OR CAST(fob_id AS TEXT) LIKE '%' || $1 || '%' OR discount_type LIKE '%' || $1 || '%')")
				args = append(args, search)
			}

			if len(filters) > 0 {
				placeholders := make([]string, len(filters))
				for i, f := range filters {
					placeholders[i] = fmt.Sprintf("$%d", len(args)+1)
					args = append(args, f)
				}
				whereClauses = append(whereClauses, "access_status IN ("+strings.Join(placeholders, ", ")+")")
			}

			if len(whereClauses) > 0 {
				whereClause := " WHERE " + strings.Join(whereClauses, " AND ")
				q += whereClause
				rowCountQuery += whereClause
			}

			if search == "" {
				q += " ORDER BY created DESC"
			} else {
				q += " ORDER BY identifier ASC"
			}

			q += " LIMIT :limit OFFSET :offset"
			return
		},
		BuildRows: func(results *sql.Rows) ([]*tableRow, error) {
			rows := []*tableRow{}
			for results.Next() {
				var id int64
				var name string
				var paymentStatus, accessStatus string
				err := results.Scan(&id, &name, &paymentStatus, &accessStatus)
				if err != nil {
					return nil, err
				}

				accessBadgeType := "secondary"
				if accessStatus != "Ready" {
					accessBadgeType = "warning"
				}

				rows = append(rows, &tableRow{
					SelfLink: fmt.Sprintf("/admin/members/%d", id),
					Cells: []*tableCell{
						{
							Text:            name,
							InlineBadge:     accessStatus,
							InlineBadgeType: accessBadgeType,
						},
					},
				})
			}

			return rows, nil
		},
	},
	{
		Title:       "Events",
		RelPath:     "/events",
		FilterParam: "event_type",
		Filters: []string{
			"Fob Swipe",
			"Waiver",
			"EmailConfirmed",
			"DiscountTypeModified",
			"AccessStatusChanged",
			"LeadershipStatusAdded",
			"LeadershipStatusRemoved",
			"NonBillableStatusAdded",
			"NonBillableStatusRemoved",
			"FobChanged",
			"WaiverSigned",
		},
		Rows: []*tableRowMeta{
			{Title: "Timestamp", Width: 1},
			{Title: "Type", Width: 1},
			{Title: "Member", Width: 2},
			{Title: "Details", Width: 2},
		},
		BuildQuery: func(r *http.Request) (q, rowCountQuery string, args []any) {
			// Parse filter values from form
			r.ParseForm()
			filters := r.Form["event_type"]

			baseQuery := `SELECT timestamp, event_type, member_id, member_name, details FROM (
				SELECT
					f.timestamp AS timestamp,
					'Fob Swipe' AS event_type,
					f.member AS member_id,
					COALESCE(m.name_override, m.identifier, 'Unknown') AS member_name,
					CAST(f.fob_id AS TEXT) AS details
				FROM fob_swipes f
				LEFT JOIN members m ON f.member = m.id

				UNION ALL

				SELECT
					e.created AS timestamp,
					e.event AS event_type,
					e.member AS member_id,
					COALESCE(m.name_override, m.identifier, 'Unknown') AS member_name,
					e.details AS details
				FROM member_events e
				LEFT JOIN members m ON e.member = m.id

				UNION ALL

				SELECT
					w.created AS timestamp,
					'Waiver' AS event_type,
					NULL AS member_id,
					w.name AS member_name,
					w.email AS details
				FROM waivers w
				WHERE w.name != ''
			) AS unified_events`

			if len(filters) > 0 {
				// Build WHERE clause with placeholders
				placeholders := make([]string, len(filters))
				for i, f := range filters {
					placeholders[i] = fmt.Sprintf("$%d", i+1)
					args = append(args, f)
				}
				baseQuery += " WHERE event_type IN (" + strings.Join(placeholders, ", ") + ")"
			}

			q = baseQuery + " ORDER BY timestamp DESC LIMIT :limit OFFSET :offset"

			// Build row count query
			if len(filters) > 0 {
				// Count with filters - use same UNION but add WHERE
				countPlaceholders := make([]string, len(filters))
				for i := range filters {
					countPlaceholders[i] = fmt.Sprintf("$%d", i+1)
				}
				rowCountQuery = `SELECT COUNT(*) FROM (
					SELECT 'Fob Swipe' AS event_type FROM fob_swipes
					UNION ALL
					SELECT event AS event_type FROM member_events
					UNION ALL
					SELECT 'Waiver' AS event_type FROM waivers WHERE name != ''
				) AS unified_events WHERE event_type IN (` + strings.Join(countPlaceholders, ", ") + ")"
			} else {
				rowCountQuery = `SELECT
					(SELECT COUNT(*) FROM fob_swipes) +
					(SELECT COUNT(*) FROM member_events) +
					(SELECT COUNT(*) FROM waivers WHERE name != '')`
			}
			return
		},
		BuildRows: func(results *sql.Rows) ([]*tableRow, error) {
			rows := []*tableRow{}
			for results.Next() {
				var timestamp engine.LocalTime
				var eventType string
				var memberID *int64
				var memberName string
				var details string
				err := results.Scan(&timestamp, &eventType, &memberID, &memberName, &details)
				if err != nil {
					return nil, err
				}

				badgeType := "secondary"
				switch eventType {
				case "Fob Swipe":
					badgeType = "info"
				case "Waiver":
					badgeType = "success"
				}

				row := &tableRow{
					Cells: []*tableCell{
						{Text: timestamp.Time.Format("2006-01-02 03:04:05 PM")},
						{Text: eventType, BadgeType: badgeType},
						{Text: memberName},
						{Text: details},
					},
				}
				if memberID != nil {
					row.SelfLink = fmt.Sprintf("/admin/members/%d", *memberID)
				}
				rows = append(rows, row)
			}

			return rows, nil
		},
	},
}

type formHandler struct {
	Path    string
	Handler *engine.FormHandler
}

func (f *formHandler) BuildHandler(db *sql.DB) http.HandlerFunc {
	return f.Handler.Handler(db)
}

var formHandlers = []*formHandler{}

func handlePostForm(fh formHandler) formHandler {
	formHandlers = append(formHandlers, &fh)
	return fh
}
