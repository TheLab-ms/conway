package e2e

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdmin_MetricsDashboard(t *testing.T) {
	_, page := setupAdminTest(t)
	seedMetrics(t, "test_series", 10)

	metricsPage := NewAdminMetricsPage(t, page)
	metricsPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	t.Run("interval_selector", func(t *testing.T) {
		expect(t).Locator(page.Locator("#interval")).ToBeVisible()
	})

	t.Run("chart_rendering", func(t *testing.T) {
		metricsPage.ExpectChartForSeries("test_series")
	})

	t.Run("time_window_selection", func(t *testing.T) {
		metricsPage.SelectInterval("720h")
		err := page.WaitForLoadState()
		require.NoError(t, err)
		expect(t).Locator(page.Locator("#interval option[selected][value='720h']")).ToBeVisible()
	})

	t.Run("chart_api", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/admin/chart?series=test_series&window=168h")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())

		body, err := resp.Body()
		require.NoError(t, err)

		var data []struct {
			Timestamp int64   `json:"t"`
			Value     float64 `json:"v"`
		}
		err = json.Unmarshal(body, &data)
		require.NoError(t, err)
		assert.NotEmpty(t, data, "should have metric data points")
	})
}

func TestAdmin_MetricsRequiresLeadership(t *testing.T) {
	_, page := setupMemberTest(t, "regular@example.com", WithConfirmed())

	resp, err := page.Goto(baseURL + "/admin/metrics")
	require.NoError(t, err)

	assert.Equal(t, 403, resp.Status())
}
