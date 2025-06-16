package auth

import (
	"net/url"
	"testing"

	"github.com/TheLab-ms/conway/engine/testutil"
)

func TestRenderLoginPage(t *testing.T) {
	tests := []struct {
		name        string
		callbackURI string
		tso         *TurnstileOptions
		fixtureName string
		description string
	}{
		{
			name:        "basic_login",
			callbackURI: "/dashboard",
			tso:         nil,
			fixtureName: "_basic",
			description: "Basic login form without Turnstile",
		},
		{
			name:        "with_turnstile",
			callbackURI: "/admin",
			tso: &TurnstileOptions{
				SiteKey: "0x4AAAAAAABkMYinukE8nzKr",
			},
			fixtureName: "_with_turnstile",
			description: "Login form with Turnstile CAPTCHA",
		},
		{
			name:        "empty_callback",
			callbackURI: "",
			tso:         nil,
			fixtureName: "_empty_callback",
			description: "Login form with empty callback URI",
		},
		{
			name:        "complex_callback",
			callbackURI: "/admin/members?search=test&page=2",
			tso: &TurnstileOptions{
				SiteKey: "test-site-key-123",
			},
			fixtureName: "_complex_callback",
			description: "Login form with complex callback URI and Turnstile",
		},
		{
			name:        "root_callback",
			callbackURI: "/",
			tso:         nil,
			fixtureName: "_root_callback",
			description: "Login form with root callback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderLoginPage(tt.callbackURI, tt.tso)
			testutil.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}

func TestRenderLoginSentPage(t *testing.T) {
	component := renderLoginSentPage()
	testutil.RenderSnapshotWithName(t, component, "")
}

func TestRenderLoginEmail(t *testing.T) {
	tests := []struct {
		name        string
		self        *url.URL
		token       string
		callback    string
		fixtureName string
		description string
	}{
		{
			name:        "basic_email",
			self:        &url.URL{Scheme: "https", Host: "conway.thelab.ms"},
			token:       "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			callback:    "/dashboard",
			fixtureName: "_basic",
			description: "Basic login email with token and callback",
		},
		{
			name:        "localhost_email",
			self:        &url.URL{Scheme: "http", Host: "localhost:8080"},
			token:       "test-token-123",
			callback:    "/admin",
			fixtureName: "_localhost",
			description: "Login email for localhost development",
		},
		{
			name:        "empty_callback",
			self:        &url.URL{Scheme: "https", Host: "example.com"},
			token:       "short-token",
			callback:    "",
			fixtureName: "_empty_callback",
			description: "Login email with empty callback",
		},
		{
			name:        "complex_callback",
			self:        &url.URL{Scheme: "https", Host: "conway.thelab.ms"},
			token:       "complex-token-with-special-chars",
			callback:    "/members?filter=active&sort=name",
			fixtureName: "_complex_callback",
			description: "Login email with complex callback URI",
		},
		{
			name:        "root_callback",
			self:        &url.URL{Scheme: "https", Host: "conway.thelab.ms"},
			token:       "root-token",
			callback:    "/",
			fixtureName: "_root_callback",
			description: "Login email with root callback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderLoginEmail(tt.self, tt.token, tt.callback)
			testutil.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}
