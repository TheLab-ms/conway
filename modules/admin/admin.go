package admin

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/TheLab-ms/conway/modules/bootstrap"
)

var (
	adminNavTemplate        *template.Template
	tableCellTemplate       *template.Template
	adminListTemplate       *template.Template
	adminListElementsTemplate *template.Template
	listPaginationTemplate  *template.Template
)

func init() {
	var err error
	adminNavTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/admin/templates/admin_nav.html")
	if err != nil {
		panic(err)
	}
	tableCellTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/admin/templates/table_cell.html")
	if err != nil {
		panic(err)
	}
	adminListTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/admin/templates/admin_list.html")
	if err != nil {
		panic(err)
	}
	adminListElementsTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/admin/templates/admin_list_elements.html")
	if err != nil {
		panic(err)
	}
	listPaginationTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/admin/templates/list_pagination.html")
	if err != nil {
		panic(err)
	}
}

type navbarTab struct {
	Title string
	Path  string
}

type tableRowMeta struct {
	Title string
	Width int
}

type tableRow struct {
	SelfLink string // i.e. /things/this-one's-id
	Cells    []*tableCell
}

type tableCell struct {
	Text           string
	BadgeType      string
	SelfLinkButton string
}

// Types from member.templ
const timeFormat = "Mon, Jan 2 2006"

type member struct {
	ID              int64
	AccessStatus    string
	Name            string
	Email           string
	Confirmed       bool
	Created         engine.LocalTime
	AdminNotes      string
	Leadership      bool
	NonBillable     bool
	FobID           *int64
	StripeSubID     *string
	StripeStatus    *string
	PaypalSubID     *string
	PaypalPrice     *float64
	DiscountType    *string
	RootFamilyEmail *string
	BillAnnually    bool
	FobLastSeen     *engine.LocalTime
	DiscordUserID   string
}

type memberEvent struct {
	Created time.Time
	Event   string
	Details string
}

type TableCellData struct {
	Text           string
	BadgeType      string
	SelfLinkButton string
	SelfLink       string
}

type AdminListData struct {
	AdminNav  template.HTML
	TypeName  string
	SearchURL string
}

type AdminListElementsData struct {
	RowMeta    []*tableRowMeta
	Rows       []*EnhancedTableRow
	Pagination template.HTML
}

type EnhancedTableRow struct {
	SelfLink string
	Cells    []*EnhancedTableCell
}

type EnhancedTableCell struct {
	CellContent template.HTML
}

type PaginationData struct {
	CurrentPage int64
	TotalPages  int64
}

func adminNav(tabs []*navbarTab) templates.Component {
	return &templates.TemplateComponent{
		Template: adminNavTemplate,
		Data:     tabs,
	}
}

func (t tableCell) Render(row *tableRow) templates.Component {
	data := TableCellData{
		Text:           t.Text,
		BadgeType:      t.BadgeType,
		SelfLinkButton: t.SelfLinkButton,
		SelfLink:       row.SelfLink,
	}
	return &templates.TemplateComponent{
		Template: tableCellTemplate,
		Data:     data,
	}
}

func renderAdminList(tabs []*navbarTab, typeName, searchURL string) templates.Component {
	// Render the admin nav
	var navBuf bytes.Buffer
	navComponent := adminNav(tabs)
	if err := navComponent.Render(nil, &navBuf); err != nil {
		panic(err)
	}

	data := AdminListData{
		AdminNav:  template.HTML(navBuf.String()),
		TypeName:  typeName,
		SearchURL: searchURL,
	}

	adminListContent := &templates.TemplateComponent{
		Template: adminListTemplate,
		Data:     data,
	}

	return bootstrap.View(adminListContent)
}

func renderAdminListElements(rowMeta []*tableRowMeta, rows []*tableRow, currentPage, totalPages int64) templates.Component {
	// Render each cell
	enhancedRows := make([]*EnhancedTableRow, len(rows))
	for i, row := range rows {
		enhancedCells := make([]*EnhancedTableCell, len(row.Cells))
		for j, cell := range row.Cells {
			var cellBuf bytes.Buffer
			cellComponent := cell.Render(row)
			if err := cellComponent.Render(nil, &cellBuf); err != nil {
				panic(err)
			}
			enhancedCells[j] = &EnhancedTableCell{
				CellContent: template.HTML(cellBuf.String()),
			}
		}
		enhancedRows[i] = &EnhancedTableRow{
			SelfLink: row.SelfLink,
			Cells:    enhancedCells,
		}
	}

	// Render pagination
	var paginationBuf bytes.Buffer
	paginationComponent := renderListPagination(currentPage, totalPages)
	if err := paginationComponent.Render(nil, &paginationBuf); err != nil {
		panic(err)
	}

	data := AdminListElementsData{
		RowMeta:    rowMeta,
		Rows:       enhancedRows,
		Pagination: template.HTML(paginationBuf.String()),
	}

	return &templates.TemplateComponent{
		Template: adminListElementsTemplate,
		Data:     data,
	}
}

func renderListPagination(currentPage, totalPages int64) templates.Component {
	data := PaginationData{
		CurrentPage: currentPage,
		TotalPages:  totalPages,
	}

	return &templates.TemplateComponent{
		Template: listPaginationTemplate,
		Data:     data,
	}
}

// TODO: Convert the complex single member template
func renderSingleMember(tabs []*navbarTab, member interface{}, events interface{}) templates.Component {
	return &templates.TemplateComponent{
		Template: template.Must(template.New("stub").Parse("<div>Single member view not yet converted</div>")),
		Data:     nil,
	}
}

func querySingleMember(ctx context.Context, db *sql.DB, id string) (*member, []*memberEvent, error) {
	mem := member{}
	err := db.QueryRowContext(ctx, `
		SELECT m.id, m.access_status, m.name, m.email, m.confirmed, m.created, COALESCE(m.fob_id, 0), m.admin_notes, m.leadership, m.non_billable, m.stripe_subscription_id, m.stripe_subscription_state, m.paypal_subscription_id, m.paypal_price, m.discount_type, COALESCE(rfm.email, ''), m.bill_annually, m.fob_last_seen, COALESCE(m.discord_user_id, '')
		FROM members m
		LEFT JOIN members rfm ON m.root_family_member = rfm.id
		WHERE m.id = $1`, id).
		Scan(&mem.ID, &mem.AccessStatus, &mem.Name, &mem.Email, &mem.Confirmed, &mem.Created, &mem.FobID, &mem.AdminNotes, &mem.Leadership, &mem.NonBillable, &mem.StripeSubID, &mem.StripeStatus, &mem.PaypalSubID, &mem.PaypalPrice, &mem.DiscountType, &mem.RootFamilyEmail, &mem.BillAnnually, &mem.FobLastSeen, &mem.DiscordUserID)
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