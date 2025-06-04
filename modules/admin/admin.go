package admin

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/TheLab-ms/conway/modules/bootstrap"
)

//go:embed templates/*
var templateFS embed.FS

var (
	adminNavTemplate        *template.Template
	tableCellTemplate       *template.Template
	adminListTemplate       *template.Template
	adminListElementsTemplate *template.Template
	listPaginationTemplate  *template.Template
)

func init() {
	var err error
	adminNavTemplate, err = template.ParseFS(templateFS, "templates/admin_nav.html")
	if err != nil {
		panic(err)
	}
	tableCellTemplate, err = template.ParseFS(templateFS, "templates/table_cell.html")
	if err != nil {
		panic(err)
	}
	adminListTemplate, err = template.ParseFS(templateFS, "templates/admin_list.html")
	if err != nil {
		panic(err)
	}
	adminListElementsTemplate, err = template.ParseFS(templateFS, "templates/admin_list_elements.html")
	if err != nil {
		panic(err)
	}
	listPaginationTemplate, err = template.ParseFS(templateFS, "templates/list_pagination.html")
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
	ID              int64               `db:"id"`
	AccessStatus    string              `db:"access_status"`
	Name            string              `db:"name"`
	Email           string              `db:"email"`
	Confirmed       bool                `db:"confirmed"`
	Created         engine.LocalTime    `db:"created"`
	AdminNotes      string              `db:"admin_notes"`
	Leadership      bool                `db:"leadership"`
	NonBillable     bool                `db:"non_billable"`
	FobID           *int64              `db:"fob_id"`
	StripeSubID     *string             `db:"stripe_subscription_id"`
	StripeStatus    *string             `db:"stripe_subscription_state"`
	PaypalSubID     *string             `db:"paypal_subscription_id"`
	PaypalPrice     *float64            `db:"paypal_price"`
	DiscountType    *string             `db:"discount_type"`
	RootFamilyEmail *string             `db:"email"` // This maps to the joined email from root family member
	BillAnnually    bool                `db:"bill_annually"`
	FobLastSeen     *engine.LocalTime   `db:"fob_last_seen"`
	DiscordUserID   string              `db:"discord_user_id"`
}

type memberEvent struct {
	Created time.Time `db:"created"`
	Event   string    `db:"event"`
	Details string    `db:"details"`
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
	// Use the new helper to render admin nav
	navHTML, err := templates.RenderToHTML(adminNav(tabs))
	if err != nil {
		panic(err)
	}

	data := AdminListData{
		AdminNav:  navHTML,
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
	// Render each cell using the helper
	enhancedRows := make([]*EnhancedTableRow, len(rows))
	for i, row := range rows {
		enhancedCells := make([]*EnhancedTableCell, len(row.Cells))
		for j, cell := range row.Cells {
			cellHTML, err := templates.RenderToHTML(cell.Render(row))
			if err != nil {
				panic(err)
			}
			enhancedCells[j] = &EnhancedTableCell{
				CellContent: cellHTML,
			}
		}
		enhancedRows[i] = &EnhancedTableRow{
			SelfLink: row.SelfLink,
			Cells:    enhancedCells,
		}
	}

	// Render pagination using the helper
	paginationHTML, err := templates.RenderToHTML(renderListPagination(currentPage, totalPages))
	if err != nil {
		panic(err)
	}

	data := AdminListElementsData{
		RowMeta:    rowMeta,
		Rows:       enhancedRows,
		Pagination: paginationHTML,
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
	dbHelper := templates.NewDBQueryHelper(db)
	
	// Query single member with complex JOIN - we'll still need manual handling for this
	// because of the complex column mapping and COALESCE operations
	mem := member{}
	query := `
		SELECT m.id, m.access_status, m.name, m.email, m.confirmed, m.created, COALESCE(m.fob_id, 0), m.admin_notes, m.leadership, m.non_billable, m.stripe_subscription_id, m.stripe_subscription_state, m.paypal_subscription_id, m.paypal_price, m.discount_type, COALESCE(rfm.email, ''), m.bill_annually, m.fob_last_seen, COALESCE(m.discord_user_id, '')
		FROM members m
		LEFT JOIN members rfm ON m.root_family_member = rfm.id
		WHERE m.id = $1`
	
	err := db.QueryRowContext(ctx, query, id).
		Scan(&mem.ID, &mem.AccessStatus, &mem.Name, &mem.Email, &mem.Confirmed, &mem.Created, &mem.FobID, &mem.AdminNotes, &mem.Leadership, &mem.NonBillable, &mem.StripeSubID, &mem.StripeStatus, &mem.PaypalSubID, &mem.PaypalPrice, &mem.DiscountType, &mem.RootFamilyEmail, &mem.BillAnnually, &mem.FobLastSeen, &mem.DiscordUserID)
	if err != nil {
		return nil, nil, err
	}

	// Query member events using the generic helper
	var events []*memberEvent
	eventsQuery := "SELECT created, event, details FROM member_events WHERE member = $1 ORDER BY created DESC LIMIT 10"
	if err := dbHelper.QueryRows(ctx, eventsQuery, &events, mem.ID); err != nil {
		return nil, nil, err
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