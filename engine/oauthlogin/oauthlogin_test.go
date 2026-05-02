package oauthlogin_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/oauthlogin"
	"github.com/TheLab-ms/conway/modules/members"
	"golang.org/x/oauth2"
)

// fakeProvider lets tests dial in each step independently.
type fakeProvider struct {
	name        string
	tokenURL    string // when set, OAuthConfig points here for code exchange
	cfgErr      error
	user        *oauthlogin.UserInfo
	fetchErr    error
	lookupID    int64
	lookupFound bool
	lookupErr   error
	tag         string

	linkCalls atomic.Int32
	linkErr   error
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) OAuthConfig(_ context.Context) (*oauth2.Config, error) {
	if p.cfgErr != nil {
		return nil, p.cfgErr
	}
	cfg := &oauth2.Config{
		ClientID:     "id",
		ClientSecret: "secret",
		Endpoint:     oauth2.Endpoint{AuthURL: "https://example.invalid/auth", TokenURL: "https://example.invalid/token"},
		RedirectURL:  "http://localhost/callback",
		Scopes:       []string{"x"},
	}
	if p.tokenURL != "" {
		cfg.Endpoint.AuthURL = p.tokenURL + "/auth"
		cfg.Endpoint.TokenURL = p.tokenURL + "/token"
	}
	return cfg, nil
}

func (p *fakeProvider) FetchUser(_ context.Context, _ *oauth2.Token, _ *oauth2.Config) (*oauthlogin.UserInfo, error) {
	return p.user, p.fetchErr
}

func (p *fakeProvider) LookupExistingMember(_ context.Context, _ *sql.DB, _ *oauthlogin.UserInfo) (int64, bool, error) {
	return p.lookupID, p.lookupFound, p.lookupErr
}

func (p *fakeProvider) LinkAccount(_ context.Context, _ *sql.DB, _ int64, _ *oauthlogin.UserInfo) error {
	p.linkCalls.Add(1)
	return p.linkErr
}

func (p *fakeProvider) SignupProviderTag(_ *oauthlogin.UserInfo) string {
	if p.tag != "" {
		return p.tag
	}
	return p.name
}

// fakeTokenServer returns a successful access_token from POST /token.
func fakeTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type fixture struct {
	deps      oauthlogin.Deps
	db        *sql.DB
	completed *atomic.Int32
	confirmed *atomic.Int32
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db := members.NewTestDB(t)
	iss := engine.NewTokenIssuer(filepath.Join(t.TempDir(), "k.pem"))
	completed := &atomic.Int32{}
	confirmed := &atomic.Int32{}
	return fixture{
		deps: oauthlogin.Deps{
			DB:             db,
			StateTokIssuer: iss,
			LoginComplete: func(w http.ResponseWriter, _ *http.Request, _ int64, _ string) {
				completed.Add(1)
				w.Header().Set("X-Logged-In", "1")
				w.WriteHeader(http.StatusOK)
			},
			SignupConfirm: func(w http.ResponseWriter, _ *http.Request, _, providerTag, _ string) {
				confirmed.Add(1)
				w.Header().Set("X-Confirm-Tag", providerTag)
				w.WriteHeader(http.StatusOK)
			},
		},
		db:        db,
		completed: completed,
		confirmed: confirmed,
	}
}

func mintState(t *testing.T, p oauthlogin.Provider, deps oauthlogin.Deps, callback string) string {
	t.Helper()
	start, _ := oauthlogin.Handlers(p, deps)
	rec := httptest.NewRecorder()
	start(rec, httptest.NewRequest("GET", "/login/"+p.Name()+"?callback_uri="+url.QueryEscape(callback), nil))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("start handler did not redirect: %d %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc.Query().Get("state")
}

func TestStartHandler_NotConfigured(t *testing.T) {
	p := &fakeProvider{name: "fake", cfgErr: errors.New("no config")}
	f := newFixture(t)
	start, _ := oauthlogin.Handlers(p, f.deps)
	rec := httptest.NewRecorder()
	start(rec, httptest.NewRequest("GET", "/login/fake", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestStartHandler_RedirectsToProvider(t *testing.T) {
	p := &fakeProvider{name: "fake"}
	f := newFixture(t)
	start, _ := oauthlogin.Handlers(p, f.deps)
	rec := httptest.NewRecorder()
	start(rec, httptest.NewRequest("GET", "/login/fake?callback_uri=/dashboard", nil))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status=%d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Host != "example.invalid" {
		t.Fatalf("redirected to %v", loc)
	}
	if loc.Query().Get("state") == "" {
		t.Fatalf("missing state param")
	}
}

func TestCallback_BadStateRejected(t *testing.T) {
	p := &fakeProvider{name: "fake"}
	f := newFixture(t)
	_, cb := oauthlogin.Handlers(p, f.deps)
	rec := httptest.NewRecorder()
	cb(rec, httptest.NewRequest("GET", "/login/fake/callback?state=garbage&code=xyz", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestCallback_WrongAudienceRejected(t *testing.T) {
	// State signed for "other-login" must be rejected by the "fake" handler.
	f := newFixture(t)
	other := &fakeProvider{name: "other"}
	state := mintState(t, other, f.deps, "/x")

	p := &fakeProvider{name: "fake"}
	_, cb := oauthlogin.Handlers(p, f.deps)
	rec := httptest.NewRecorder()
	cb(rec, httptest.NewRequest("GET", "/login/fake/callback?state="+state+"&code=xyz", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 cross-provider state rejection, got %d", rec.Code)
	}
}

func TestCallback_UpstreamErrorRedirectsToLogin(t *testing.T) {
	p := &fakeProvider{name: "fake"}
	f := newFixture(t)
	state := mintState(t, p, f.deps, "/dash")

	_, cb := oauthlogin.Handlers(p, f.deps)
	rec := httptest.NewRecorder()
	cb(rec, httptest.NewRequest("GET", "/login/fake/callback?state="+state+"&error=access_denied", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("expected redirect to /login, got %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCallback_SignupConfirmInvokedWhenMemberMissing(t *testing.T) {
	srv := fakeTokenServer(t)
	p := &fakeProvider{
		name:        "fake",
		tokenURL:    srv.URL,
		user:        &oauthlogin.UserInfo{Email: "new@example.com", ProviderID: "x"},
		lookupFound: false,
		tag:         "fake:x",
	}
	f := newFixture(t)
	state := mintState(t, p, f.deps, "/dash")

	_, cb := oauthlogin.Handlers(p, f.deps)
	rec := httptest.NewRecorder()
	cb(rec, httptest.NewRequest("GET", "/login/fake/callback?state="+state+"&code=ok", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if f.confirmed.Load() != 1 || f.completed.Load() != 0 {
		t.Fatalf("expected signup confirm to fire (confirmed=%d completed=%d)", f.confirmed.Load(), f.completed.Load())
	}
	if got := rec.Header().Get("X-Confirm-Tag"); got != "fake:x" {
		t.Fatalf("expected provider tag in callback, got %q", got)
	}
	if p.linkCalls.Load() != 0 {
		t.Fatalf("LinkAccount must not be called when member doesn't exist yet (got %d calls)", p.linkCalls.Load())
	}
}

func TestCallback_ExistingMemberCompletesAndLinks(t *testing.T) {
	srv := fakeTokenServer(t)
	p := &fakeProvider{
		name:        "fake",
		tokenURL:    srv.URL,
		user:        &oauthlogin.UserInfo{Email: "existing@example.com", ProviderID: "abc"},
		lookupID:    42,
		lookupFound: true,
	}
	f := newFixture(t)
	state := mintState(t, p, f.deps, "/dash")

	_, cb := oauthlogin.Handlers(p, f.deps)
	rec := httptest.NewRecorder()
	cb(rec, httptest.NewRequest("GET", "/login/fake/callback?state="+state+"&code=ok", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if f.completed.Load() != 1 || f.confirmed.Load() != 0 {
		t.Fatalf("completed=%d confirmed=%d", f.completed.Load(), f.confirmed.Load())
	}
	if p.linkCalls.Load() != 1 {
		t.Fatalf("LinkAccount must be called exactly once for existing members, got %d", p.linkCalls.Load())
	}
}

func TestCallback_NoSignupConfirm_FallsBackToCreate(t *testing.T) {
	srv := fakeTokenServer(t)
	p := &fakeProvider{
		name:        "fake",
		tokenURL:    srv.URL,
		user:        &oauthlogin.UserInfo{Email: "fresh@example.com"},
		lookupFound: false,
	}
	f := newFixture(t)
	f.deps.SignupConfirm = nil // explicit: no confirm path

	state := mintState(t, p, f.deps, "/")
	_, cb := oauthlogin.Handlers(p, f.deps)
	rec := httptest.NewRecorder()
	cb(rec, httptest.NewRequest("GET", "/login/fake/callback?state="+state+"&code=ok", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if f.completed.Load() != 1 {
		t.Fatalf("expected login to complete, got completed=%d", f.completed.Load())
	}
	if p.linkCalls.Load() != 1 {
		t.Fatalf("LinkAccount must be called once after auto-create, got %d", p.linkCalls.Load())
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM members WHERE email=?", "fresh@example.com").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected member auto-created, got count=%d", count)
	}
}

func TestCallback_EmptyEmailRejected(t *testing.T) {
	srv := fakeTokenServer(t)
	p := &fakeProvider{
		name:     "fake",
		tokenURL: srv.URL,
		user:     &oauthlogin.UserInfo{Email: ""},
	}
	f := newFixture(t)
	state := mintState(t, p, f.deps, "/")

	_, cb := oauthlogin.Handlers(p, f.deps)
	rec := httptest.NewRecorder()
	cb(rec, httptest.NewRequest("GET", "/login/fake/callback?state="+state+"&code=ok", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
