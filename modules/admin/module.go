package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/julienschmidt/httprouter"
)

// TODO: Snapshot tests

//go:generate templ generate

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/admin", router.WithAuth(m.onlyLeadership(m.renderAdminView)))
	router.Handle("GET", "/admin/members", router.WithAuth(m.onlyLeadership(m.renderMembersListView)))
	router.Handle("POST", "/admin/search/members", router.WithAuth(m.onlyLeadership(m.renderMembersSearchElements)))
	router.Handle("GET", "/admin/members/:id", router.WithAuth(m.onlyLeadership(m.renderSingleMemberView)))
	router.Handle("POST", "/admin/members/:id/updates/basics", router.WithAuth(m.onlyLeadership(m.updateMemberBasics)))
	router.Handle("POST", "/admin/members/:id/updates/designations", router.WithAuth(m.onlyLeadership(m.updateMemberDesignations)))
	router.Handle("POST", "/admin/members/:id/updates/discounts", router.WithAuth(m.onlyLeadership(m.updateMemberDiscounts)))
	router.Handle("POST", "/admin/members/:id/delete", router.WithAuth(m.onlyLeadership(m.deleteMember)))
}

func (m *Module) onlyLeadership(next engine.Handler) engine.Handler {
	return func(r *http.Request, ps httprouter.Params) engine.Response {
		if meta := auth.GetUserMeta(r.Context()); meta == nil || !meta.Leadership {
			return engine.ClientErrorf("You must be a member of leadership to access this page")
		}
		return next(r, ps)
	}
}

func (m *Module) renderAdminView(r *http.Request, ps httprouter.Params) engine.Response {
	// TODO: return engine.Component(renderAdmin())
	return engine.Redirect("/admin/members", http.StatusSeeOther)
}

func (m *Module) renderMembersListView(r *http.Request, ps httprouter.Params) engine.Response {
	sorts := []*sort{
		newSort(r, "Sort by creation date", "date"),
		newSort(r, "Sort by name", "name"),
	}
	return engine.Component(renderAdminList("Members", "/admin/search/members", sorts))
}

func (m *Module) renderMembersSearchElements(r *http.Request, ps httprouter.Params) engine.Response {
	q := "SELECT id, identifier, COALESCE(payment_status, 'Inactive') AS payment_status, access_status FROM members"

	search := r.PostFormValue("search")
	if search != "" {
		q += " WHERE name LIKE '%' || $1 || '%' OR email LIKE '%' || $1 || '%'"
	}

	switch r.URL.Query().Get("sort") {
	case "", "date":
		q += " ORDER BY created DESC"
	case "name":
		q += " ORDER BY name ASC"
	}

	rowMeta := []*tableRowMeta{
		{Title: "Name", Width: 2},
		{Title: "Fob Status", Width: 1},
		{Title: "Payment Status", Width: 1},
	}

	results, err := m.db.QueryContext(r.Context(), q, search)
	if err != nil {
		return engine.Errorf("querying the database: %s", err)
	}
	defer results.Close()

	rows := membersListToRows(results)
	if err := results.Err(); err != nil {
		return engine.Errorf("scanning the query results: %s", err)
	}

	return engine.Component(renderAdminListElements(rowMeta, rows))
}

func membersListToRows(results *sql.Rows) []*tableRow {
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
}

func (m *Module) renderSingleMemberView(r *http.Request, ps httprouter.Params) engine.Response {
	mem := member{}
	err := m.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.access_status, m.name, m.email, m.confirmed, m.created, m.fob_id, m.admin_notes, m.leadership, m.non_billable, m.stripe_subscription_id, m.stripe_subscription_state, m.paypal_subscription_id, m.paypal_last_payment, m.paypal_price, m.discount_type, rfm.email, m.bill_annually
		FROM members m
		LEFT JOIN members rfm ON m.root_family_member = rfm.id
		WHERE m.id = $1`, ps.ByName("id")).
		Scan(&mem.ID, &mem.AccessStatus, &mem.Name, &mem.Email, &mem.Confirmed, &mem.Created, &mem.FobID, &mem.AdminNotes, &mem.Leadership, &mem.NonBillable, &mem.StripeSubID, &mem.StripeStatus, &mem.PaypalSubID, &mem.PaypalLastPayment, &mem.PaypalPrice, &mem.DiscountType, &mem.RootFamilyEmail, &mem.BillAnnually)
	if err != nil {
		return engine.Errorf("querying the database: %s", err)
	}

	if mem.RootFamilyEmail == nil {
		mem.RootFamilyEmail = new(string)
	}
	if mem.FobID == nil {
		mem.FobID = new(int64)
	}

	return engine.Component(renderSingleMember(&mem))
}

func (m *Module) updateMemberBasics(r *http.Request, ps httprouter.Params) engine.Response {
	id := ps.ByName("id")
	name := r.FormValue("name")
	email := r.FormValue("email")
	adminNotes := r.FormValue("admin_notes")
	billAnnually := r.FormValue("bill_annually") == "on"

	fobIDInt, _ := strconv.ParseInt(r.FormValue("fob_id"), 10, 64)
	var fobID *int64
	if fobIDInt > 0 {
		fobID = &fobIDInt
	}

	_, err := m.db.ExecContext(r.Context(), "UPDATE members SET name = $1, email = $2,  fob_id = $3, admin_notes = $4, bill_annually = $5 WHERE id = $6", name, email, fobID, adminNotes, billAnnually, id)
	if err != nil {
		return engine.Errorf("updating member: %s", err)
	}

	return engine.Redirect(fmt.Sprintf("/admin/members/%s", id), http.StatusSeeOther)
}

func (m *Module) updateMemberDesignations(r *http.Request, ps httprouter.Params) engine.Response {
	id := ps.ByName("id")
	leadership := r.FormValue("leadership") == "on"
	nonBillable := r.FormValue("non-billable") == "on"

	_, err := m.db.ExecContext(r.Context(), "UPDATE members SET leadership = $1, non_billable = $2 WHERE id = $3", leadership, nonBillable, id)
	if err != nil {
		return engine.Errorf("updating member: %s", err)
	}

	return engine.Redirect(fmt.Sprintf("/admin/members/%s", id), http.StatusSeeOther)
}

func (m *Module) updateMemberDiscounts(r *http.Request, ps httprouter.Params) engine.Response {
	id := ps.ByName("id")
	rootEmail := r.FormValue("family_email")
	discountTypeStr := r.FormValue("discount")

	var discountType *string
	if discountTypeStr != "" && !strings.EqualFold(discountTypeStr, "none") {
		discountType = &discountTypeStr
	}

	_, err := m.db.ExecContext(r.Context(), "UPDATE members SET discount_type = $1, root_family_member = (SELECT id FROM members WHERE email = $2 AND root_family_member IS NULL) WHERE id = $3", discountType, rootEmail, id)
	if err != nil {
		return engine.Errorf("updating member discounts: %s", err)
	}

	return engine.Redirect(fmt.Sprintf("/admin/members/%s", id), http.StatusSeeOther)
}

func (m *Module) deleteMember(r *http.Request, ps httprouter.Params) engine.Response {
	id := ps.ByName("id")

	_, err := m.db.ExecContext(r.Context(), "DELETE FROM members WHERE id = $1", id)
	if err != nil {
		return engine.Errorf("deleting member: %s", err)
	}

	return engine.Redirect("/admin/members", http.StatusSeeOther)
}
