package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/TheLab-ms/conway/db"
	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoginIntegration(t *testing.T) {
	a := newTestApp(t)

	// Fake email handler
	emails := make(chan string)
	emailCount := atomic.Int32{}
	a.Auth.Mailer = func(ctx context.Context, to, subj string, msg []byte) bool {
		if emailCount.Add(1) == 1 {
			return false // return an error to prove it's retried eventually
		}

		emails <- string(msg)
		return true
	}

	start(t, a.App)

	var client *http.Client
	for i := 0; i < 2; i++ {
		// Run the test twice to cover both registration and (re)login
		t.Run(fmt.Sprintf("iteration-%d", i), func(t *testing.T) {
			jar, err := cookiejar.New(&cookiejar.Options{})
			require.NoError(t, err)
			client = &http.Client{
				CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
				Jar:           jar,
			}

			// Try to hit an authorized endpoint without a token
			resp, err := client.Get(a.URL + "/whoami")
			require.NoError(t, err)
			resp.Body.Close()
			assert.Equal(t, 302, resp.StatusCode)
			loginPageURL := resp.Header.Get("Location")

			// Enter an email address to start login flow
			resp, err = client.Post(a.URL+loginPageURL, "application/x-www-form-urlencoded", bytes.NewBufferString("email=foobar&callback_uri=/barbaz"))
			require.NoError(t, err)
			resp.Body.Close()
			assert.Equal(t, 303, resp.StatusCode)
			enterCodePageURL := resp.Header.Get("Location")

			// Extract the code from the login email
			msg := <-emails
			code := regexp.MustCompile(`\b\d{6}\b`).FindString(msg)
			t.Logf("got login code: %s", code)

			// Complete the login flow by inputting an INVALID code
			resp, err = client.Post(a.URL+enterCodePageURL, "application/x-www-form-urlencoded", bytes.NewBufferString("code=12345678"))
			require.NoError(t, err)
			resp.Body.Close()
			require.Len(t, resp.Cookies(), 0) // no token granted

			// Use the correct code
			resp, err = client.Post(a.URL+enterCodePageURL, "application/x-www-form-urlencoded", bytes.NewBufferString("code="+code))
			require.NoError(t, err)
			resp.Body.Close()
			assert.Equal(t, 302, resp.StatusCode)
			assert.Equal(t, "/barbaz", resp.Header.Get("Location"))
			require.Len(t, resp.Cookies(), 1)

			// Hit the whoami endpoint to confirm the identity
			resp, err = client.Get(a.URL + "/whoami")
			require.NoError(t, err)
			assert.Equal(t, 200, resp.StatusCode)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			assert.Equal(t, "{\"Email\":\"foobar\",\"ActiveMember\":false,\"Leadership\":false}\n", string(body))

			// The code cannot be used again
			resp, err = client.Post(a.URL+enterCodePageURL, "application/x-www-form-urlencoded", bytes.NewBufferString("code="+code))
			require.NoError(t, err)
			resp.Body.Close()
			require.Len(t, resp.Cookies(), 0) // no token granted

			// The session can be used by external oauth clients
			resp, err = client.Get(a.URL + "/oauth2/authorize?redirect_uri=https://localhost/foobar")
			require.NoError(t, err)
			assert.Equal(t, 302, resp.StatusCode)
			assert.Contains(t, resp.Header.Get("Location"), "https://localhost/foobar")

			u, err := url.Parse(resp.Header.Get("Location"))
			require.NoError(t, err)
			oauthCode := u.Query().Get("code")
			assert.NotEmpty(t, oauthCode)

			// Exchange the code for an access token (standard oauth2 flow)
			resp, err = client.Post(a.URL+"/oauth2/token?code="+oauthCode, "", nil)
			require.NoError(t, err)
			assert.Equal(t, 200, resp.StatusCode)

			m := map[string]any{}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			tok := m["access_token"].(string)
			assert.NotEmpty(t, tok)

			// Get the user's oauth2 metadata
			req, err := http.NewRequest("GET", a.URL+"/oauth2/userinfo", nil)
			require.NoError(t, err)
			req.Header.Add("Authorization", "Bearer "+tok)
			resp, err = client.Do(req)
			require.NoError(t, err)
			assert.Equal(t, 200, resp.StatusCode)

			m = map[string]any{}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			assert.Equal(t, "6b86b273-ff34-fce1-9d6b-804eff5a3f57", m["id"].(string))
			assert.Equal(t, "foobar", m["email"].(string))
			assert.Nil(t, m["groups"])
			assert.Equal(t, "", m["name"].(string))
		})
	}

	// Delete the user from the DB, prove the token is invalidated
	_, err := a.Exec("DELETE FROM members")
	require.NoError(t, err)

	resp, err := client.Get(a.URL + "/whoami")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 302, resp.StatusCode)
}

type testApp struct {
	*engine.App
	*sql.DB

	URL  string
	Auth *auth.Module
}

func newTestApp(t *testing.T) *testApp {
	addr, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr.Close()

	db := db.NewTest(t)
	a, auth, err := newApp(db, addr.Addr().String(), "", &url.URL{Host: "localhost"}, nil)
	require.NoError(t, err)

	return &testApp{
		App:  a,
		DB:   db,
		URL:  fmt.Sprintf("http://%s", addr.Addr().String()),
		Auth: auth,
	}
}

func start(t *testing.T, a *engine.App) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
	})

	go func() {
		defer close(done)
		a.Run(ctx)
	}()
}
