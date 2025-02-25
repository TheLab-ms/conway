// Code generated by templ - DO NOT EDIT.

// templ: version: v0.2.793
package admin

//lint:file-ignore SA4006 This context is only used if a nested component is present.

import "github.com/a-h/templ"
import templruntime "github.com/a-h/templ/runtime"

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/bootstrap"
	"strconv"
	"time"
)

const timeFormat = "Mon, Jan 2 2006"

type member struct {
	ID                int64
	AccessStatus      string
	Name              string
	Email             string
	Confirmed         bool
	Created           int64
	AdminNotes        string
	Leadership        bool
	NonBillable       bool
	FobID             *int64
	StripeSubID       *string
	StripeStatus      *string
	PaypalSubID       *string
	PaypalPrice       *float64
	PaypalLastPayment *int64
	DiscountType      *string
	RootFamilyEmail   *string
	BillAnnually      bool
	FobLastSeen       *int64
}

type memberEvent struct {
	Created time.Time
	Event   string
	Details string
}

func querySingleMember(ctx context.Context, db *sql.DB, id string) (*member, []*memberEvent, error) {
	mem := member{}
	err := db.QueryRowContext(ctx, `
		SELECT m.id, m.access_status, m.name, m.email, m.confirmed, m.created, COALESCE(m.fob_id, 0), m.admin_notes, m.leadership, m.non_billable, m.stripe_subscription_id, m.stripe_subscription_state, m.paypal_subscription_id, m.paypal_last_payment, m.paypal_price, m.discount_type, COALESCE(rfm.email, ''), m.bill_annually, m.fob_last_seen
		FROM members m
		LEFT JOIN members rfm ON m.root_family_member = rfm.id
		WHERE m.id = $1`, id).
		Scan(&mem.ID, &mem.AccessStatus, &mem.Name, &mem.Email, &mem.Confirmed, &mem.Created, &mem.FobID, &mem.AdminNotes, &mem.Leadership, &mem.NonBillable, &mem.StripeSubID, &mem.StripeStatus, &mem.PaypalSubID, &mem.PaypalLastPayment, &mem.PaypalPrice, &mem.DiscountType, &mem.RootFamilyEmail, &mem.BillAnnually, &mem.FobLastSeen)
	if err != nil {
		return nil, nil, err
	}

	var events []*memberEvent
	results, err := db.QueryContext(ctx, "SELECT created, event, details FROM member_events WHERE member = $1 ORDER BY created DESC LIMIT 10", mem.ID)
	if err != nil {
		return nil, nil, err
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

	return &mem, events, nil
}

var _ = handlePostForm(formHandler{
	Path: "/admin/members/:id/updates/basics",
	Post: &engine.PostFormHandler{
		Fields: []string{"name", "email", "confirmed", "fob_id", "admin_notes", "bill_annually"},
		Query: `UPDATE members SET name = :name, email = :email, admin_notes = :admin_notes,
			confirmed = (CASE WHEN :confirmed = 'on' THEN 1 ELSE 0 END),
			bill_annually = (CASE WHEN :bill_annually = 'on' THEN 1 ELSE 0 END),
			fob_id = (CASE WHEN CAST(:fob_id AS INTEGER) = 0 THEN NULL ELSE CAST(:fob_id AS INTEGER) END)
			WHERE id = :route_id`,
	},
})

var _ = handlePostForm(formHandler{
	Path: "/admin/members/:id/updates/designations",
	Post: &engine.PostFormHandler{
		Fields: []string{"leadership", "non_billable"},
		Query: `UPDATE members SET
			leadership = (CASE WHEN :leadership = 'on' THEN 1 ELSE 0 END),
			non_billable = (CASE WHEN :non_billable = 'on' THEN 1 ELSE 0 END)
			WHERE id = :route_id`,
	},
})

var _ = handlePostForm(formHandler{
	Path: "/admin/members/:id/updates/discounts",
	Post: &engine.PostFormHandler{
		Fields: []string{"family_email", "discount"},
		Query: `UPDATE members SET
			discount_type = (CASE WHEN :discount = '' THEN NULL ELSE :discount END),
			root_family_member = (SELECT id FROM members WHERE email = :family_email AND root_family_member IS NULL)
			WHERE id = :route_id`,
	},
})

var _ = handlePostForm(formHandler{
	Path: "/admin/members/:id/delete",
	Delete: &engine.DeleteFormHandler{
		Table:    "members",
		Redirect: "/admin/members",
	},
})

func renderSingleMember(tabs []*navbarTab, member *member, events []*memberEvent) templ.Component {
	return templruntime.GeneratedTemplate(func(templ_7745c5c3_Input templruntime.GeneratedComponentInput) (templ_7745c5c3_Err error) {
		templ_7745c5c3_W, ctx := templ_7745c5c3_Input.Writer, templ_7745c5c3_Input.Context
		if templ_7745c5c3_CtxErr := ctx.Err(); templ_7745c5c3_CtxErr != nil {
			return templ_7745c5c3_CtxErr
		}
		templ_7745c5c3_Buffer, templ_7745c5c3_IsBuffer := templruntime.GetBuffer(templ_7745c5c3_W)
		if !templ_7745c5c3_IsBuffer {
			defer func() {
				templ_7745c5c3_BufErr := templruntime.ReleaseBuffer(templ_7745c5c3_Buffer)
				if templ_7745c5c3_Err == nil {
					templ_7745c5c3_Err = templ_7745c5c3_BufErr
				}
			}()
		}
		ctx = templ.InitializeContext(ctx)
		templ_7745c5c3_Var1 := templ.GetChildren(ctx)
		if templ_7745c5c3_Var1 == nil {
			templ_7745c5c3_Var1 = templ.NopComponent
		}
		ctx = templ.ClearChildren(ctx)
		templ_7745c5c3_Var2 := templruntime.GeneratedTemplate(func(templ_7745c5c3_Input templruntime.GeneratedComponentInput) (templ_7745c5c3_Err error) {
			templ_7745c5c3_W, ctx := templ_7745c5c3_Input.Writer, templ_7745c5c3_Input.Context
			templ_7745c5c3_Buffer, templ_7745c5c3_IsBuffer := templruntime.GetBuffer(templ_7745c5c3_W)
			if !templ_7745c5c3_IsBuffer {
				defer func() {
					templ_7745c5c3_BufErr := templruntime.ReleaseBuffer(templ_7745c5c3_Buffer)
					if templ_7745c5c3_Err == nil {
						templ_7745c5c3_Err = templ_7745c5c3_BufErr
					}
				}()
			}
			ctx = templ.InitializeContext(ctx)
			templ_7745c5c3_Err = adminNav(tabs).Render(ctx, templ_7745c5c3_Buffer)
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" <div class=\"container my-5\"><div class=\"card\"><div class=\"card-body\"><h5 class=\"card-title mb-2\">Basic Info</h5><h6 class=\"card-subtitle mb-2 text-muted\">Account created on ")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var3 string
			templ_7745c5c3_Var3, templ_7745c5c3_Err = templ.JoinStringErrs(time.Unix(member.Created, 0).Format(timeFormat))
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 123, Col: 115}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var3))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</h6>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.FobLastSeen != nil {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<h6 class=\"card-subtitle mb-2 text-muted\">Last fob'd in ")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				var templ_7745c5c3_Var4 string
				templ_7745c5c3_Var4, templ_7745c5c3_Err = templ.JoinStringErrs(formatLastFobSwipe(time.Unix(*member.FobLastSeen, 0)))
				if templ_7745c5c3_Err != nil {
					return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 125, Col: 117}
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var4))
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</h6>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			if member.AccessStatus != "Ready" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<span class=\"badge text-bg-danger mb-3\">")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				var templ_7745c5c3_Var5 string
				templ_7745c5c3_Var5, templ_7745c5c3_Err = templ.JoinStringErrs(member.AccessStatus)
				if templ_7745c5c3_Err != nil {
					return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 128, Col: 67}
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var5))
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</span>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<form method=\"post\" action=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var6 templ.SafeURL = templ.URL(fmt.Sprintf("/admin/members/%d/updates/basics", member.ID))
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var6)))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\" id=\"basic-info-form\"><div class=\"form-floating mb-2\"><input type=\"text\" class=\"form-control\" id=\"name\" name=\"name\" value=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var7 string
			templ_7745c5c3_Var7, templ_7745c5c3_Err = templ.JoinStringErrs(member.Name)
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 132, Col: 88}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var7))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"> <label for=\"name\" class=\"form-label\">Name</label></div><div class=\"input-group mb-2\"><div class=\"form-floating\"><input type=\"email\" class=\"form-control\" id=\"email\" name=\"email\" value=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var8 string
			templ_7745c5c3_Var8, templ_7745c5c3_Err = templ.JoinStringErrs(member.Email)
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 137, Col: 93}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var8))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"> <label for=\"email\">Email</label></div><div class=\"input-group-text\"><label class=\"form-check-label\" for=\"confirmed\" style=\"margin-right: 5px;\">Confirmed</label> <input type=\"checkbox\" class=\"form-check-input mt-0\" id=\"confirmed\" name=\"confirmed\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.Confirmed {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" checked")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("></div></div><div class=\"form-floating mb-2\"><input type=\"number\" class=\"form-control\" id=\"fob_id\" name=\"fob_id\" value=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var9 string
			templ_7745c5c3_Var9, templ_7745c5c3_Err = templ.JoinStringErrs(strconv.FormatInt(*member.FobID, 10))
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 146, Col: 119}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var9))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"> <label for=\"fob_id\" class=\"form-label\">Keyfob ID</label></div><div class=\"input-group mb-2\"><input type=\"checkbox\" class=\"form-check-input\" id=\"bill_annually\" name=\"bill_annually\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.BillAnnually {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" checked")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("> <label class=\"form-check-label\" for=\"bill_annually\" style=\"margin-left: 5px;\">Bill Annually</label></div><div class=\"form-floating mb-2\"><textarea class=\"form-control\" id=\"admin_notes\" name=\"admin_notes\" style=\"height: 100px\">")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var10 string
			templ_7745c5c3_Var10, templ_7745c5c3_Err = templ.JoinStringErrs(member.AdminNotes)
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 154, Col: 115}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var10))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</textarea> <label for=\"admin_notes\" class=\"form-label\">Admin Notes</label></div><button type=\"submit\" class=\"btn btn-primary\">Save</button> <button type=\"button\" class=\"btn btn-secondary\" onclick=\"document.getElementById(&#39;delete-account-link&#39;).style.display=&#39;block&#39;\">Delete</button></form><form method=\"post\" action=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var11 templ.SafeURL = templ.URL(fmt.Sprintf("/admin/members/%d/delete", member.ID))
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var11)))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"><button type=\"submit\" class=\"btn btn-danger mt-3\" id=\"delete-account-link\" style=\"display:none;\">Confirm Delete</button></form></div></div><div class=\"card mt-3\"><div class=\"card-body\"><h5 class=\"card-title mb-2\">Discounts</h5><form method=\"post\" action=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var12 templ.SafeURL = templ.URL(fmt.Sprintf("/admin/members/%d/updates/discounts", member.ID))
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var12)))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"><script>\n\t\t\t\t\t\t\tdocument.addEventListener('DOMContentLoaded', (event) => {\n\t\t\t\t\t\t\t\tconst handler = () => {\n\t\t\t\t\t\t\t\t\tlet selector = document.getElementById('discount')\n\t\t\t\t\t\t\t\t\tlet field = document.getElementById('family-email-field')\n\t\t\t\t\t\t\t\t\tif (selector.value === 'family') {\n\t\t\t\t\t\t\t\t\t\tfield.style.display = 'block'\n\t\t\t\t\t\t\t\t\t} else {\n\t\t\t\t\t\t\t\t\t\tfield.style.display = 'none'\n\t\t\t\t\t\t\t\t\t\tdocument.getElementById('family_email').value = ''\n\t\t\t\t\t\t\t\t\t}\n\t\t\t\t\t\t\t\t}\n\n\t\t\t\t\t\t\t\tdocument.getElementById('discount').addEventListener('change', event => {\n\t\t\t\t\t\t\t\t\thandler()\n\t\t\t\t\t\t\t\t})\n\t\t\t\t\t\t\t\thandler()\n\t\t\t\t\t\t\t})\n\t\t\t\t\t\t</script><select class=\"form-select\" id=\"discount\" name=\"discount\"><option value=\"\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.DiscountType == nil || *member.DiscountType == "" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" selected")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(">None</option> <option value=\"military\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.DiscountType != nil && *member.DiscountType == "military" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" selected")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(">Military</option> <option value=\"retired\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.DiscountType != nil && *member.DiscountType == "retired" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" selected")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(">Retired</option> <option value=\"unemployed\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.DiscountType != nil && *member.DiscountType == "unemployed" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" selected")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(">Unemployed</option> <option value=\"firstResponder\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.DiscountType != nil && *member.DiscountType == "firstResponder" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" selected")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(">First Responder</option> <option value=\"educator\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.DiscountType != nil && *member.DiscountType == "educator" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" selected")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(">Educator</option> <option value=\"emeritus\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.DiscountType != nil && *member.DiscountType == "emeritus" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" selected")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(">Emeritus</option> <option value=\"family\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.DiscountType != nil && *member.DiscountType == "family" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" selected")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(">Family</option></select><div class=\"mb-2 mt-2\" id=\"family-email-field\" style=\"display:none;\"><label for=\"family_email\" class=\"form-label\">Root Family Account Email Address</label> <input type=\"email\" class=\"form-control\" id=\"family_email\" name=\"family_email\" value=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var13 string
			templ_7745c5c3_Var13, templ_7745c5c3_Err = templ.JoinStringErrs(*member.RootFamilyEmail)
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 200, Col: 117}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var13))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"></div><p class=\"card-subtitle mt-2 text-muted\">The discounted rate is only applied during Stripe checkout. Applying a new discount will not modify existing memberships until the member re-subscribes.</p><button type=\"submit\" class=\"btn btn-primary mt-2\">Save</button></form></div></div><div class=\"card mt-3\"><div class=\"card-body\"><h5 class=\"card-title\">Stripe ")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.StripeStatus == nil || *member.StripeStatus != "active" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<span class=\"badge text-bg-danger\">!</span>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</h5><h6 class=\"card-subtitle mb-2 text-muted\">Subscription status:  <b>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.StripeStatus == nil || *member.StripeStatus == "" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("unknown")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			} else {
				var templ_7745c5c3_Var14 string
				templ_7745c5c3_Var14, templ_7745c5c3_Err = templ.JoinStringErrs(*member.StripeStatus)
				if templ_7745c5c3_Err != nil {
					return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 223, Col: 30}
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var14))
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</b></h6>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.StripeSubID == nil || *member.StripeSubID == "" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<a href=\"#\" class=\"btn btn-secondary disabled\" aria-disabled=\"true\">View subscription in Stripe</a>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			} else {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<a href=\"")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				var templ_7745c5c3_Var15 templ.SafeURL = templ.URL("https://dashboard.stripe.com/subscriptions/" + *member.StripeSubID)
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var15)))
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\" target=\"_blank\" class=\"btn btn-primary\">View subscription in Stripe</a>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</div></div>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.PaypalSubID != nil && *member.PaypalSubID != "" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<div class=\"card mt-3\"><div class=\"card-body\"><h5 class=\"card-title\">Paypal</h5>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				if member.PaypalLastPayment != nil {
					_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<h6 class=\"card-subtitle mb-2 text-muted\">Last payment processed: <b>")
					if templ_7745c5c3_Err != nil {
						return templ_7745c5c3_Err
					}
					var templ_7745c5c3_Var16 string
					templ_7745c5c3_Var16, templ_7745c5c3_Err = templ.JoinStringErrs(time.Unix(*member.PaypalLastPayment, 0).Format(timeFormat))
					if templ_7745c5c3_Err != nil {
						return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 241, Col: 71}
					}
					_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var16))
					if templ_7745c5c3_Err != nil {
						return templ_7745c5c3_Err
					}
					_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</b></h6>")
					if templ_7745c5c3_Err != nil {
						return templ_7745c5c3_Err
					}
				}
				if member.PaypalPrice != nil {
					_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<h6 class=\"card-subtitle mb-2 text-muted\">Subscription price: <b>")
					if templ_7745c5c3_Err != nil {
						return templ_7745c5c3_Err
					}
					var templ_7745c5c3_Var17 string
					templ_7745c5c3_Var17, templ_7745c5c3_Err = templ.JoinStringErrs(fmt.Sprintf("%.2f", *member.PaypalPrice))
					if templ_7745c5c3_Err != nil {
						return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 247, Col: 53}
					}
					_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var17))
					if templ_7745c5c3_Err != nil {
						return templ_7745c5c3_Err
					}
					_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</b></h6>")
					if templ_7745c5c3_Err != nil {
						return templ_7745c5c3_Err
					}
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</div></div>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<div class=\"card mt-3\"><div class=\"card-body\"><h5 class=\"card-title mb-2\">Designations</h5><form method=\"post\" action=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var18 templ.SafeURL = templ.URL(fmt.Sprintf("/admin/members/%d/updates/designations", member.ID))
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var18)))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"><div class=\"form-check\"><input type=\"checkbox\" class=\"form-check-input\" id=\"leadership\" name=\"leadership\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.Leadership {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" checked")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("> <label class=\"form-check-label\" for=\"leadership\">Leadership <i>(Conway admin access)</i></label></div><div class=\"form-check\"><input type=\"checkbox\" class=\"form-check-input\" id=\"non-billable\" name=\"non_billable\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.NonBillable {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" checked")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("> <label class=\"form-check-label\" for=\"non-billable\">Non-billable <i>(landlord fobs, lifetime members, etc.)</i></label></div><button type=\"submit\" class=\"btn btn-primary mt-2\">Save</button></form></div></div><div class=\"card mt-3\"><div class=\"card-body\"><h5 class=\"card-title mb-2\">Events</h5>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if len(events) > 9 {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<h6 class=\"card-subtitle mb-2 text-muted\">Only the 10 most recent events are shown</h6>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<table class=\"table\"><thead><tr><th scope=\"col\">Time</th><th scope=\"col\">Event</th><th scope=\"col\">Details</th></tr></thead> <tbody>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			for _, event := range events {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<tr><td>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				var templ_7745c5c3_Var19 string
				templ_7745c5c3_Var19, templ_7745c5c3_Err = templ.JoinStringErrs(event.Created.Format(time.RFC3339))
				if templ_7745c5c3_Err != nil {
					return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 286, Col: 49}
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var19))
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</td><td><strong>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				var templ_7745c5c3_Var20 string
				templ_7745c5c3_Var20, templ_7745c5c3_Err = templ.JoinStringErrs(event.Event)
				if templ_7745c5c3_Err != nil {
					return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 287, Col: 34}
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var20))
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</strong></td><td>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				var templ_7745c5c3_Var21 string
				templ_7745c5c3_Var21, templ_7745c5c3_Err = templ.JoinStringErrs(event.Details)
				if templ_7745c5c3_Err != nil {
					return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 288, Col: 28}
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var21))
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</td></tr>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</tbody></table></div></div></div>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			return templ_7745c5c3_Err
		})
		templ_7745c5c3_Err = bootstrap.View().Render(templ.WithChildren(ctx, templ_7745c5c3_Var2), templ_7745c5c3_Buffer)
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		return templ_7745c5c3_Err
	})
}

func formatLastFobSwipe(ts time.Time) string {
	dur := time.Since(ts)

	const day = time.Hour * 24
	switch {
	case dur > day*30:
		return "on " + ts.Format(timeFormat)
	case dur > day:
		return fmt.Sprintf("%d days ago", int(dur/day))
	default:
		return "within the last day"
	}
}

var _ = templruntime.GeneratedTemplate
