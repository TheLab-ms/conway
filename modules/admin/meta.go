package admin

import (
	"database/sql"
	"fmt"
	"net/http"
)

type listView struct {
	RelPath    string
	Rows       []*tableRowMeta
	BuildQuery func(*http.Request) (query string, args []any)
	BuildRows  func(*sql.Rows) []*tableRow
}

var listViews = []listView{
	{
		RelPath: "/members",
		Rows: []*tableRowMeta{
			{Title: "Name", Width: 2},
			{Title: "Fob Status", Width: 1},
			{Title: "Payment Status", Width: 1},
		},
		BuildQuery: func(r *http.Request) (string, []any) {
			q := "SELECT id, identifier, COALESCE(payment_status, 'Inactive') AS payment_status, access_status FROM members"

			search := r.PostFormValue("search")
			if search != "" {
				q += " WHERE name LIKE '%' || $1 || '%' OR email LIKE '%' || $1 || '%' OR CAST(fob_id AS TEXT) LIKE '%' || $1 || '%'"
			}

			if search == "" {
				q += " ORDER BY created DESC"
			} else {
				q += " ORDER BY identifier ASC"
			}

			return q, []any{search}
		},
		BuildRows: func(results *sql.Rows) []*tableRow {
			rows := []*tableRow{}
			for results.Next() {
				var id int64
				var name string
				var paymentStatus, accessStatus string
				results.Scan(&id, &name, &paymentStatus, &accessStatus)

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
					},
				})
			}

			return rows
		},
	},
}
