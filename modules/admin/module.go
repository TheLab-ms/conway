package admin

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/julienschmidt/httprouter"
)

// TODO: Move the waiver status to a separate table (view?)

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
	router.Handle("POST", "/admin/members/:id/updates/building_access", router.WithAuth(m.onlyLeadership(m.updateMemberBuildingAccess)))
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
	return engine.Component(renderAdmin())
}

func (m *Module) renderMembersListView(r *http.Request, ps httprouter.Params) engine.Response {
	sorts := []*sort{
		newSort(r, "Sort by creation date", "date"),
		newSort(r, "Sort by name", "name"),
	}
	return engine.Component(renderAdminList("Members", "/admin/search/members", sorts))
}

func (m *Module) renderMembersSearchElements(r *http.Request, ps httprouter.Params) engine.Response {
	q := "SELECT id, name, email, building_access_approver, waiver_signed, fob_id, stripe_subscription_state, paypal_subscription_id, non_billable FROM members"

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
		{Title: "Email", Width: 2},
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
		var email string
		var accessApprover *string
		var waiverSigned *bool
		var fobID *int64
		var stripeState *string
		var paypalSub *string
		var nonBillable bool
		results.Scan(&id, &name, &email, &accessApprover, &waiverSigned, &fobID, &stripeState, &paypalSub, &nonBillable)

		fobCell := &tableCell{Text: "Ready", BadgeType: "success"}
		if fobID == nil {
			fobCell.Text = "Missing Fob ID"
			fobCell.BadgeType = "warning"
		}
		if waiverSigned == nil || !*waiverSigned {
			fobCell.Text = "Missing Waiver"
			fobCell.BadgeType = "warning"
		}
		if accessApprover == nil {
			fobCell.Text = "Access Not Approved"
			fobCell.BadgeType = "warning"
		}

		paymentCell := &tableCell{Text: "Inactive", BadgeType: "warning"}
		if stripeState != nil {
			switch *stripeState {
			case "inactive":
				// Leave the default
			case "incomplete":
				paymentCell.Text = "Incomplete (Stripe)"
				paymentCell.BadgeType = "warning"
			case "past_due":
				paymentCell.Text = "Past Due (Stripe)"
				paymentCell.BadgeType = "warning"
			case "canceled":
				paymentCell.Text = "Canceled (Stripe)"
				paymentCell.BadgeType = "warning"
			case "unpaid":
				paymentCell.Text = "Unpaid (Stripe)"
				paymentCell.BadgeType = "warning"
			case "paused":
				paymentCell.Text = "Paused (Stripe)"
				paymentCell.BadgeType = "warning"
			default:
				paymentCell.Text = "Active (Stripe)"
				paymentCell.BadgeType = "success"
			}
		}
		if paypalSub != nil && *paypalSub != "" {
			paymentCell.Text = "Active (Paypal)"
			paymentCell.BadgeType = "success"
		}
		if nonBillable {
			paymentCell.Text = "Active (Non-Billable)"
			paymentCell.BadgeType = "info"
		}

		rows = append(rows, &tableRow{
			SelfLink: fmt.Sprintf("/admin/members/%d", id),
			Cells: []*tableCell{
				{Text: name},
				{Text: email},
				fobCell,
				paymentCell,
			},
		})
	}

	return rows
}

func (m *Module) renderSingleMemberView(r *http.Request, ps httprouter.Params) engine.Response {
	mem := member{}
	err := m.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.name, m.email, m.created, m.admin_notes, m.leadership, m.non_billable, m.stripe_subscription_id, m.stripe_subscription_state, m.paypal_subscription_id, m.paypal_last_payment, m.paypal_price, m.discount_type, m.root_family_email, ba.identifier
		FROM members m
		LEFT JOIN members ba ON m.building_access_approver = ba.id
		WHERE m.id = $1`, ps.ByName("id")).
		Scan(&mem.ID, &mem.Name, &mem.Email, &mem.Created, &mem.AdminNotes, &mem.Leadership, &mem.NonBillable, &mem.StripeSubID, &mem.StripeStatus, &mem.PaypalSubID, &mem.PaypalLastPayment, &mem.PaypalPrice, &mem.DiscountType, &mem.RootFamilyEmail, &mem.BuildingAccessApprover)
	if err != nil {
		return engine.Errorf("querying the database: %s", err)
	}

	if mem.RootFamilyEmail == nil {
		mem.RootFamilyEmail = new(string)
	}

	return engine.Component(renderSingleMember(&mem))
}

func (m *Module) updateMemberBasics(r *http.Request, ps httprouter.Params) engine.Response {
	id := ps.ByName("id")
	name := r.FormValue("name")
	email := r.FormValue("email")
	adminNotes := r.FormValue("admin_notes")

	_, err := m.db.ExecContext(r.Context(), "UPDATE members SET name = $1, email = $2, admin_notes = $3 WHERE id = $4", name, email, adminNotes, id)
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
	discountType := r.FormValue("discount")
	rootEmail := r.FormValue("family_email")

	_, err := m.db.ExecContext(r.Context(), "UPDATE members SET discount_type = $1, root_family_email = $2 WHERE id = $3", discountType, rootEmail, id)
	if err != nil {
		return engine.Errorf("updating member discounts: %s", err)
	}

	return engine.Redirect(fmt.Sprintf("/admin/members/%s", id), http.StatusSeeOther)
}

func (m *Module) updateMemberBuildingAccess(r *http.Request, ps httprouter.Params) engine.Response {
	id := ps.ByName("id")
	approved := r.FormValue("approved") == "true"

	var err error
	if approved {
		_, err = m.db.ExecContext(r.Context(), "UPDATE members SET building_access_approver = $1 WHERE id = $2", auth.GetUserMeta(r.Context()).ID, id)
	} else {
		_, err = m.db.ExecContext(r.Context(), "UPDATE members SET building_access_approver = NULL WHERE id = $1", id)
	}
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
