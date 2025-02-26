package oauth2

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"
)

const tokenValidity = time.Hour * 8

type Module struct {
	db   *sql.DB
	self *url.URL
	auth *auth.Module
}

func New(db *sql.DB, self *url.URL, am *auth.Module) *Module {
	return &Module{db: db, self: self, auth: am}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("POST", "/oauth2/token", m.handleOauthToken)
	router.Handle("GET", "/oauth2/authorize", router.WithAuth(m.handleOauthAuthorization))
	router.Handle("GET", "/oauth2/userinfo", m.handleOauthUserInfo)

	router.Handle("GET", "/oauth2/jwks", func(r *http.Request, ps httprouter.Params) engine.Response {
		return engine.JSON(&map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"use": "sig",
				"kid": "1",
				"alg": "RS512",
				"n":   base64.RawURLEncoding.EncodeToString(m.auth.SigningKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(m.auth.SigningKey.E)).Bytes()),
			}},
		})
	})

	router.Handle("GET", "/.well-known/openid-configuration", func(r *http.Request, ps httprouter.Params) engine.Response {
		return engine.JSON(&map[string]any{
			"issuer":                                m.self.String(),
			"authorization_endpoint":                m.self.String() + "/oauth2/authorize",
			"token_endpoint":                        m.self.String() + "/oauth2/token",
			"userinfo_endpoint":                     m.self.String() + "/oauth2/userinfo",
			"jwks_uri":                              m.self.String() + "/oauth2/jwks",
			"id_token_signing_alg_values_supported": []string{"RS512"},
		})
	})
}

// handleOauthAuthorization starts the external oauth flow by generating a "code" for the flow.
// The code is a special JWT instead of state in the db to avoid complexity.
func (s *Module) handleOauthAuthorization(r *http.Request, p httprouter.Params) engine.Response {
	email := auth.GetUserMeta(r.Context())

	codeTok := jwt.NewWithClaims(jwt.SigningMethodRS512, &jwt.RegisteredClaims{
		Issuer:    s.self.String(),
		Subject:   email.Email,
		Audience:  jwt.ClaimStrings{"conway-oauth"},
		ExpiresAt: &jwt.NumericDate{Time: time.Now().Add(time.Minute)},
	})
	codeToken, err := codeTok.SignedString(s.auth.SigningKey)
	if err != nil {
		return engine.Errorf("signing code jwt: %s", err)
	}

	// Set the code query param on the redirect URI
	redirectURI, err := url.Parse(r.FormValue("redirect_uri"))
	if err != nil {
		return engine.Errorf("invalid redirect uri: %s", err)
	}
	if rootDomain(redirectURI) != rootDomain(s.self) {
		return engine.Errorf("refusing to redirect to external url: %s (trusted: %s)", rootDomain(redirectURI), rootDomain(s.self))
	}
	q := redirectURI.Query()
	q.Add("code", codeToken)
	q.Add("state", r.URL.Query().Get("state"))
	redirectURI.RawQuery = q.Encode()

	return engine.Redirect(redirectURI.String(), http.StatusFound)
}

// handleOauthToken exchanges a code generated by handleOauthAuthorization for an access token.
func (m *Module) handleOauthToken(r *http.Request, p httprouter.Params) engine.Response {
	code := r.FormValue("code")

	// Anyone can request tokens for any audience EXCEPT the internal "conway" audience.
	// This means clients should treat oauth tokens as relatively low trust which is fine for this use-case.
	clientID, _, _ := r.BasicAuth()
	if clientID == "conway" {
		return engine.ClientErrorf(400, "cannot create token for reserved audience")
	}

	claims := &jwt.RegisteredClaims{}
	tok, err := jwt.ParseWithClaims(code, claims, func(token *jwt.Token) (interface{}, error) { return &m.auth.SigningKey.PublicKey, nil })
	if err != nil || !tok.Valid || len(claims.Audience) == 0 || claims.Audience[0] != "conway-oauth" {
		return engine.Errorf("invalid code: %s", err)
	}

	// Make sure member is still active, map email->id
	var memberID int64
	err = m.db.QueryRowContext(r.Context(), "SELECT id FROM members WHERE email = ? LIMIT 1", claims.Subject).Scan(&memberID)
	if err != nil {
		return engine.Errorf("getting member: %s", err)
	}

	// Generate an access JWT
	token, err := m.signToken(memberID, clientID)
	if err != nil {
		return engine.Errorf("signing jwt: %s", err)
	}

	// Return the standard oauth token response
	resp := &oauthTokenResponse{
		IDToken:     token,
		AccessToken: token,
		Type:        "Bearer",
		ExpiresIn:   int(tokenValidity.Seconds()),
	}
	return engine.JSON(resp)
}

func (m *Module) signToken(memberID int64, clientID string) (string, error) {
	accessTok := jwt.NewWithClaims(jwt.SigningMethodRS512, &jwt.RegisteredClaims{
		Issuer:    m.self.String(),
		Subject:   strconv.FormatInt(memberID, 10),
		Audience:  jwt.ClaimStrings{clientID},
		ExpiresAt: &jwt.NumericDate{Time: time.Now().Add(tokenValidity)},
	})
	return accessTok.SignedString(m.auth.SigningKey)
}

type oauthTokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	Type        string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// handleOauthUserInfo returns metadata for the user represented by the given bearer token.
// This is commonly used by oauth2 clients to get current email, groups, etc. for old tokens.
func (m *Module) handleOauthUserInfo(r *http.Request, p httprouter.Params) engine.Response {
	tokStr := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	// Parse the JWT
	claims := &jwt.RegisteredClaims{}
	tok, err := jwt.ParseWithClaims(tokStr, claims, func(token *jwt.Token) (interface{}, error) { return &m.auth.SigningKey.PublicKey, nil })
	if err != nil || !tok.Valid {
		return engine.Errorf("invalid jwt: %s", err)
	}

	// Get the member from the DB
	var name string
	var email string
	var active bool
	var leadership bool
	err = m.db.QueryRowContext(r.Context(), "SELECT name, email, payment_status IS NOT NULL, leadership FROM members WHERE id = ? LIMIT 1", claims.Subject).Scan(&name, &email, &active, &leadership)
	if err != nil {
		return engine.Errorf("getting user from db: %s", err)
	}

	// Represent their ID as a UUID - just for aesthetics honestly.
	// Ints look weird as user IDs.
	hash := sha256.Sum256([]byte(claims.Subject))
	uid := uuid.Must(uuid.FromBytes(hash[:16]))

	resp := &oauthUserInfoResponse{
		ID:     uid.String(),
		Name:   name,
		Email:  email,
		Groups: []string{},
	}
	if active {
		resp.Groups = append(resp.Groups, "member")
	}
	if leadership {
		resp.Groups = append(resp.Groups, "admin")
	}
	return engine.JSON(resp)
}

type oauthUserInfoResponse struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Email  string   `json:"email"`
	Groups []string `json:"groups"`
}

func rootDomain(u *url.URL) string {
	parts := strings.Split(u.Hostname(), ".")
	if len(parts) < 2 {
		return parts[len(parts)-1]
	}
	return strings.Join(parts[len(parts)-2:], ".")
}
