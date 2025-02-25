package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/julienschmidt/httprouter"
)

//go:generate templ generate

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/admin", router.WithAuth(m.onlyLeadership(m.renderAdminView)))
	router.Handle("GET", "/admin/members/:id", router.WithAuth(m.onlyLeadership(m.renderSingleMemberView)))
	router.Handle("POST", "/admin/members/:id/updates/basics", router.WithAuth(m.onlyLeadership(m.updateMemberBasics)))
	router.Handle("POST", "/admin/members/:id/updates/designations", router.WithAuth(m.onlyLeadership(m.updateMemberDesignations)))
	router.Handle("POST", "/admin/members/:id/updates/discounts", router.WithAuth(m.onlyLeadership(m.updateMemberDiscounts)))
	router.Handle("POST", "/admin/members/:id/delete", router.WithAuth(m.onlyLeadership(m.deleteMember)))

	for _, view := range listViews {
		router.Handle("GET", "/admin"+view.RelPath, router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
			return engine.Component(renderAdminList("Members", "/admin/search"+view.RelPath))
		})))

		router.Handle("POST", "/admin/search"+view.RelPath, router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
			q, args := view.BuildQuery(r)
			results, err := m.db.QueryContext(r.Context(), q, args...)
			if err != nil {
				return engine.Errorf("querying the database: %s", err)
			}
			defer results.Close()

			rows := view.BuildRows(results)
			if err := results.Err(); err != nil {
				return engine.Errorf("scanning the query results: %s", err)
			}

			return engine.Component(renderAdminListElements(view.Rows, rows))
		})))
	}
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

func (m *Module) renderSingleMemberView(r *http.Request, ps httprouter.Params) engine.Response {
	mem := member{}
	err := m.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.access_status, m.name, m.email, m.confirmed, m.created, m.fob_id, m.admin_notes, m.leadership, m.non_billable, m.stripe_subscription_id, m.stripe_subscription_state, m.paypal_subscription_id, m.paypal_last_payment, m.paypal_price, m.discount_type, rfm.email, m.bill_annually, m.fob_last_seen
		FROM members m
		LEFT JOIN members rfm ON m.root_family_member = rfm.id
		WHERE m.id = $1`, ps.ByName("id")).
		Scan(&mem.ID, &mem.AccessStatus, &mem.Name, &mem.Email, &mem.Confirmed, &mem.Created, &mem.FobID, &mem.AdminNotes, &mem.Leadership, &mem.NonBillable, &mem.StripeSubID, &mem.StripeStatus, &mem.PaypalSubID, &mem.PaypalLastPayment, &mem.PaypalPrice, &mem.DiscountType, &mem.RootFamilyEmail, &mem.BillAnnually, &mem.FobLastSeen)
	if err != nil {
		return engine.Errorf("querying the database: %s", err)
	}

	if mem.RootFamilyEmail == nil {
		mem.RootFamilyEmail = new(string)
	}
	if mem.FobID == nil {
		mem.FobID = new(int64)
	}

	var events []*memberEvent
	results, err := m.db.QueryContext(r.Context(), "SELECT created, event, details FROM member_events WHERE member = $1 ORDER BY created DESC LIMIT 10", mem.ID)
	if err != nil {
		return engine.Errorf("querying the database: %s", err)
	}
	defer results.Close()

	for results.Next() {
		var created int64
		event := &memberEvent{}
		if results.Scan(&created, &event.Event, &event.Details) == nil {
			event.Created = time.Unix(created, 0)
			events = append(events, event)
		}
	}

	return engine.Component(renderSingleMember(&mem, events))
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
