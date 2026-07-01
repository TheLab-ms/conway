package e2e

import (
	"net/http"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

func TestElections_AdminSettingsWorkflowAndMemberVoting(t *testing.T) {
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "elections-admin@example.com", WithConfirmed(), WithNonBillable(), WithLeadership())
	memberID := seedMember(t, env, "elections-voter@example.com", WithConfirmed(), WithNonBillable())

	adminPage := newPage(t)
	loginPageAs(t, env, adminPage, adminID)
	_, err := adminPage.Goto(env.baseURL + "/admin/config/elections")
	require.NoError(t, err)
	expect(t).Locator(adminPage.GetByText("Election Management")).ToBeVisible()
	expect(t).Locator(adminPage.Locator("nav a[href='/admin/config']")).ToBeVisible()
	expect(t).Locator(adminPage.Locator("nav a[href='/admin/elections']")).ToHaveCount(0)

	require.NoError(t, adminPage.Locator("a:has-text('New election')").First().Click())
	require.NoError(t, adminPage.Locator("#title").Fill("Board Seat 2026"))
	require.NoError(t, adminPage.Locator("#description").Fill("Choose the candidate you trust most."))
	questionInputs := adminPage.Locator("input[name='question_text']")
	require.NoError(t, questionInputs.Nth(0).Fill("Who should represent members?"))
	optionInputs := adminPage.Locator("input[name='option_label_0']")
	require.NoError(t, optionInputs.Nth(0).Fill("Ada Lovelace"))
	require.NoError(t, optionInputs.Nth(1).Fill("Grace Hopper"))
	require.NoError(t, adminPage.Locator("button:has-text('Add question')").Click())
	require.NoError(t, questionInputs.Nth(1).Fill("Which budget priority matters most?"))
	secondQuestionOptions := adminPage.Locator(".question-card").Nth(1).Locator("input.option-label")
	require.NoError(t, secondQuestionOptions.Nth(0).Fill("Tools"))
	require.NoError(t, secondQuestionOptions.Nth(1).Fill("Classes"))
	require.NoError(t, adminPage.Locator("button:has-text('Save draft')").Click())
	require.NoError(t, adminPage.WaitForURL("**/admin/config/elections/*"))
	expect(t).Locator(adminPage.GetByText("Private voting link")).ToBeVisible()

	shareURL, err := adminPage.Locator("#share-url").InputValue()
	require.NoError(t, err)
	require.NotEmpty(t, shareURL)
	electionID := lastPathSegment(shareURL)
	require.NotEmpty(t, electionID)

	memberPage := newPage(t)
	loginPageAs(t, env, memberPage, memberID)
	resp, err := memberPage.Goto(shareURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.Status())

	_, err = adminPage.Goto(env.baseURL + "/admin/config/elections/" + electionID)
	require.NoError(t, err)
	require.NoError(t, adminPage.Locator("button:has-text('Open voting')").Click())
	require.NoError(t, adminPage.WaitForURL("**/admin/config/elections/*"))
	expect(t).Locator(adminPage.GetByText("Open")).ToBeVisible()

	_, err = memberPage.Goto(shareURL)
	require.NoError(t, err)
	expect(t).Locator(memberPage.GetByText("Board Seat 2026")).ToBeVisible()
	require.NoError(t, memberPage.Locator("label:has-text('Ada Lovelace') input").Check())
	require.NoError(t, memberPage.Locator("label:has-text('Tools') input").Check())
	require.NoError(t, memberPage.Locator("button:has-text('Submit vote')").Click())
	require.NoError(t, memberPage.WaitForURL("**/elections/*?voted=1"))
	expect(t).Locator(memberPage.GetByText("Your vote is recorded")).ToBeVisible()
	expect(t).Locator(memberPage.Locator("button:has-text('Vote submitted')")).ToBeVisible()
	expect(t).Locator(memberPage.Locator("button:has-text('Submit vote')")).ToHaveCount(0)
	resp, err = memberPage.Goto(shareURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status())
	expect(t).Locator(memberPage.GetByText("Your vote is recorded and cannot be changed.")).ToBeVisible()
	expect(t).Locator(memberPage.Locator("button:has-text('Submit vote')")).ToHaveCount(0)

	var voteCount, logCount int
	require.NoError(t, env.db.QueryRow(`SELECT COUNT(*) FROM election_votes WHERE election_id = ?`, electionID).Scan(&voteCount))
	require.NoError(t, env.db.QueryRow(`SELECT COUNT(*) FROM election_vote_log WHERE election_id = ? AND member_id = ?`, electionID, memberID).Scan(&logCount))
	require.Equal(t, 1, voteCount)
	require.Equal(t, 1, logCount)

	_, err = adminPage.Goto(env.baseURL + "/admin/config/elections/" + electionID + "/results")
	require.NoError(t, err)
	expect(t).Locator(adminPage.GetByText("Ada Lovelace")).ToBeVisible()
	expect(t).Locator(adminPage.GetByText("Tools")).ToBeVisible()
	expect(t).Locator(adminPage.GetByText("1 votes")).ToBeVisible()

	_, err = adminPage.Goto(env.baseURL + "/admin/config/elections/" + electionID + "/votes")
	require.NoError(t, err)
	expect(t).Locator(adminPage.GetByText("elections-voter@example.com")).ToBeVisible()
	expect(t).Locator(adminPage.GetByText("Ada Lovelace")).ToBeVisible()
	expect(t).Locator(adminPage.GetByText("Tools")).ToBeVisible()

	_, err = adminPage.Goto(env.baseURL + "/admin/config/elections/" + electionID)
	require.NoError(t, err)
	require.NoError(t, adminPage.Locator("button:has-text('Close voting')").Click())
	require.NoError(t, adminPage.WaitForURL("**/admin/config/elections/*"))
	_, err = memberPage.Goto(shareURL)
	require.NoError(t, err)
	expect(t).Locator(memberPage.GetByText("This election is closed")).ToBeVisible()
	expect(t).Locator(memberPage.Locator("button:has-text('Vote submitted')")).ToBeVisible()

	_, err = adminPage.Goto(env.baseURL + "/admin/config/elections/" + electionID)
	require.NoError(t, err)
	adminPage.OnDialog(func(dialog playwright.Dialog) {
		require.Equal(t, "confirm", dialog.Type())
		require.NoError(t, dialog.Accept())
	})
	require.NoError(t, adminPage.Locator("button:has-text('Delete')").Click())
	require.NoError(t, adminPage.WaitForURL("**/admin/config/elections"))
	resp, err = memberPage.Goto(shareURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.Status())
}

func TestElections_OnlyActiveMembersCanVote(t *testing.T) {
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "elections-admin-2@example.com", WithConfirmed(), WithNonBillable(), WithLeadership())
	inactiveID := seedMember(t, env, "inactive-voter@example.com", WithConfirmed())
	electionID := seedOpenElection(t, env, adminID)

	page := newPage(t)
	loginPageAs(t, env, page, inactiveID)
	resp, err := page.Goto(env.baseURL + "/elections/" + electionID)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.Status())
	expect(t).Locator(page.GetByText("Only active members can vote")).ToBeVisible()
}

func seedOpenElection(t *testing.T, env *TestEnv, adminID int64) string {
	t.Helper()
	var id string
	err := env.db.QueryRow(`INSERT INTO elections (id, created_by, title, question, status, max_choices)
		VALUES ('11111111-1111-4111-8111-111111111111', ?, 'Seed Election', 'Pick one', 'open', 1)
		RETURNING id`, adminID).Scan(&id)
	require.NoError(t, err)
	var questionID int64
	err = env.db.QueryRow(`INSERT INTO election_questions (election_id, position, question, max_choices) VALUES (?, 1, 'Pick one', 1) RETURNING id`, id).Scan(&questionID)
	require.NoError(t, err)
	_, err = env.db.Exec(`INSERT INTO election_options (election_id, question_id, position, label) VALUES (?, ?, 1, 'One'), (?, ?, 2, 'Two')`, id, questionID, id, questionID)
	require.NoError(t, err)
	return id
}

func lastPathSegment(raw string) string {
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == '/' {
			return raw[i+1:]
		}
	}
	return raw
}
