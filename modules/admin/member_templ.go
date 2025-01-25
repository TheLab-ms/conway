// Code generated by templ - DO NOT EDIT.

// templ: version: v0.2.793
package admin

//lint:file-ignore SA4006 This context is only used if a nested component is present.

import "github.com/a-h/templ"
import templruntime "github.com/a-h/templ/runtime"

import (
	"fmt"
	"github.com/TheLab-ms/conway/modules/bootstrap"
	"strconv"
	"time"
)

const timeFormat = "Mon, Jan 2 2006 at 15:04 MST"

type member struct {
	ID                     int64
	Name                   string
	Email                  string
	Confirmed              bool
	Created                int64
	AdminNotes             string
	Leadership             bool
	NonBillable            bool
	FobID                  *int64
	StripeSubID            *string
	StripeStatus           *string
	PaypalSubID            *string
	PaypalPrice            *float64
	PaypalLastPayment      *int64
	DiscountType           *string
	RootFamilyEmail        *string
	BuildingAccessApprover *string
}

func renderSingleMember(member *member) templ.Component {
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
			templ_7745c5c3_Err = adminNav().Render(ctx, templ_7745c5c3_Buffer)
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" <div class=\"container my-5\"><div class=\"card\"><div class=\"card-body\"><h5 class=\"card-title\">Basic Info</h5><h6 class=\"card-subtitle mb-3 text-muted\">Account created on ")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var3 string
			templ_7745c5c3_Var3, templ_7745c5c3_Err = templ.JoinStringErrs(time.Unix(member.Created, 0).Format(timeFormat))
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 39, Col: 115}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var3))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</h6><form method=\"post\" action=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var4 templ.SafeURL = templ.URL(fmt.Sprintf("/admin/members/%d/updates/basics", member.ID))
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var4)))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\" id=\"basic-info-form\"><div class=\"form-floating mb-3\"><input type=\"text\" class=\"form-control\" id=\"name\" name=\"name\" value=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var5 string
			templ_7745c5c3_Var5, templ_7745c5c3_Err = templ.JoinStringErrs(member.Name)
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 42, Col: 88}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var5))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"> <label for=\"name\" class=\"form-label\">Name</label></div><div class=\"input-group mb-3\"><div class=\"form-floating\"><input type=\"email\" class=\"form-control\" id=\"email\" name=\"email\" value=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var6 string
			templ_7745c5c3_Var6, templ_7745c5c3_Err = templ.JoinStringErrs(member.Email)
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 47, Col: 93}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var6))
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
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("></div></div><div class=\"form-floating mb-3\"><input type=\"number\" class=\"form-control\" id=\"fob_id\" name=\"fob_id\" value=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var7 string
			templ_7745c5c3_Var7, templ_7745c5c3_Err = templ.JoinStringErrs(strconv.FormatInt(*member.FobID, 10))
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 56, Col: 119}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var7))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"> <label for=\"fob_id\" class=\"form-label\">Keyfob ID</label></div><div class=\"form-floating mb-3\"><textarea class=\"form-control\" id=\"admin_notes\" name=\"admin_notes\" style=\"height: 100px\">")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var8 string
			templ_7745c5c3_Var8, templ_7745c5c3_Err = templ.JoinStringErrs(member.AdminNotes)
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 60, Col: 115}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var8))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</textarea> <label for=\"admin_notes\" class=\"form-label\">Admin Notes</label></div><button type=\"submit\" class=\"btn btn-primary\">Save</button> <button type=\"button\" class=\"btn btn-secondary\" onclick=\"document.getElementById(&#39;delete-account-link&#39;).style.display=&#39;block&#39;\">Delete</button></form><form method=\"post\" action=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var9 templ.SafeURL = templ.URL(fmt.Sprintf("/admin/members/%d/delete", member.ID))
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var9)))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"><button type=\"submit\" class=\"btn btn-danger mt-3\" id=\"delete-account-link\" style=\"display:none;\">Confirm Delete</button></form></div></div><div class=\"card mt-3\"><div class=\"card-body\"><h5 class=\"card-title mb-4\">Building Access ")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.BuildingAccessApprover == nil || *member.BuildingAccessApprover == "" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<span class=\"badge text-bg-danger\">!</span>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</h5>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.BuildingAccessApprover == nil || *member.BuildingAccessApprover == "" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<form method=\"post\" action=\"")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				var templ_7745c5c3_Var10 templ.SafeURL = templ.URL(fmt.Sprintf("/admin/members/%d/updates/building_access", member.ID))
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var10)))
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"><input type=\"hidden\" value=\"true\" name=\"approved\"><h6 class=\"card-subtitle mb-3 text-muted\">Not Approved</h6><button type=\"submit\" class=\"btn btn-primary\">Approve</button></form>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			} else {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<h6 class=\"card-subtitle mb-3 text-muted\">Approved By: <strong>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				var templ_7745c5c3_Var11 string
				templ_7745c5c3_Var11, templ_7745c5c3_Err = templ.JoinStringErrs(*member.BuildingAccessApprover)
				if templ_7745c5c3_Err != nil {
					return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 86, Col: 101}
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var11))
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</strong></h6><form method=\"post\" action=\"")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				var templ_7745c5c3_Var12 templ.SafeURL = templ.URL(fmt.Sprintf("/admin/members/%d/updates/building_access", member.ID))
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var12)))
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"><input type=\"hidden\" value=\"false\" name=\"approved\"> <button type=\"submit\" class=\"btn btn-danger\">Revoke</button></form>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</div></div><div class=\"card mt-3\"><div class=\"card-body\"><h5 class=\"card-title mb-4\">Discounts</h5><form method=\"post\" action=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var13 templ.SafeURL = templ.URL(fmt.Sprintf("/admin/members/%d/updates/discounts", member.ID))
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var13)))
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
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(">Family</option></select><div class=\"mb-3 mt-3\" id=\"family-email-field\" style=\"display:none;\"><label for=\"family_email\" class=\"form-label\">Root Family Account Email Address</label> <input type=\"email\" class=\"form-control\" id=\"family_email\" name=\"family_email\" value=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var14 string
			templ_7745c5c3_Var14, templ_7745c5c3_Err = templ.JoinStringErrs(*member.RootFamilyEmail)
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 129, Col: 117}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var14))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("\"></div><p class=\"card-subtitle mt-4 text-muted\">The discounted rate is only applied during Stripe checkout. Applying a new discount will not modify existing memberships until the member re-subscribes.</p><button type=\"submit\" class=\"btn btn-primary mt-4\">Save</button></form></div></div><div class=\"card mt-3\"><div class=\"card-body\"><h5 class=\"card-title\">Stripe ")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.StripeStatus == nil || *member.StripeStatus != "active" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<span class=\"badge text-bg-danger\">!</span>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</h5><h6 class=\"card-subtitle mb-3 text-muted\">Subscription status:  <b>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.StripeStatus == nil || *member.StripeStatus == "" {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("unknown")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			} else {
				var templ_7745c5c3_Var15 string
				templ_7745c5c3_Var15, templ_7745c5c3_Err = templ.JoinStringErrs(*member.StripeStatus)
				if templ_7745c5c3_Err != nil {
					return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 152, Col: 30}
				}
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var15))
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
				var templ_7745c5c3_Var16 templ.SafeURL = templ.URL("https://dashboard.stripe.com/subscriptions/" + *member.StripeSubID)
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var16)))
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
					_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<h6 class=\"card-subtitle mb-3 text-muted\">Last payment processed: <b>")
					if templ_7745c5c3_Err != nil {
						return templ_7745c5c3_Err
					}
					var templ_7745c5c3_Var17 string
					templ_7745c5c3_Var17, templ_7745c5c3_Err = templ.JoinStringErrs(time.Unix(*member.PaypalLastPayment, 0).Format(timeFormat))
					if templ_7745c5c3_Err != nil {
						return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 170, Col: 71}
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
				if member.PaypalPrice != nil {
					_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<h6 class=\"card-subtitle mb-3 text-muted\">Subscription price: <b>")
					if templ_7745c5c3_Err != nil {
						return templ_7745c5c3_Err
					}
					var templ_7745c5c3_Var18 string
					templ_7745c5c3_Var18, templ_7745c5c3_Err = templ.JoinStringErrs(fmt.Sprintf("%.2f", *member.PaypalPrice))
					if templ_7745c5c3_Err != nil {
						return templ.Error{Err: templ_7745c5c3_Err, FileName: `member.templ`, Line: 176, Col: 53}
					}
					_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var18))
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
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<div class=\"card mt-3\"><div class=\"card-body\"><h5 class=\"card-title mb-4\">Designations</h5><form method=\"post\" action=\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var19 templ.SafeURL = templ.URL(fmt.Sprintf("/admin/members/%d/updates/designations", member.ID))
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(string(templ_7745c5c3_Var19)))
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
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("> <label class=\"form-check-label\" for=\"leadership\">Leadership <i>(Conway admin access)</i></label></div><div class=\"form-check\"><input type=\"checkbox\" class=\"form-check-input\" id=\"non-billable\" name=\"non-billable\"")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if member.NonBillable {
				_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(" checked")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("> <label class=\"form-check-label\" for=\"non-billable\">Non-billable <i>(landlord fobs, lifetime members, etc.)</i></label></div><button type=\"submit\" class=\"btn btn-primary mt-4\">Save</button></form></div></div></div>")
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

var _ = templruntime.GeneratedTemplate
