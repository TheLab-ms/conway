// Package oauthlogin extracts the common Discord/Google OAuth2 login flow.
//
// Both providers had ~120 lines of nearly-identical code for state JWT
// issuance, code exchange, error handling, and find-or-signup-confirm. This
// package owns that machinery and exposes a small Provider interface for the
// per-provider differences (audience name, OAuth config, user-info fetch,
// existing-member lookup, post-login linking, signup tag).
//
// Adding a third provider becomes ~80 lines of provider glue.
package oauthlogin

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/members/memberdb"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// UserInfo is the minimal information a Provider must extract from the
// upstream OAuth2 response to participate in this flow.
type UserInfo struct {
	// Email is the verified email address for the user. Required; an empty
	// value will cause the flow to return a 400 error.
	Email string
	// ProviderID is the provider-specific stable identifier (e.g. Discord
	// user id, Google sub claim). May be empty for providers that don't
	// expose one.
	ProviderID string
}

// Provider supplies the per-provider variation in an OAuth2 login flow.
//
// All methods receive a request context. Implementations should be
// stateless and re-entrant.
type Provider interface {
	// Name is the short, lowercase identifier used to namespace the JWT
	// audience ("<name>-login") and routes ("/login/<name>"). Must be a
	// stable, URL-safe slug.
	Name() string

	// OAuthConfig returns the OAuth2 client configuration for the login
	// flow. Returning an error causes the handlers to respond 503
	// "not configured".
	OAuthConfig(ctx context.Context) (*oauth2.Config, error)

	// FetchUser exchanges a successful OAuth2 token for the user's
	// identity. The OAuth2 config is passed back so providers can use
	// oc.Client(ctx, token) without rebuilding it.
	FetchUser(ctx context.Context, token *oauth2.Token, oc *oauth2.Config) (*UserInfo, error)

	// LookupExistingMember resolves an existing member id for the given
	// upstream user, if any. Implementations should consult provider-
	// specific identity columns first (e.g. discord_user_id) and may also
	// fall back to email. Return (0, false, nil) if no match.
	LookupExistingMember(ctx context.Context, db *sql.DB, info *UserInfo) (memberID int64, found bool, err error)

	// LinkAccount is invoked after a successful login (whether for an
	// existing or newly-created member) so the provider can write any
	// provider-specific columns (e.g. discord_user_id, discord_email).
	// May be a no-op.
	LinkAccount(ctx context.Context, db *sql.DB, memberID int64, info *UserInfo) error

	// SignupProviderTag is the opaque "provider" string passed to the
	// signup confirmation page. The auth module decodes this on confirm
	// to know which provider initiated signup.
	SignupProviderTag(info *UserInfo) string
}

// LoginCompleteFunc is called to finish a successful login by issuing the
// session cookie and redirecting to callbackURI.
type LoginCompleteFunc func(w http.ResponseWriter, r *http.Request, memberID int64, callbackURI string)

// SignupConfirmFunc is called when no member exists for the upstream user.
// It typically renders an "Are you sure you want to create an account?" page
// whose POST handler calls back into the auth module.
type SignupConfirmFunc func(w http.ResponseWriter, r *http.Request, email, providerTag, callbackURI string)

// Deps bundles the dependencies the handlers need from the host.
type Deps struct {
	DB             *sql.DB
	StateTokIssuer *engine.TokenIssuer
	LoginComplete  LoginCompleteFunc
	SignupConfirm  SignupConfirmFunc // may be nil; flow will create directly
}

// Handlers returns the start and callback HTTP handlers for the given
// provider.
//
//	start:    GET /login/<provider>
//	callback: GET /login/<provider>/callback
//
// Both handlers self-contain all error rendering and redirects.
func Handlers(p Provider, deps Deps) (start, callback http.HandlerFunc) {
	if deps.DB == nil || deps.StateTokIssuer == nil || deps.LoginComplete == nil {
		panic("oauthlogin: Deps.DB, StateTokIssuer, and LoginComplete are required")
	}
	return start1(p, deps), callback1(p, deps)
}

func audienceName(p Provider) string { return p.Name() + "-login" }

// notConfiguredErr signals OAuthConfig is missing — surface as 503, not 500.
const notConfiguredMsg = "is not configured"

func start1(p Provider, deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		oauthConf, err := p.OAuthConfig(r.Context())
		if err != nil {
			engine.ClientError(w, "Not Available", capitalize(p.Name())+" login "+notConfiguredMsg, http.StatusServiceUnavailable)
			return
		}

		callbackURI := r.URL.Query().Get("callback_uri")

		state, err := deps.StateTokIssuer.Sign(&jwt.RegisteredClaims{
			Issuer:    callbackURI,
			Audience:  jwt.ClaimStrings{audienceName(p)},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
		})
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}

		http.Redirect(w, r, oauthConf.AuthCodeURL(state), http.StatusTemporaryRedirect)
	}
}

func callback1(p Provider, deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Verify state JWT
		stateClaims, err := deps.StateTokIssuer.Verify(r.URL.Query().Get("state"))
		if err != nil || len(stateClaims.Audience) == 0 || stateClaims.Audience[0] != audienceName(p) {
			engine.ClientError(w, "Invalid State", "The login state is invalid or expired. Please try again.", http.StatusBadRequest)
			return
		}
		callbackURI := stateClaims.Issuer

		// User-denied or upstream OAuth error.
		if r.URL.Query().Get("error") != "" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		oauthConf, err := p.OAuthConfig(r.Context())
		if err != nil {
			engine.ClientError(w, "Not Available", capitalize(p.Name())+" login "+notConfiguredMsg, http.StatusServiceUnavailable)
			return
		}

		token, err := oauthConf.Exchange(r.Context(), r.URL.Query().Get("code"))
		if err != nil {
			engine.ClientError(w, "Login Failed", capitalize(p.Name())+" login failed. Please try again.", http.StatusBadRequest)
			return
		}

		info, err := p.FetchUser(r.Context(), token, oauthConf)
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}
		if info.Email == "" {
			engine.ClientError(w, "Email Required",
				"Your "+capitalize(p.Name())+" account does not have a verified email address. Please use email login instead.",
				http.StatusBadRequest)
			return
		}

		memberID, found, err := p.LookupExistingMember(r.Context(), deps.DB, info)
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}
		if !found {
			if deps.SignupConfirm != nil {
				deps.SignupConfirm(w, r, info.Email, p.SignupProviderTag(info), callbackURI)
				return
			}
			memberID, err = memberdb.FindOrCreateByEmail(r.Context(), deps.DB, info.Email)
			if err != nil {
				engine.SystemError(w, err.Error())
				return
			}
		}

		if err := p.LinkAccount(r.Context(), deps.DB, memberID, info); err != nil {
			// Linking failures are logged at the provider's discretion;
			// we don't block login for them.
			_ = err
		}

		deps.LoginComplete(w, r, memberID, callbackURI)
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	return s
}
