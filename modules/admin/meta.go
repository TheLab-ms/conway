package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"

	"github.com/TheLab-ms/conway/engine"
)

type listView struct {
	Title      string
	RelPath    string
	Rows       []*tableRowMeta
	BuildQuery func(*http.Request) (query, rowCountQuery string, args []any)
	BuildRows  func(*sql.Rows) ([]*tableRow, error)
}

var listViews = []listView{
	{
		Title:   "Members",
		RelPath: "/members",
		Rows: []*tableRowMeta{
			{Title: "Name", Width: 2},
			{Title: "Fob Status", Width: 1},
			{Title: "Payment Status", Width: 1},
			{Title: "Actions", Width: 1},
		},
		BuildQuery: func(r *http.Request) (q, rowCountQuery string, args []any) {
			q = "SELECT id, identifier, COALESCE(payment_status, 'Inactive') AS payment_status, access_status FROM members"
			rowCountQuery = "SELECT COUNT(*) FROM members"

			search := r.PostFormValue("search")
			if search != "" {
				logic := " WHERE name LIKE '%' || $1 || '%' OR email LIKE '%' || $1 || '%' OR CAST(fob_id AS TEXT) LIKE '%' || $1 || '%'"
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

				accessCell := &tableCell{Text: accessStatus, BadgeType: "secondary"}
				if accessCell.Text != "Ready" {
					accessCell.BadgeType = "warning"
				}

				paymentCell := &tableCell{Text: paymentStatus, BadgeType: "secondary"}
				if paymentCell.Text == "Inactive" {
					paymentCell.BadgeType = "warning"
				}

				rows = append(rows, &tableRow{
					SelfLink: fmt.Sprintf("/admin/members/%d", id),
					Cells: []*tableCell{
						{Text: name},
						accessCell,
						paymentCell,
						{SelfLinkButton: "Edit"},
					},
				})
			}

			return rows, nil
		},
	},
	{
		Title:   "Fob Swipes",
		RelPath: "/fobs",
		Rows: []*tableRowMeta{
			{Title: "Timestamp", Width: 1},
			{Title: "Member", Width: 2},
			{Title: "Fob ID", Width: 1},
		},
		BuildQuery: func(r *http.Request) (q, rowCountQuery string, args []any) {
			q = `SELECT f.timestamp, f.member, m.identifier AS member, f.fob_id 
				 FROM fob_swipes f 
				 LEFT JOIN members m ON f.member = m.id 
				 ORDER BY f.timestamp DESC 
				 LIMIT :limit OFFSET :offset`
			rowCountQuery = "SELECT COUNT(*) FROM fob_swipes"
			return
		},
		BuildRows: func(results *sql.Rows) ([]*tableRow, error) {
			rows := []*tableRow{}
			for results.Next() {
				var timestamp engine.LocalTime
				var memberID *int64
				var member *string
				var fobID int64
				err := results.Scan(&timestamp, &memberID, &member, &fobID)
				if err != nil {
					return nil, err
				}

				if member == nil {
					val := "Unknown"
					member = &val
				}

				row := &tableRow{
					Cells: []*tableCell{
						{Text: timestamp.Time.Format("2006-01-02 03:04:05 PM")},
						{Text: *member},
						{Text: strconv.FormatInt(fobID, 10)},
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
	{
		Title:   "Events",
		RelPath: "/events",
		Rows: []*tableRowMeta{
			{Title: "Timestamp", Width: 1},
			{Title: "Member", Width: 1},
			{Title: "Event", Width: 1},
			{Title: "Details", Width: 2},
		},
		BuildQuery: func(r *http.Request) (q, rowCountQuery string, args []any) {
			q = `SELECT f.created, f.member, m.identifier AS member, f.event, f.details
				 FROM member_events f
				 LEFT JOIN members m ON f.member = m.id
				 ORDER BY f.created DESC
				 LIMIT :limit OFFSET :offset`
			rowCountQuery = "SELECT COUNT(*) FROM member_events"
			return
		},
		BuildRows: func(results *sql.Rows) ([]*tableRow, error) {
			rows := []*tableRow{}
			for results.Next() {
				var timestamp engine.LocalTime
				var memberID *int64
				var member *string
				var event string
				var details string
				err := results.Scan(&timestamp, &memberID, &member, &event, &details)
				if err != nil {
					return nil, err
				}

				memberCell := &tableCell{}
				if member != nil {
					memberCell.Text = *member
				} else {
					memberCell.Text = "Unknown"
				}

				row := &tableRow{
					Cells: []*tableCell{
						{Text: timestamp.Time.Format("2006-01-02 03:04:05 PM")},
						memberCell,
						{Text: event, BadgeType: "secondary"},
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
	Path   string
	Post   *engine.PostFormHandler
	Delete *engine.DeleteFormHandler
}

func (f *formHandler) BuildHandler(db *sql.DB) engine.Handler {
	if f.Post != nil {
		return f.Post.Handler(db)
	}
	return f.Delete.Handler(db)
}

var formHandlers = []*formHandler{}

func handlePostForm(fh formHandler) formHandler {
	formHandlers = append(formHandlers, &fh)
	return fh
}
