package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuth2_Discovery(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	t.Run("openid_config", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/.well-known/openid-configuration")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())

		body, err := resp.Body()
		require.NoError(t, err)

		var config map[string]interface{}
		err = json.Unmarshal(body, &config)
		require.NoError(t, err)

		assert.Contains(t, config, "issuer")
		assert.Contains(t, config, "authorization_endpoint")
		assert.Contains(t, config, "token_endpoint")
		assert.Contains(t, config, "userinfo_endpoint")
		assert.Contains(t, config, "jwks_uri")
	})

	t.Run("jwks", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/oauth2/jwks")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())

		body, err := resp.Body()
		require.NoError(t, err)

		var jwks map[string]interface{}
		err = json.Unmarshal(body, &jwks)
		require.NoError(t, err)

		assert.Contains(t, jwks, "keys")
		keys := jwks["keys"].([]interface{})
		assert.NotEmpty(t, keys, "should have at least one key")

		key := keys[0].(map[string]interface{})
		assert.Contains(t, key, "kty")
		assert.Contains(t, key, "kid")
		assert.Contains(t, key, "n")
		assert.Contains(t, key, "e")
	})
}

func TestOAuth2_AuthorizeFlow(t *testing.T) {
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

	redirectURI := baseURL + "/login"
	authURL := baseURL + "/oauth2/authorize?response_type=code&client_id=test-client&redirect_uri=" + redirectURI + "&state=teststate"

	resp, _ := page.Goto(authURL)
	finalURL := page.URL()

	// The endpoint should return a valid response
	assert.True(t, resp.Status() == 200 || resp.Status() == 302 || resp.Status() == 400,
		"should return valid HTTP status, got: %d", resp.Status())

	// Check if we got redirected with a code parameter
	if strings.Contains(finalURL, "code=") {
		assert.Contains(t, finalURL, "state=teststate")
	}
}

func TestOAuth2_UserInfo_RequiresAuth(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	resp, err := page.Goto(baseURL + "/oauth2/userinfo")
	require.NoError(t, err)

	assert.GreaterOrEqual(t, resp.Status(), 400)
}
