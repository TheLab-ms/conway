package admin

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
)

// TODO: Support search for pages other than members

type listView struct {
	Title      string
	RelPath    string
	Searchable bool
	Rows       []*tableRowMeta
	BuildQuery func(*http.Request) (query, rowCountQuery string, args []any)
	BuildRows  func(*sql.Rows) ([]*tableRow, error)
}

var listViews = []listView{
	{
		Title:      "Members",
		RelPath:    "/members",
		Searchable: true,
		Rows: []*tableRowMeta{
			{Title: "Member", Width: 5},
		},
		BuildQuery: func(r *http.Request) (q, rowCountQuery string, args []any) {
			q = "SELECT id, COALESCE(name_override, identifier) AS identifier, COALESCE(payment_status, 'Inactive') AS payment_status, access_status FROM members"
			rowCountQuery = "SELECT COUNT(*) FROM members"

			search := r.PostFormValue("search")
			if search != "" {
				logic := " WHERE name LIKE '%' || $1 || '%' OR name_override LIKE '%' || $1 || '%' OR email LIKE '%' || $1 || '%' OR CAST(fob_id AS TEXT) LIKE '%' || $1 || '%' OR discount_type LIKE '%' || $1 || '%'"
				q += logic
				rowCountQuery += logic
				args = append(args, search)
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
		Title:   "Events",
		RelPath: "/events",
		Rows: []*tableRowMeta{
			{Title: "Timestamp", Width: 1},
			{Title: "Type", Width: 1},
			{Title: "Member", Width: 2},
			{Title: "Details", Width: 2},
		},
		BuildQuery: func(r *http.Request) (q, rowCountQuery string, args []any) {
			q = `SELECT timestamp, event_type, member_id, member_name, details FROM (
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
			) AS unified_events
			ORDER BY timestamp DESC
			LIMIT :limit OFFSET :offset`
			rowCountQuery = `SELECT
				(SELECT COUNT(*) FROM fob_swipes) +
				(SELECT COUNT(*) FROM member_events) +
				(SELECT COUNT(*) FROM waivers WHERE name != '')`
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
