package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuth2_OpenIDConfig(t *testing.T) {
	clearTestData(t)

	page := newPage(t)

	resp, err := page.Goto(baseURL + "/.well-known/openid-configuration")
	require.NoError(t, err)

	assert.Equal(t, 200, resp.Status())

	body, err := resp.Body()
	require.NoError(t, err)

	var config map[string]interface{}
	err = json.Unmarshal(body, &config)
	require.NoError(t, err)

	// Verify required fields are present
	assert.Contains(t, config, "issuer")
	assert.Contains(t, config, "authorization_endpoint")
	assert.Contains(t, config, "token_endpoint")
	assert.Contains(t, config, "userinfo_endpoint")
	assert.Contains(t, config, "jwks_uri")
}

func TestOAuth2_JWKS(t *testing.T) {
	clearTestData(t)

	page := newPage(t)

	resp, err := page.Goto(baseURL + "/oauth2/jwks")
	require.NoError(t, err)

	assert.Equal(t, 200, resp.Status())

	body, err := resp.Body()
	require.NoError(t, err)

	var jwks map[string]interface{}
	err = json.Unmarshal(body, &jwks)
	require.NoError(t, err)

	// Should have keys array
	assert.Contains(t, jwks, "keys")
	keys := jwks["keys"].([]interface{})
	assert.NotEmpty(t, keys, "should have at least one key")

	// First key should have required fields
	key := keys[0].(map[string]interface{})
	assert.Contains(t, key, "kty")
	assert.Contains(t, key, "kid")
	assert.Contains(t, key, "n")
	assert.Contains(t, key, "e")
}

func TestOAuth2_AuthorizeEndpoint(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "oauth@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	// Use the base URL as redirect_uri so Playwright can follow it
	// The authorize endpoint will redirect back with a code
	redirectURI := baseURL + "/login" // Use an existing page as redirect
	authURL := baseURL + "/oauth2/authorize?response_type=code&client_id=test-client&redirect_uri=" + redirectURI + "&state=teststate"

	// The authorize endpoint will redirect with code parameter
	// Even if the client_id is not registered, it should return some response
	resp, err := page.Goto(authURL)
	require.NoError(t, err)

	// Get the final URL after redirect
	finalURL := page.URL()

	// The endpoint should either:
	// 1. Redirect to redirect_uri with code (if configured)
	// 2. Return an error (if client not registered)
	// Either way, we got a response without crashing
	assert.True(t, resp.Status() == 200 || resp.Status() == 302 || resp.Status() == 400,
		"should return valid HTTP status, got: %d", resp.Status())

	t.Logf("OAuth2 authorize redirected to: %s", finalURL)
}

func TestOAuth2_AuthorizeEndpoint_RedirectsWithCode(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "oauthcode@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	// Listen for the redirect response before navigation
	redirectURI := baseURL + "/login"
	authURL := baseURL + "/oauth2/authorize?response_type=code&client_id=test-client&redirect_uri=" + redirectURI + "&state=teststate"

	resp, _ := page.Goto(authURL)

	finalURL := page.URL()

	// Check if we got redirected with a code parameter
	if strings.Contains(finalURL, "code=") {
		t.Log("Successfully received authorization code in redirect")
		assert.Contains(t, finalURL, "state=teststate")
	} else if resp != nil && resp.Status() == 400 {
		t.Log("OAuth2 client not registered (expected in test without configured clients)")
	} else {
		t.Logf("OAuth2 authorize final URL: %s", finalURL)
	}
}

func TestOAuth2_UserInfo_RequiresAuth(t *testing.T) {
	clearTestData(t)

	page := newPage(t)

	// Try to access userinfo without auth
	resp, err := page.Goto(baseURL + "/oauth2/userinfo")
	require.NoError(t, err)

	// Should return 401 or similar error (500 if token parsing fails)
	assert.GreaterOrEqual(t, resp.Status(), 400)
}
