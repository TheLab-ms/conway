package admin

import (
	"testing"

	snaptest "github.com/TheLab-ms/conway/internal/testing"
)

func TestAdminNav(t *testing.T) {
	tests := []struct {
		name        string
		tabs        []*navbarTab
		fixtureName string
		description string
	}{
		{
			name:        "empty_tabs",
			tabs:        []*navbarTab{},
			fixtureName: "_empty",
			description: "Navigation with no tabs",
		},
		{
			name: "single_tab",
			tabs: []*navbarTab{
				{Title: "Members", Path: "/admin/members"},
			},
			fixtureName: "_single",
			description: "Navigation with single tab",
		},
		{
			name: "multiple_tabs",
			tabs: []*navbarTab{
				{Title: "Members", Path: "/admin/members"},
				{Title: "Events", Path: "/admin/events"},
				{Title: "Settings", Path: "/admin/settings"},
			},
			fixtureName: "_multiple",
			description: "Navigation with multiple tabs",
		},
		{
			name: "special_chars_tabs",
			tabs: []*navbarTab{
				{Title: "M&M's Report", Path: "/admin/reports?type=candy"},
				{Title: "Café & Lounge", Path: "/admin/areas/café"},
				{Title: "R&D Projects", Path: "/admin/projects?dept=r&d"},
			},
			fixtureName: "_special_chars",
			description: "Navigation with special characters in titles and paths",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := adminNav(tt.tabs)
			snaptest.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}

func TestTableCellRender(t *testing.T) {
	tests := []struct {
		name        string
		cell        tableCell
		row         *tableRow
		fixtureName string
		description string
	}{
		{
			name: "text_cell",
			cell: tableCell{
				Text:           "Simple Text",
				BadgeType:      "",
				SelfLinkButton: "",
			},
			row: &tableRow{
				SelfLink: "/admin/items/123",
				Cells:    []*tableCell{},
			},
			fixtureName: "_text",
			description: "Basic text cell with no special formatting",
		},
		{
			name: "badge_primary",
			cell: tableCell{
				Text:           "Active",
				BadgeType:      "primary",
				SelfLinkButton: "",
			},
			row: &tableRow{
				SelfLink: "/admin/items/123",
				Cells:    []*tableCell{},
			},
			fixtureName: "_badge_primary",
			description: "Cell with primary badge",
		},
		{
			name: "badge_success",
			cell: tableCell{
				Text:           "Ready",
				BadgeType:      "success",
				SelfLinkButton: "",
			},
			row: &tableRow{
				SelfLink: "/admin/members/456",
				Cells:    []*tableCell{},
			},
			fixtureName: "_badge_success",
			description: "Cell with success badge",
		},
		{
			name: "badge_danger",
			cell: tableCell{
				Text:           "Inactive",
				BadgeType:      "danger",
				SelfLinkButton: "",
			},
			row: &tableRow{
				SelfLink: "/admin/members/789",
				Cells:    []*tableCell{},
			},
			fixtureName: "_badge_danger",
			description: "Cell with danger badge",
		},
		{
			name: "badge_warning",
			cell: tableCell{
				Text:           "Pending",
				BadgeType:      "warning",
				SelfLinkButton: "",
			},
			row: &tableRow{
				SelfLink: "/admin/requests/101",
				Cells:    []*tableCell{},
			},
			fixtureName: "_badge_warning",
			description: "Cell with warning badge",
		},
		{
			name: "self_link_button",
			cell: tableCell{
				Text:           "",
				BadgeType:      "",
				SelfLinkButton: "Edit",
			},
			row: &tableRow{
				SelfLink: "/admin/items/456",
				Cells:    []*tableCell{},
			},
			fixtureName: "_button",
			description: "Cell with edit button linking to item",
		},
		{
			name: "empty_cell",
			cell: tableCell{
				Text:           "",
				BadgeType:      "",
				SelfLinkButton: "",
			},
			row: &tableRow{
				SelfLink: "/admin/empty/1",
				Cells:    []*tableCell{},
			},
			fixtureName: "_empty",
			description: "Empty cell with default rendering",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := tt.cell.Render(tt.row)
			snaptest.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}

func TestRenderAdminList(t *testing.T) {
	tests := []struct {
		name        string
		tabs        []*navbarTab
		typeName    string
		searchURL   string
		fixtureName string
		description string
	}{
		{
			name: "members_list",
			tabs: []*navbarTab{
				{Title: "Members", Path: "/admin/members"},
				{Title: "Events", Path: "/admin/events"},
			},
			typeName:    "Members",
			searchURL:   "/admin/members/search",
			fixtureName: "_members",
			description: "Admin list for members with navigation",
		},
		{
			name: "events_list",
			tabs: []*navbarTab{
				{Title: "Members", Path: "/admin/members"},
				{Title: "Events", Path: "/admin/events"},
			},
			typeName:    "Events",
			searchURL:   "/admin/events/search",
			fixtureName: "_events",
			description: "Admin list for events",
		},
		{
			name:        "minimal_list",
			tabs:        []*navbarTab{},
			typeName:    "Items",
			searchURL:   "/search",
			fixtureName: "_minimal",
			description: "Minimal admin list with no navigation tabs",
		},
		{
			name: "complex_search_url",
			tabs: []*navbarTab{
				{Title: "Reports", Path: "/admin/reports"},
			},
			typeName:    "Transaction Reports",
			searchURL:   "/admin/reports/search?filter=transactions&period=monthly",
			fixtureName: "_complex_search",
			description: "Admin list with complex search URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderAdminList(tt.tabs, tt.typeName, tt.searchURL)
			snaptest.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}

func TestRenderAdminListElements(t *testing.T) {
	tests := []struct {
		name        string
		rowMeta     []*tableRowMeta
		rows        []*tableRow
		currentPage int64
		totalPages  int64
		fixtureName string
		description string
	}{
		{
			name:        "empty_list",
			rowMeta:     []*tableRowMeta{},
			rows:        []*tableRow{},
			currentPage: 1,
			totalPages:  1,
			fixtureName: "_empty",
			description: "Empty list with no data",
		},
		{
			name: "simple_list",
			rowMeta: []*tableRowMeta{
				{Title: "Name", Width: 6},
				{Title: "Status", Width: 3},
				{Title: "Actions", Width: 3},
			},
			rows: []*tableRow{
				{
					SelfLink: "/admin/items/1",
					Cells: []*tableCell{
						{Text: "Item One"},
						{Text: "Active", BadgeType: "success"},
						{SelfLinkButton: "Edit"},
					},
				},
				{
					SelfLink: "/admin/items/2",
					Cells: []*tableCell{
						{Text: "Item Two"},
						{Text: "Pending", BadgeType: "warning"},
						{SelfLinkButton: "Edit"},
					},
				},
			},
			currentPage: 1,
			totalPages:  1,
			fixtureName: "_simple",
			description: "Simple list with data",
		},
		{
			name: "paginated_list_first_page",
			rowMeta: []*tableRowMeta{
				{Title: "ID", Width: 2},
				{Title: "Email", Width: 8},
				{Title: "Status", Width: 2},
			},
			rows: []*tableRow{
				{
					SelfLink: "/admin/members/1",
					Cells: []*tableCell{
						{Text: "1"},
						{Text: "user1@example.com"},
						{Text: "Ready", BadgeType: "success"},
					},
				},
			},
			currentPage: 1,
			totalPages:  5,
			fixtureName: "_paginated_first",
			description: "First page of paginated list",
		},
		{
			name: "paginated_list_middle_page",
			rowMeta: []*tableRowMeta{
				{Title: "ID", Width: 2},
				{Title: "Email", Width: 8},
				{Title: "Status", Width: 2},
			},
			rows: []*tableRow{
				{
					SelfLink: "/admin/members/11",
					Cells: []*tableCell{
						{Text: "11"},
						{Text: "user11@example.com"},
						{Text: "Active", BadgeType: "primary"},
					},
				},
			},
			currentPage: 3,
			totalPages:  5,
			fixtureName: "_paginated_middle",
			description: "Middle page of paginated list",
		},
		{
			name: "paginated_list_last_page",
			rowMeta: []*tableRowMeta{
				{Title: "ID", Width: 2},
				{Title: "Email", Width: 8},
				{Title: "Status", Width: 2},
			},
			rows: []*tableRow{
				{
					SelfLink: "/admin/members/25",
					Cells: []*tableCell{
						{Text: "25"},
						{Text: "user25@example.com"},
						{Text: "Inactive", BadgeType: "danger"},
					},
				},
			},
			currentPage: 5,
			totalPages:  5,
			fixtureName: "_paginated_last",
			description: "Last page of paginated list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderAdminListElements(tt.rowMeta, tt.rows, tt.currentPage, tt.totalPages)
			snaptest.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}

func TestRenderListPagination(t *testing.T) {
	tests := []struct {
		name        string
		currentPage int64
		totalPages  int64
		fixtureName string
		description string
	}{
		{
			name:        "single_page",
			currentPage: 1,
			totalPages:  1,
			fixtureName: "_single",
			description: "Single page pagination",
		},
		{
			name:        "first_page_multiple",
			currentPage: 1,
			totalPages:  5,
			fixtureName: "_first_multiple",
			description: "First page of multiple pages",
		},
		{
			name:        "middle_page",
			currentPage: 3,
			totalPages:  5,
			fixtureName: "_middle",
			description: "Middle page of multiple pages",
		},
		{
			name:        "last_page",
			currentPage: 5,
			totalPages:  5,
			fixtureName: "_last",
			description: "Last page of multiple pages",
		},
		{
			name:        "large_pagination",
			currentPage: 50,
			totalPages:  100,
			fixtureName: "_large",
			description: "Large pagination with many pages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderListPagination(tt.currentPage, tt.totalPages)
			snaptest.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}