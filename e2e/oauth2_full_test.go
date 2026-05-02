package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noFollowClient returns an *http.Client that does not follow redirects, so
// tests can inspect the Location header and status code directly. (A separate
// helper named noRedirectClient is declared elsewhere in this package.)
func noFollowClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 5 * time.Second,
	}
}

// fetchAuthCode walks /oauth2/authorize as the given (already-seeded) member and
// returns the authorization code captured from the redirect.
func fetchAuthCode(t *testing.T, env *TestEnv, memberID int64, redirectURI, state string) string {
	t.Helper()
	tok := generateAuthToken(t, env, memberID)

	q := url.Values{}
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("response_type", "code")
	q.Set("client_id", "test-client")

	req, err := http.NewRequest("GET", env.baseURL+"/oauth2/authorize?"+q.Encode(), nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "token", Value: tok})

	resp, err := noFollowClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode, "expected 302 redirect from /oauth2/authorize")

	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	code := loc.Query().Get("code")
	require.NotEmpty(t, code, "expected code= in redirect, got %q", resp.Header.Get("Location"))
	require.Equal(t, state, loc.Query().Get("state"))
	return code
}

// exchangeCodeForToken POSTs the code to /oauth2/token with HTTP Basic auth.
// Returns the decoded JSON token response.
func exchangeCodeForToken(t *testing.T, env *TestEnv, code, clientID, clientSecret string) (status int, body map[string]any) {
	t.Helper()
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)

	req, err := http.NewRequest("POST", env.baseURL+"/oauth2/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if clientID != "" || clientSecret != "" {
		req.SetBasicAuth(clientID, clientSecret)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 && len(raw) > 0 {
		require.NoError(t, json.Unmarshal(raw, &body), "decode token response: %s", raw)
	}
	return resp.StatusCode, body
}

// TestOAuth2_DiscoveryEndpoint verifies the OIDC discovery document is served
// with the standard required fields.
func TestOAuth2_DiscoveryEndpoint(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp, err := http.Get(env.baseURL + "/.well-known/openid-configuration")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var doc map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))

	assert.Equal(t, env.baseURL, doc["issuer"])
	assert.Equal(t, env.baseURL+"/oauth2/authorize", doc["authorization_endpoint"])
	assert.Equal(t, env.baseURL+"/oauth2/token", doc["token_endpoint"])
	assert.Equal(t, env.baseURL+"/oauth2/userinfo", doc["userinfo_endpoint"])
	assert.Equal(t, env.baseURL+"/oauth2/jwks", doc["jwks_uri"])

	algs, ok := doc["id_token_signing_alg_values_supported"].([]any)
	require.True(t, ok, "id_token_signing_alg_values_supported should be an array")
	assert.Contains(t, algs, "RS256")
}

// TestOAuth2_JWKS verifies the JWKS endpoint returns a valid RSA key set.
func TestOAuth2_JWKS(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp, err := http.Get(env.baseURL + "/oauth2/jwks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var jwks struct {
		Keys []map[string]any `json:"keys"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&jwks))
	require.Len(t, jwks.Keys, 1, "expected exactly one signing key")

	k := jwks.Keys[0]
	assert.Equal(t, "RSA", k["kty"])
	assert.Equal(t, "sig", k["use"])
	assert.Equal(t, "RS256", k["alg"])
	assert.NotEmpty(t, k["kid"])
	assert.NotEmpty(t, k["n"])
	assert.NotEmpty(t, k["e"])
}

// TestOAuth2_AuthorizeRequiresAuthn ensures unauthenticated requests are
// redirected to /login (not silently issued a code).
func TestOAuth2_AuthorizeRequiresAuthn(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	q := url.Values{}
	q.Set("redirect_uri", "http://127.0.0.1/cb")
	q.Set("state", "abc")
	q.Set("response_type", "code")

	req, err := http.NewRequest("GET", env.baseURL+"/oauth2/authorize?"+q.Encode(), nil)
	require.NoError(t, err)

	resp, err := noFollowClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	loc := resp.Header.Get("Location")
	assert.True(t, strings.HasPrefix(loc, "/login?"), "expected redirect to /login, got %q", loc)
	assert.Contains(t, loc, "callback_uri=")
}

// TestOAuth2_AuthorizeRedirectsWithCode verifies an authenticated member with
// a redirect_uri sharing the server's root domain receives a 302 to that URI
// carrying ?code=...&state=...
func TestOAuth2_AuthorizeRedirectsWithCode(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "auth-redir@example.com", WithConfirmed(), WithNonBillable())

	tok := generateAuthToken(t, env, memberID)

	q := url.Values{}
	q.Set("redirect_uri", "http://127.0.0.1/cb")
	q.Set("state", "state-token-123")
	q.Set("response_type", "code")
	q.Set("client_id", "anyclient")

	req, err := http.NewRequest("GET", env.baseURL+"/oauth2/authorize?"+q.Encode(), nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "token", Value: tok})

	resp, err := noFollowClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", loc.Hostname())
	assert.Equal(t, "/cb", loc.Path)
	assert.NotEmpty(t, loc.Query().Get("code"))
	assert.Equal(t, "state-token-123", loc.Query().Get("state"))
}

// TestOAuth2_TokenExchange runs the full authorize → token → access_token flow
// and asserts the returned access_token is verifiable and bound to the
// requesting client (audience != "conway").
func TestOAuth2_TokenExchange(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "exchange@example.com", WithConfirmed(), WithNonBillable())

	code := fetchAuthCode(t, env, memberID, "http://127.0.0.1/cb", "s1")

	status, body := exchangeCodeForToken(t, env, code, "myclient", "irrelevant-secret")
	require.Equal(t, 200, status, "token exchange failed: %v", body)
	require.NotNil(t, body)

	access, _ := body["access_token"].(string)
	idTok, _ := body["id_token"].(string)
	tokType, _ := body["token_type"].(string)
	expIn, _ := body["expires_in"].(float64)

	assert.NotEmpty(t, access, "access_token missing")
	assert.NotEmpty(t, idTok, "id_token missing")
	assert.Equal(t, "Bearer", tokType)
	assert.Greater(t, expIn, float64(0))

	// Decode the access token (unverified) and verify audience is the requesting
	// client and NOT the reserved "conway" audience.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	parsed, _, err := parser.ParseUnverified(access, jwt.MapClaims{})
	require.NoError(t, err)
	claims, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)

	auds, _ := claims["aud"].([]any)
	require.Len(t, auds, 1)
	assert.Equal(t, "myclient", auds[0])
	assert.NotEqual(t, "conway", auds[0], "oauth tokens must not have the reserved 'conway' audience")
	assert.Equal(t, strconv.FormatInt(memberID, 10), claims["sub"])
}

// TestOAuth2_TokenRejectsConwayClient ensures a client cannot mint a token in
// the reserved "conway" audience (which would let it act as a session cookie).
//
// CAVEAT: The current oauth2 module does NOT validate per-client secrets or
// enforce that Basic auth be present at all. The only authentication-style
// check is that the basic-auth username (interpreted as client_id / audience)
// is not the literal string "conway". This test pins that behavior.
func TestOAuth2_TokenRejectsConwayClient(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "reserved-aud@example.com", WithConfirmed(), WithNonBillable())

	code := fetchAuthCode(t, env, memberID, "http://127.0.0.1/cb", "s2")

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)

	req, err := http.NewRequest("POST", env.baseURL+"/oauth2/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("conway", "anything")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestOAuth2_TokenInvalidCode ensures a bogus authorization code is rejected.
func TestOAuth2_TokenInvalidCode(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", "not-a-real-jwt")

	req, err := http.NewRequest("POST", env.baseURL+"/oauth2/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("myclient", "secret")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.GreaterOrEqual(t, resp.StatusCode, 400, "invalid code should not return 2xx")
}

// TestOAuth2_UserinfoMember walks the full flow as a normal active member and
// asserts the userinfo response contains email and groups: ["member"].
func TestOAuth2_UserinfoMember(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "ui-member@example.com",
		WithConfirmed(), WithNonBillable(), WithName("UI Member"))

	code := fetchAuthCode(t, env, memberID, "http://127.0.0.1/cb", "s3")
	status, body := exchangeCodeForToken(t, env, code, "myclient", "")
	require.Equal(t, 200, status)
	access := body["access_token"].(string)

	req, err := http.NewRequest("GET", env.baseURL+"/oauth2/userinfo", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+access)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var info map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))

	assert.Equal(t, "ui-member@example.com", info["email"])
	groups, _ := info["groups"].([]any)
	assert.ElementsMatch(t, []any{"member"}, groups)
	assert.NotEmpty(t, info["id"])
}

// TestOAuth2_UserinfoLeadership asserts a leadership member's userinfo has the
// "admin" group in addition to "member".
func TestOAuth2_UserinfoLeadership(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "ui-lead@example.com",
		WithConfirmed(), WithNonBillable(), WithLeadership(), WithName("UI Lead"))

	code := fetchAuthCode(t, env, memberID, "http://127.0.0.1/cb", "s4")
	status, body := exchangeCodeForToken(t, env, code, "myclient", "")
	require.Equal(t, 200, status)
	access := body["access_token"].(string)

	req, err := http.NewRequest("GET", env.baseURL+"/oauth2/userinfo", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+access)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var info map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))

	groups, _ := info["groups"].([]any)
	assert.ElementsMatch(t, []any{"member", "admin"}, groups)
}

// TestOAuth2_UserinfoRejectsConwayAudience verifies a Conway *session* JWT
// (signed by authIssuer, audience "conway") cannot be used as a Bearer token
// at /oauth2/userinfo. Conway uses dual issuers exactly so a session token
// can't escape into the OAuth surface — userinfo verifies with the OAuth
// issuer's key, so a session token will fail signature verification.
func TestOAuth2_UserinfoRejectsConwayAudience(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "session-escape@example.com",
		WithConfirmed(), WithNonBillable())

	// This is a perfectly valid Conway session token (audience: conway,
	// signed by authIssuer). It MUST NOT be accepted at /oauth2/userinfo.
	sessionTok := generateAuthToken(t, env, memberID)

	req, err := http.NewRequest("GET", env.baseURL+"/oauth2/userinfo", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+sessionTok)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.GreaterOrEqual(t, resp.StatusCode, 400,
		"session JWT must not be accepted as an OAuth bearer token")
	assert.NotEqual(t, 200, resp.StatusCode)
}

// TestOAuth2_RedirectURIMismatchedRootDomain ensures /authorize refuses to
// redirect to a URL whose root domain differs from the server's.
//
// The test server runs on 127.0.0.1:<port>, so rootDomain(self) == "0.1".
// A redirect_uri host of "example.com" yields rootDomain "example.com" — a
// mismatch — and must be refused (no Location, no code leak).
func TestOAuth2_RedirectURIMismatchedRootDomain(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "external-redir@example.com",
		WithConfirmed(), WithNonBillable())
	tok := generateAuthToken(t, env, memberID)

	q := url.Values{}
	q.Set("redirect_uri", "http://example.com/cb")
	q.Set("state", "x")
	q.Set("response_type", "code")

	req, err := http.NewRequest("GET", env.baseURL+"/oauth2/authorize?"+q.Encode(), nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "token", Value: tok})

	resp, err := noFollowClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Must NOT be a 302 redirect that leaks a code to example.com.
	assert.NotEqual(t, http.StatusFound, resp.StatusCode,
		"server must refuse to redirect to a foreign root domain")
	assert.GreaterOrEqual(t, resp.StatusCode, 400)
	assert.Empty(t, resp.Header.Get("Location"),
		"no Location header should be set when redirect_uri is rejected")
}
