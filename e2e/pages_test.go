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

func (p *LoginPage) ExpandLoginCodeSection() {
	err := p.page.Locator("a:has-text('Have a login code?')").Click()
	require.NoError(p.t, err)
	// Wait for collapse animation
	err = p.page.Locator("#login-code-section").WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(p.t, err)
}

func (p *LoginPage) FillCode(code string) {
	digits := p.page.Locator(".code-digit")
	for i, digit := range code {
		err := digits.Nth(i).Fill(string(digit))
		require.NoError(p.t, err)
	}
}

func (p *LoginPage) ExpectNoLoginMethods() {
	locator := p.page.GetByText("No login methods are configured")
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

// DirectoryPage represents the member directory page.
type DirectoryPage struct {
	page playwright.Page
	t    *testing.T
}

func NewDirectoryPage(t *testing.T, page playwright.Page) *DirectoryPage {
	return &DirectoryPage{page: page, t: t}
}

func (p *DirectoryPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/directory")
	require.NoError(p.t, err)
}

func (p *DirectoryPage) ExpectHeading() {
	locator := p.page.Locator("h2", playwright.PageLocatorOptions{HasText: "Member Directory"})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *DirectoryPage) ExpectMemberCard(displayName string) {
	locator := p.page.Locator(".card", playwright.PageLocatorOptions{
		Has: p.page.Locator(".card-title", playwright.PageLocatorOptions{HasText: displayName}),
	})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *DirectoryPage) ExpectMemberCardNotVisible(displayName string) {
	locator := p.page.Locator(".card", playwright.PageLocatorOptions{
		Has: p.page.Locator(".card-title", playwright.PageLocatorOptions{HasText: displayName}),
	})
	expect(p.t).Locator(locator).ToBeHidden()
}

func (p *DirectoryPage) MemberCard(displayName string) playwright.Locator {
	return p.page.Locator(".card", playwright.PageLocatorOptions{
		Has: p.page.Locator(".card-title", playwright.PageLocatorOptions{HasText: displayName}),
	})
}

func (p *DirectoryPage) ExpectLeadershipBadge(displayName string) {
	card := p.MemberCard(displayName)
	expect(p.t).Locator(card.Locator(".badge", playwright.LocatorLocatorOptions{HasText: "Leadership"})).ToBeVisible()
}

func (p *DirectoryPage) ExpectNoLeadershipBadge(displayName string) {
	card := p.MemberCard(displayName)
	expect(p.t).Locator(card.Locator(".badge", playwright.LocatorLocatorOptions{HasText: "Leadership"})).ToBeHidden()
}

func (p *DirectoryPage) ExpectDiscordUsername(displayName, discordUsername string) {
	card := p.MemberCard(displayName)
	expect(p.t).Locator(card.GetByText("@" + discordUsername)).ToBeVisible()
}

func (p *DirectoryPage) ExpectAvatar(displayName string) {
	card := p.MemberCard(displayName)
	expect(p.t).Locator(card.Locator("img.rounded-circle")).ToBeVisible()
}

func (p *DirectoryPage) ExpectPlaceholderAvatar(displayName string) {
	card := p.MemberCard(displayName)
	// Placeholder avatar is a div with an SVG, not an img
	expect(p.t).Locator(card.Locator("div.rounded-circle svg")).ToBeVisible()
}

func (p *DirectoryPage) ExpectEmptyMessage() {
	locator := p.page.GetByText("No members with building access found.")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *DirectoryPage) GetMemberCardCount() int {
	count, err := p.page.Locator(".card").Count()
	require.NoError(p.t, err)
	return count
}

func (p *DirectoryPage) ExpectBio(displayName, bio string) {
	card := p.MemberCard(displayName)
	expect(p.t).Locator(card.GetByText(bio)).ToBeVisible()
}

func (p *DirectoryPage) ClickEditProfile() {
	err := p.page.Locator("a:has-text('Edit Profile')").Click()
	require.NoError(p.t, err)
}

func (p *DirectoryPage) ExpectMemberCardFirst(displayName string) {
	// The first member card should have the given display name
	firstCard := p.page.Locator(".card").First()
	expect(p.t).Locator(firstCard.Locator(".card-title", playwright.LocatorLocatorOptions{HasText: displayName})).ToBeVisible()
}

// ProfilePage represents the profile editing page.
type ProfilePage struct {
	page playwright.Page
	t    *testing.T
}

func NewProfilePage(t *testing.T, page playwright.Page) *ProfilePage {
	return &ProfilePage{page: page, t: t}
}

func (p *ProfilePage) Navigate() {
	_, err := p.page.Goto(baseURL + "/directory/profile")
	require.NoError(p.t, err)
}

func (p *ProfilePage) ExpectHeading() {
	locator := p.page.Locator("h2", playwright.PageLocatorOptions{HasText: "Edit Profile"})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *ProfilePage) ExpectPreviewName(name string) {
	locator := p.page.Locator("#preview-name")
	expect(p.t).Locator(locator).ToHaveText(name)
}

func (p *ProfilePage) ExpectBioValue(bio string) {
	locator := p.page.Locator("#bio")
	expect(p.t).Locator(locator).ToHaveValue(bio)
}

func (p *ProfilePage) FillBio(bio string) {
	err := p.page.Locator("#bio").Fill(bio)
	require.NoError(p.t, err)
}

func (p *ProfilePage) Submit() {
	err := p.page.Locator("button[type='submit']:has-text('Save Changes')").Click()
	require.NoError(p.t, err)
}

func (p *ProfilePage) ExpectDiscordUsername(username string) {
	locator := p.page.GetByText("@" + username)
	expect(p.t).Locator(locator).ToBeVisible()
}

// AdminStripeConfigPage represents the admin Stripe configuration page.
type AdminStripeConfigPage struct {
	page playwright.Page
	t    *testing.T
}

func NewAdminStripeConfigPage(t *testing.T, page playwright.Page) *AdminStripeConfigPage {
	return &AdminStripeConfigPage{page: page, t: t}
}

func (p *AdminStripeConfigPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/admin/config/stripe")
	require.NoError(p.t, err)
}

func (p *AdminStripeConfigPage) FillAPIKey(key string) {
	err := p.page.Locator("#api_key").Fill(key)
	require.NoError(p.t, err)
}

func (p *AdminStripeConfigPage) FillWebhookKey(key string) {
	err := p.page.Locator("#webhook_key").Fill(key)
	require.NoError(p.t, err)
}

func (p *AdminStripeConfigPage) Submit() {
	err := p.page.Locator("button[type='submit']").Click()
	require.NoError(p.t, err)
}

func (p *AdminStripeConfigPage) ExpectVersionBadge(version int) {
	locator := p.page.Locator(".badge", playwright.PageLocatorOptions{HasText: fmt.Sprintf("Version %d", version)})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *AdminStripeConfigPage) ExpectSaveSuccessMessage() {
	locator := p.page.Locator(".alert-success", playwright.PageLocatorOptions{HasText: "saved successfully"})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *AdminStripeConfigPage) ExpectHasAPIKey() {
	locator := p.page.Locator("#api_key")
	placeholder, err := locator.GetAttribute("placeholder")
	require.NoError(p.t, err)
	require.Contains(p.t, placeholder, "secret is set", "API key should show as set")
}

func (p *AdminStripeConfigPage) ExpectHasWebhookKey() {
	locator := p.page.Locator("#webhook_key")
	placeholder, err := locator.GetAttribute("placeholder")
	require.NoError(p.t, err)
	require.Contains(p.t, placeholder, "secret is set", "Webhook key should show as set")
}

func (p *AdminStripeConfigPage) ExpectActiveSubscriptions(count int) {
	locator := p.page.Locator(".card-body h3:has-text('" + fmt.Sprintf("%d", count) + "')")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *AdminStripeConfigPage) ExpectWebhookURLInstruction() {
	locator := p.page.Locator("code:has-text('/webhooks/stripe')")
	expect(p.t).Locator(locator).ToBeVisible()
}

// AdminBambuConfigPage represents the admin Bambu configuration page.
type AdminBambuConfigPage struct {
	page playwright.Page
	t    *testing.T
}

func NewAdminBambuConfigPage(t *testing.T, page playwright.Page) *AdminBambuConfigPage {
	return &AdminBambuConfigPage{page: page, t: t}
}

func (p *AdminBambuConfigPage) Navigate() {
	_, err := p.page.Goto(baseURL + "/admin/config/bambu")
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) ExpectPageTitle() {
	locator := p.page.GetByText("Bambu 3D Printer Integration")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *AdminBambuConfigPage) ExpectAddPrinterButton() {
	locator := p.page.Locator("button:has-text('Add Printer')")
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *AdminBambuConfigPage) ClickAddPrinter() {
	err := p.page.Locator("button:has-text('Add Printer')").Click()
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) PrinterCardCount() int {
	count, err := p.page.Locator("#printers-container .printer-card").Count()
	require.NoError(p.t, err)
	return count
}

func (p *AdminBambuConfigPage) PrinterCard(index int) playwright.Locator {
	return p.page.Locator(fmt.Sprintf("#printers-container .printer-card[data-printer-index='%d']", index))
}

func (p *AdminBambuConfigPage) FillPrinterName(index int, name string) {
	locator := p.page.Locator(fmt.Sprintf("input[name='printer[%d][name]']", index))
	err := locator.Fill(name)
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) FillPrinterHost(index int, host string) {
	locator := p.page.Locator(fmt.Sprintf("input[name='printer[%d][host]']", index))
	err := locator.Fill(host)
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) FillPrinterAccessCode(index int, code string) {
	locator := p.page.Locator(fmt.Sprintf("input[name='printer[%d][access_code]']", index))
	err := locator.Fill(code)
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) FillPrinterSerial(index int, serial string) {
	locator := p.page.Locator(fmt.Sprintf("input[name='printer[%d][serial_number]']", index))
	err := locator.Fill(serial)
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) GetPrinterName(index int) string {
	locator := p.page.Locator(fmt.Sprintf("input[name='printer[%d][name]']", index))
	value, err := locator.InputValue()
	require.NoError(p.t, err)
	return value
}

func (p *AdminBambuConfigPage) GetPrinterHost(index int) string {
	locator := p.page.Locator(fmt.Sprintf("input[name='printer[%d][host]']", index))
	value, err := locator.InputValue()
	require.NoError(p.t, err)
	return value
}

func (p *AdminBambuConfigPage) GetPrinterSerial(index int) string {
	locator := p.page.Locator(fmt.Sprintf("input[name='printer[%d][serial_number]']", index))
	value, err := locator.InputValue()
	require.NoError(p.t, err)
	return value
}

func (p *AdminBambuConfigPage) ExpectPrinterAccessCodePlaceholder(index int, expectedPlaceholder string) {
	locator := p.page.Locator(fmt.Sprintf("input[name='printer[%d][access_code]']", index))
	placeholder, err := locator.GetAttribute("placeholder")
	require.NoError(p.t, err)
	require.Contains(p.t, placeholder, expectedPlaceholder)
}

func (p *AdminBambuConfigPage) ClickDeletePrinter(index int) {
	card := p.PrinterCard(index)
	err := card.Locator("button.delete-printer-btn").Click()
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) ConfirmDeletePrinter(index int) {
	card := p.PrinterCard(index)
	err := card.Locator(".delete-confirm button.btn-danger").Click()
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) CancelDeletePrinter(index int) {
	card := p.PrinterCard(index)
	err := card.Locator(".delete-confirm button.btn-secondary").Click()
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) ExpectDeleteConfirmVisible(index int) {
	card := p.PrinterCard(index)
	expect(p.t).Locator(card.Locator(".delete-confirm")).ToBeVisible()
}

func (p *AdminBambuConfigPage) ExpectDeleteConfirmHidden(index int) {
	card := p.PrinterCard(index)
	expect(p.t).Locator(card.Locator(".delete-confirm")).ToBeHidden()
}

func (p *AdminBambuConfigPage) FillPollInterval(seconds int) {
	err := p.page.Locator("#poll_interval_seconds").Fill(fmt.Sprintf("%d", seconds))
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) GetPollInterval() string {
	value, err := p.page.Locator("#poll_interval_seconds").InputValue()
	require.NoError(p.t, err)
	return value
}

func (p *AdminBambuConfigPage) Submit() {
	err := p.page.Locator("button[type='submit']").Click()
	require.NoError(p.t, err)
}

func (p *AdminBambuConfigPage) ExpectVersionBadge(version int) {
	locator := p.page.Locator(".badge", playwright.PageLocatorOptions{HasText: fmt.Sprintf("Version %d", version)})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *AdminBambuConfigPage) ExpectSaveSuccessMessage() {
	locator := p.page.Locator(".alert-success", playwright.PageLocatorOptions{HasText: "saved successfully"})
	expect(p.t).Locator(locator).ToBeVisible()
}

func (p *AdminBambuConfigPage) ExpectConfiguredPrintersCount(count int) {
	// Target the Status card specifically (contains "Configured Printers" text)
	statusCard := p.page.Locator(".card:has-text('Configured Printers')")
	locator := statusCard.Locator(".col-md-6").First().Locator("h3")
	text, err := locator.TextContent()
	require.NoError(p.t, err)
	require.Equal(p.t, fmt.Sprintf("%d", count), text)
}

func (p *AdminBambuConfigPage) ExpectPollIntervalDisplay(seconds int) {
	// Target the Status card specifically (contains "Poll Interval" text)
	statusCard := p.page.Locator(".card:has-text('Poll Interval')")
	locator := statusCard.Locator(".col-md-6").Last().Locator("h3")
	text, err := locator.TextContent()
	require.NoError(p.t, err)
	require.Equal(p.t, fmt.Sprintf("%ds", seconds), text)
}

func (p *AdminBambuConfigPage) ExpectPrinterCardHeaderText(index int, expectedText string) {
	card := p.PrinterCard(index)
	header := card.Locator(".printer-name-display")
	text, err := header.TextContent()
	require.NoError(p.t, err)
	require.Equal(p.t, expectedText, text)
}
