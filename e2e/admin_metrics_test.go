package e2e

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdmin_MetricsDashboard(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	// Create some metrics data
	seedMetrics(t, "active_members", 10)
	seedMetrics(t, "daily_visitors", 10)

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	metricsPage := NewAdminMetricsPage(t, page)
	metricsPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Should show the metrics page with interval selector
	expect(t).Locator(page.Locator("#interval")).ToBeVisible()
}

func TestAdmin_MetricsChartRendering(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	seedMetrics(t, "test_series", 10)

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	metricsPage := NewAdminMetricsPage(t, page)
	metricsPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Charts should be visible (they're canvas elements with data-series attribute)
	metricsPage.ExpectChartForSeries("test_series")
}

func TestAdmin_MetricsTimeWindow(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	seedMetrics(t, "window_test", 10)

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	metricsPage := NewAdminMetricsPage(t, page)
	metricsPage.Navigate()

	// Select a different time window
	metricsPage.SelectInterval("720h") // 30 days

	// Page should reload with new interval
	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify the interval is selected
	expect(t).Locator(page.Locator("#interval option[selected][value='720h']")).ToBeVisible()
}

func TestAdmin_MetricsChartAPI(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	seedMetrics(t, "api_test_series", 5)

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	// Directly call the chart API endpoint
	resp, err := page.Goto(baseURL + "/admin/chart?series=api_test_series&window=168h")
	require.NoError(t, err)

	assert.Equal(t, 200, resp.Status())

	// Parse the JSON response
	body, err := resp.Body()
	require.NoError(t, err)

	var data []struct {
		Timestamp int64   `json:"t"`
		Value     float64 `json:"v"`
	}
	err = json.Unmarshal(body, &data)
	require.NoError(t, err)

	assert.NotEmpty(t, data, "should have metric data points")
}

func TestAdmin_MetricsRequiresLeadership(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "regular@example.com",
		WithConfirmed(),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	resp, err := page.Goto(baseURL + "/admin/metrics")
	require.NoError(t, err)

	assert.Equal(t, 403, resp.Status())
}
