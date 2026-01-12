package e2e

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

// LoginPage represents the login page.
type LoginPage struct {
	page playwright.Page
	t    *testing.T
}

func NewLoginPage(t *testing.T, page playwright.Page) *LoginPage {
	return &LoginPage{page: page, t: t}
}

func (p *LoginPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/login")
	require.NoError(p.t, err)
}

func (p *LoginPage) NavigateWithCallback(callback string) {
	_, err := p.page.Goto(baseURL + "/login?callback_uri=" + url.QueryEscape(callback))
	require.NoError(p.t, err)
}

func (p *LoginPage) FillEmail(email string) {
	err := p.page.Locator("#email").Fill(email)
	require.NoError(p.t, err)
}

func (p *LoginPage) Submit() {
	err := p.page.Locator("button[type='submit']").Click()
	require.NoError(p.t, err)
}

func (p *LoginPage) ExpectSentPage() {
	err := p.page.WaitForURL("**/login/sent**")
	require.NoError(p.t, err)
}

func (p *LoginPage) ExpectEmailSentMessage() {
	locator := p.page.GetByText("We sent a login link")
	expect(p.t).Locator(locator).ToBeVisible()
}

// WaiverPage represents the waiver signing page.
type WaiverPage struct {
	page playwright.Page
	t    *testing.T
}

func NewWaiverPage(t *testing.T, page playwright.Page) *WaiverPage {
	return &WaiverPage{page: page, t: t}
}

func (p *WaiverPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/waiver")
	require.NoError(p.t, err)
}

func (p *WaiverPage) NavigateWithRedirect(redirect string) {
	_, err := p.page.Goto(baseURL + "/waiver?r=" + url.QueryEscape(redirect))
	require.NoError(p.t, err)
}

func (p *WaiverPage) FillName(name string) {
	err := p.page.Locator("#name").Fill(name)
	require.NoError(p.t, err)
}

func (p *WaiverPage) FillEmail(email string) {
	err := p.page.Locator("#email").Fill(email)
	require.NoError(p.t, err)
}

func (p *WaiverPage) CheckAgree1() {
	err := p.page.Locator("#agree0").Check()
	require.NoError(p.t, err)
}

func (p *WaiverPage) CheckAgree2() {
	err := p.page.Locator("#agree1").Check()
	require.NoError(p.t, err)
}

func (p *WaiverPage) Submit() {
	err := p.page.Locator("button[type='submit']").Click()
	require.NoError(p.t, err)
}

func (p *WaiverPage) ExpectSuccessMessage() {
	locator := p.page.GetByText("Waiver has been submitted successfully")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *WaiverPage) ExpectWaiverText() {
	locator := p.page.GetByText("Liability Waiver")
	expect(p.t).Locator(locator).ToBeVisible()
}

// MemberDashboardPage represents the member dashboard.
type MemberDashboardPage struct {
	page playwright.Page
	t    *testing.T
}

func NewMemberDashboardPage(t *testing.T, page playwright.Page) *MemberDashboardPage {
	return &MemberDashboardPage{page: page, t: t}
}

func (p *MemberDashboardPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/")
	require.NoError(p.t, err)
}

func (p *MemberDashboardPage) ExpectActiveStatus() {
	locator := p.page.GetByText("You're all set!")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *MemberDashboardPage) ExpectWelcomeMessage() {
	locator := p.page.GetByText("Welcome!")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *MemberDashboardPage) ExpectOnboardingChecklist() {
	locator := p.page.GetByText("Membership Setup")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *MemberDashboardPage) ExpectMissingWaiverAlert() {
	// The new UI shows the "Sign Waiver" button when waiver is missing
	locator := p.page.Locator("a.btn:has-text('Sign Waiver')")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *MemberDashboardPage) ExpectMissingPaymentAlert() {
	// The new UI shows the "Set Up Payment" button when payment is missing
	locator := p.page.Locator("a.btn:has-text('Set Up Payment')")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *MemberDashboardPage) ExpectMissingKeyFobAlert() {
	// The new UI shows "Action Required" badge for key fob step
	locator := p.page.GetByText("Action Required")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *MemberDashboardPage) ExpectFamilyInactiveAlert() {
	locator := p.page.GetByText("Family Plan Issue")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *MemberDashboardPage) ExpectStepComplete(stepTitle string) {
	// Find the step item that contains the title and check for Complete badge
	stepLocator := p.page.Locator("li.list-group-item", playwright.PageLocatorOptions{
		Has: p.page.GetByText(stepTitle),
	})
	expect(p.t).Locator(stepLocator.GetByText("Complete")).ToBeVisible()
}

func (p *MemberDashboardPage) ExpectStepPending(stepTitle string) {
	// Find the step item that contains the title and check for Pending badge
	stepLocator := p.page.Locator("li.list-group-item", playwright.PageLocatorOptions{
		Has: p.page.GetByText(stepTitle),
	})
	expect(p.t).Locator(stepLocator.GetByText("Pending")).ToBeVisible()
}

func (p *MemberDashboardPage) ClickManagePayment() {
	err := p.page.Locator("a:has-text('Manage Payment')").Click()
	require.NoError(p.t, err)
}

func (p *MemberDashboardPage) ClickLinkDiscord() {
	err := p.page.Locator("a:has-text('Link Discord')").Click()
	require.NoError(p.t, err)
}

func (p *MemberDashboardPage) ClickLogout() {
	err := p.page.Locator("a:has-text('Logout')").Click()
	require.NoError(p.t, err)
}

// AdminMembersListPage represents the admin members list page.
type AdminMembersListPage struct {
	page playwright.Page
	t    *testing.T
}

func NewAdminMembersListPage(t *testing.T, page playwright.Page) *AdminMembersListPage {
	return &AdminMembersListPage{page: page, t: t}
}

func (p *AdminMembersListPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/admin/members")
	require.NoError(p.t, err)
}

func (p *AdminMembersListPage) Search(query string) {
	err := p.page.Locator("#searchbox").Fill(query)
	require.NoError(p.t, err)
	// Wait for HTMX to complete
	err = p.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	})
	require.NoError(p.t, err)
}

func (p *AdminMembersListPage) ExpectMemberInList(identifier string) {
	locator := p.page.Locator("#results").GetByText(identifier)
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *AdminMembersListPage) ExpectMemberNotInList(identifier string) {
	locator := p.page.Locator("#results").GetByText(identifier)
	expect(p.t).Locator(locator).ToBeHidden()
}

func (p *AdminMembersListPage) ClickMemberRow(identifier string) {
	err := p.page.Locator("tr", playwright.PageLocatorOptions{HasText: identifier}).Click()
	require.NoError(p.t, err)
}

func (p *AdminMembersListPage) ClickNextPage() {
	err := p.page.Locator("a:has-text('Next')").Click()
	require.NoError(p.t, err)
}

func (p *AdminMembersListPage) ClickPreviousPage() {
	err := p.page.Locator("a:has-text('Previous')").Click()
	require.NoError(p.t, err)
}

func (p *AdminMembersListPage) ExpectPageNumber(pageNum int) {
	locator := p.page.Locator("#currentpage")
	expect(p.t).Locator(locator).ToHaveValue(string(rune('0' + pageNum)))
}

// AdminMemberDetailPage represents the admin member detail page.
type AdminMemberDetailPage struct {
	page playwright.Page
	t    *testing.T
}

func NewAdminMemberDetailPage(t *testing.T, page playwright.Page) *AdminMemberDetailPage {
	return &AdminMemberDetailPage{page: page, t: t}
}

func (p *AdminMemberDetailPage) NavigateToMember(memberID int64) {
	_, err := p.page.Goto(baseURL + "/admin/members/" + string(rune(memberID)))
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) FillNameOverride(name string) {
	err := p.page.Locator("input[name='name']").Fill(name)
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) FillEmail(email string) {
	err := p.page.Locator("input[name='email']").Fill(email)
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) FillFobID(fobID string) {
	err := p.page.Locator("input[name='fob_id']").Fill(fobID)
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) FillAdminNotes(notes string) {
	err := p.page.Locator("textarea[name='admin_notes']").Fill(notes)
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) ToggleLeadership() {
	err := p.page.Locator("input[name='leadership']").Click()
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) ToggleNonBillable() {
	err := p.page.Locator("input[name='non_billable']").Click()
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) SubmitBasicsForm() {
	// Find the form containing the basics and submit it
	err := p.page.Locator("form[action$='/updates/basics'] button[type='submit']").Click()
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) SubmitDesignationsForm() {
	err := p.page.Locator("form[action$='/updates/designations'] button[type='submit']").Click()
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) ClickGenerateLoginQR() {
	err := p.page.Locator("a:has-text('Login Code')").Click()
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) ClickDeleteMember() {
	// Click the first Delete button (the one that shows the confirmation)
	err := p.page.Locator("button.btn-secondary:has-text('Delete')").Click()
	require.NoError(p.t, err)
}

func (p *AdminMemberDetailPage) ConfirmDelete() {
	// Click the Confirm Delete button (which has id="delete-account-link")
	err := p.page.Locator("#delete-account-link").Click()
	require.NoError(p.t, err)
}

// AdminDataListPage represents generic admin data list pages (fobs, events, waivers).
type AdminDataListPage struct {
	page playwright.Page
	t    *testing.T
	path string
}

func NewAdminEventsPage(t *testing.T, page playwright.Page) *AdminDataListPage {
	return &AdminDataListPage{page: page, t: t, path: "/admin/events"}
}

func (p *AdminDataListPage) Navigate() {
	_, err := p.page.Goto(baseURL + p.path)
	require.NoError(p.t, err)
}

func (p *AdminDataListPage) Search(query string) {
	err := p.page.Locator("#searchbox").Fill(query)
	require.NoError(p.t, err)
	err = p.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	})
	require.NoError(p.t, err)
}

func (p *AdminDataListPage) ExpectRowWithText(text string) {
	locator := p.page.Locator("#results").GetByText(text)
	expect(p.t).Locator(locator).ToBeVisible()
}

// AdminMetricsPage represents the admin metrics page.
type AdminMetricsPage struct {
	page playwright.Page
	t    *testing.T
}

func NewAdminMetricsPage(t *testing.T, page playwright.Page) *AdminMetricsPage {
	return &AdminMetricsPage{page: page, t: t}
}

func (p *AdminMetricsPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/admin/metrics")
	require.NoError(p.t, err)
}

func (p *AdminMetricsPage) SelectInterval(interval string) {
	_, err := p.page.Locator("#interval").SelectOption(playwright.SelectOptionValues{
		Values: &[]string{interval},
	})
	require.NoError(p.t, err)
}

func (p *AdminMetricsPage) ExpectChartForSeries(series string) {
	locator := p.page.Locator("canvas[data-series='" + series + "']")
	expect(p.t).Locator(locator).ToBeVisible()
}

// KioskPage represents the kiosk page.
type KioskPage struct {
	page playwright.Page
	t    *testing.T
}

func NewKioskPage(t *testing.T, page playwright.Page) *KioskPage {
	return &KioskPage{page: page, t: t}
}

func (p *KioskPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/kiosk")
	require.NoError(p.t, err)
}

func (p *KioskPage) ExpectKioskInterface() {
	// The kiosk page should show the welcome message
	locator := p.page.GetByText("How To Join")
	expect(p.t).Locator(locator).ToBeVisible()
}

// MachinesPage represents the machines/printers status page.
type MachinesPage struct {
	page playwright.Page
	t    *testing.T
}

func NewMachinesPage(t *testing.T, page playwright.Page) *MachinesPage {
	return &MachinesPage{page: page, t: t}
}

func (p *MachinesPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/machines")
	require.NoError(p.t, err)
}

func (p *MachinesPage) ExpectHeading() {
	locator := p.page.Locator("h2", playwright.PageLocatorOptions{HasText: "Printers"})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *MachinesPage) PrinterCard(name string) playwright.Locator {
	return p.page.Locator(".card", playwright.PageLocatorOptions{
		Has: p.page.Locator(".card-title", playwright.PageLocatorOptions{HasText: name}),
	})
}

func (p *MachinesPage) ExpectPrinterCard(name string) {
	expect(p.t).Locator(p.PrinterCard(name)).ToBeVisible()
}

func (p *MachinesPage) StatusBadge(printerName string) playwright.Locator {
	return p.PrinterCard(printerName).Locator(".badge")
}

func (p *MachinesPage) ExpectStatusBadge(printerName, status string) {
	locator := p.StatusBadge(printerName)
	expect(p.t).Locator(locator).ToHaveText(status)
}

func (p *MachinesPage) StopButton(printerName string) playwright.Locator {
	return p.PrinterCard(printerName).Locator("button[type='submit']")
}

func (p *MachinesPage) ExpectStopButton(printerName string) {
	expect(p.t).Locator(p.StopButton(printerName)).ToBeVisible()
}

func (p *MachinesPage) ExpectNoStopButton(printerName string) {
	expect(p.t).Locator(p.StopButton(printerName)).ToBeHidden()
}

func (p *MachinesPage) CameraImg(printerName string) playwright.Locator {
	return p.PrinterCard(printerName).Locator("img.card-img-top")
}

func (p *MachinesPage) ExpectCameraImg(printerName string) {
	expect(p.t).Locator(p.CameraImg(printerName)).ToBeVisible()
}

func (p *MachinesPage) ExpectTimeRemaining(printerName string) {
	locator := p.PrinterCard(printerName).Locator("small.text-muted", playwright.LocatorLocatorOptions{HasText: "remaining"})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *MachinesPage) ExpectErrorCode(printerName, errorCode string) {
	locator := p.PrinterCard(printerName).Locator("small.text-muted", playwright.LocatorLocatorOptions{HasText: errorCode})
	expect(p.t).Locator(locator).ToBeVisible()
}

// AdminWaiverConfigPage represents the admin waiver configuration page.
type AdminWaiverConfigPage struct {
	page playwright.Page
	t    *testing.T
}

func NewAdminWaiverConfigPage(t *testing.T, page playwright.Page) *AdminWaiverConfigPage {
	return &AdminWaiverConfigPage{page: page, t: t}
}

func (p *AdminWaiverConfigPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/admin/config/waiver")
	require.NoError(p.t, err)
}

func (p *AdminWaiverConfigPage) GetContent() string {
	content, err := p.page.Locator("#content").InputValue()
	require.NoError(p.t, err)
	return content
}

func (p *AdminWaiverConfigPage) SetContent(content string) {
	err := p.page.Locator("#content").Fill(content)
	require.NoError(p.t, err)
}

func (p *AdminWaiverConfigPage) Submit() {
	err := p.page.Locator("button[type='submit']").Click()
	require.NoError(p.t, err)
}

func (p *AdminWaiverConfigPage) ExpectVersionBadge(version int) {
	locator := p.page.Locator(".badge", playwright.PageLocatorOptions{HasText: fmt.Sprintf("Version %d", version)})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *AdminWaiverConfigPage) ExpectSaveSuccessMessage() {
	locator := p.page.Locator(".alert-success", playwright.PageLocatorOptions{HasText: "saved successfully"})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *AdminWaiverConfigPage) ExpectSyntaxGuide() {
	expect(p.t).Locator(p.page.GetByText("# Title")).ToBeVisible()
	expect(p.t).Locator(p.page.GetByText("- [ ] Checkbox text")).ToBeVisible()
}
