package auth

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/TheLab-ms/conway/engine"
)

// PasskeyUser implements the webauthn.User interface for Conway members.
type PasskeyUser struct {
	ID          int64
	Email       string
	DisplayName string
	Credentials []webauthn.Credential
}

func (u *PasskeyUser) WebAuthnID() []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(u.ID))
	return buf
}

func (u *PasskeyUser) WebAuthnName() string {
	return u.Email
}

func (u *PasskeyUser) WebAuthnDisplayName() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Email
}

func (u *PasskeyUser) WebAuthnCredentials() []webauthn.Credential {
	return u.Credentials
}

// newWebAuthn creates a configured WebAuthn instance.
func newWebAuthn(self *url.URL) (*webauthn.WebAuthn, error) {
	origin := self.Scheme + "://" + self.Host
	return webauthn.New(&webauthn.Config{
		RPID:          self.Hostname(),
		RPDisplayName: "TheLab.ms Makerspace",
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationPreferred,
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
		},
		AttestationPreference: protocol.PreferNoAttestation,
	})
}

// attachPasskeyRoutes registers all passkey-related HTTP routes.
func (m *Module) attachPasskeyRoutes(router *engine.Router) {
	// Registration endpoints (require authentication)
	router.HandleFunc("POST /passkey/register/begin", m.WithAuthn(m.handlePasskeyRegisterBegin))
	router.HandleFunc("POST /passkey/register/finish", m.WithAuthn(m.handlePasskeyRegisterFinish))
	router.HandleFunc("GET /passkey/list", m.WithAuthn(m.handlePasskeyList))
	router.HandleFunc("DELETE /passkey/{id}", m.WithAuthn(m.handlePasskeyDelete))
	router.HandleFunc("POST /passkey/dismiss-prompt", m.WithAuthn(m.handlePasskeyDismissPrompt))

	// Login endpoints (public)
	router.HandleFunc("POST /login/passkey/begin", m.handlePasskeyLoginBegin)
	router.HandleFunc("POST /login/passkey/finish", m.handlePasskeyLoginFinish)
}

// handlePasskeyRegisterBegin starts the passkey registration ceremony.
func (m *Module) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	user := GetUserMeta(r.Context())

	creds, err := m.loadUserCredentials(r.Context(), user.ID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Get display name from database
	var displayName string
	m.db.QueryRowContext(r.Context(), "SELECT name FROM members WHERE id = ?", user.ID).Scan(&displayName)

	passkeyUser := &PasskeyUser{
		ID:          user.ID,
		Email:       user.Email,
		DisplayName: displayName,
		Credentials: creds,
	}

	// Exclude existing credentials to prevent duplicates
	excludeList := make([]protocol.CredentialDescriptor, len(creds))
	for i, c := range creds {
		excludeList[i] = protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: c.ID,
		}
	}

	options, session, err := m.webauthn.BeginRegistration(passkeyUser,
		webauthn.WithExclusions(excludeList),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
	)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	sessionID, err := m.storeWebAuthnSession(r.Context(), session)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "webauthn_session",
		Value:    sessionID,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		Secure:   strings.Contains(m.self.Scheme, "s"),
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(options)
}

// handlePasskeyRegisterFinish completes the passkey registration ceremony.
func (m *Module) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	user := GetUserMeta(r.Context())

	session, err := m.getWebAuthnSession(r)
	if err != nil {
		engine.ClientError(w, "Session Expired", "Registration session expired, please try again", http.StatusBadRequest)
		return
	}

	creds, err := m.loadUserCredentials(r.Context(), user.ID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	var displayName string
	m.db.QueryRowContext(r.Context(), "SELECT name FROM members WHERE id = ?", user.ID).Scan(&displayName)

	passkeyUser := &PasskeyUser{
		ID:          user.ID,
		Email:       user.Email,
		DisplayName: displayName,
		Credentials: creds,
	}

	credential, err := m.webauthn.FinishRegistration(passkeyUser, *session, r)
	if err != nil {
		engine.ClientError(w, "Registration Failed", err.Error(), http.StatusBadRequest)
		return
	}

	// Generate a default name based on count
	var count int
	m.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM passkey_credentials WHERE member_id = ?", user.ID).Scan(&count)
	name := fmt.Sprintf("Passkey %d", count+1)

	err = m.storeCredential(r.Context(), user.ID, credential, name)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	m.deleteWebAuthnSession(r)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// handlePasskeyLoginBegin starts the passkey login ceremony (discoverable credentials).
func (m *Module) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	m.authLimiter.Wait(r.Context())

	options, session, err := m.webauthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationPreferred),
	)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	sessionID, err := m.storeWebAuthnSession(r.Context(), session)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "webauthn_session",
		Value:    sessionID,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		Secure:   strings.Contains(m.self.Scheme, "s"),
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(options)
}

// handlePasskeyLoginFinish completes the passkey login ceremony.
func (m *Module) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	m.authLimiter.Wait(r.Context())

	session, err := m.getWebAuthnSession(r)
	if err != nil {
		engine.ClientError(w, "Session Expired", "Login session expired, please try again", http.StatusBadRequest)
		return
	}

	// Handler to look up user by credential
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		if len(userHandle) < 8 {
			return nil, fmt.Errorf("invalid user handle")
		}
		memberID := int64(binary.BigEndian.Uint64(userHandle))

		creds, err := m.loadUserCredentials(r.Context(), memberID)
		if err != nil {
			return nil, err
		}

		var email, name string
		err = m.db.QueryRowContext(r.Context(),
			"SELECT email, name FROM members WHERE id = ?", memberID).Scan(&email, &name)
		if err != nil {
			return nil, err
		}

		return &PasskeyUser{
			ID:          memberID,
			Email:       email,
			DisplayName: name,
			Credentials: creds,
		}, nil
	}

	credential, err := m.webauthn.FinishDiscoverableLogin(handler, *session, r)
	if err != nil {
		engine.ClientError(w, "Login Failed", "Passkey verification failed", http.StatusBadRequest)
		return
	}

	// Update sign count
	m.updateCredentialSignCount(r.Context(), credential.ID, credential.Authenticator.SignCount)

	// Look up member ID from credential
	var memberID int64
	err = m.db.QueryRowContext(r.Context(),
		"SELECT member_id FROM passkey_credentials WHERE credential_id = ?",
		credential.ID).Scan(&memberID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	m.deleteWebAuthnSession(r)

	// Complete login
	callback := r.URL.Query().Get("callback_uri")
	m.completePasskeyLogin(w, r, memberID, callback)
}

// completePasskeyLogin issues a session token for the authenticated user.
func (m *Module) completePasskeyLogin(w http.ResponseWriter, r *http.Request, memberID int64, callback string) {
	// Mark as confirmed if not already
	m.db.ExecContext(r.Context(), "UPDATE members SET confirmed = true WHERE id = ? AND confirmed = false", memberID)

	exp := time.Now().Add(time.Hour * 24 * 30)
	sessionToken, err := m.tokens.Sign(&jwt.RegisteredClaims{
		Issuer:    "conway",
		Subject:   strconv.FormatInt(memberID, 10),
		Audience:  jwt.ClaimStrings{"conway"},
		ExpiresAt: &jwt.NumericDate{Time: exp},
	})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    sessionToken,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
		Secure:   strings.Contains(m.self.Scheme, "s"),
	})

	if callback == "" {
		callback = "/"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success":  true,
		"redirect": callback,
	})
}

// handlePasskeyList returns the user's registered passkeys.
func (m *Module) handlePasskeyList(w http.ResponseWriter, r *http.Request) {
	user := GetUserMeta(r.Context())

	rows, err := m.db.QueryContext(r.Context(), `
		SELECT id, name, created, last_used
		FROM passkey_credentials WHERE member_id = ?
		ORDER BY created DESC`, user.ID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	defer rows.Close()

	type passkey struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Created  int64  `json:"created"`
		LastUsed *int64 `json:"last_used,omitempty"`
	}

	passkeys := []passkey{}
	for rows.Next() {
		var p passkey
		if err := rows.Scan(&p.ID, &p.Name, &p.Created, &p.LastUsed); err != nil {
			engine.SystemError(w, err.Error())
			return
		}
		passkeys = append(passkeys, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(passkeys)
}

// handlePasskeyDelete removes a passkey.
func (m *Module) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	user := GetUserMeta(r.Context())
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "Invalid passkey ID", http.StatusBadRequest)
		return
	}

	result, err := m.db.ExecContext(r.Context(),
		"DELETE FROM passkey_credentials WHERE id = ? AND member_id = ?", id, user.ID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		engine.ClientError(w, "Not Found", "Passkey not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// handlePasskeyDismissPrompt marks the passkey prompt as dismissed.
func (m *Module) handlePasskeyDismissPrompt(w http.ResponseWriter, r *http.Request) {
	user := GetUserMeta(r.Context())

	_, err := m.db.ExecContext(r.Context(),
		"UPDATE members SET passkey_prompt_dismissed = 1 WHERE id = ?", user.ID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// Database helper functions

func (m *Module) loadUserCredentials(ctx context.Context, memberID int64) ([]webauthn.Credential, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT credential_id, public_key, attestation_type, transport, aaguid, sign_count, clone_warning
		FROM passkey_credentials WHERE member_id = ?`, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []webauthn.Credential
	for rows.Next() {
		var credID, pubKey, aaguid []byte
		var attestationType, transportJSON string
		var signCount uint32
		var cloneWarning bool

		err := rows.Scan(&credID, &pubKey, &attestationType, &transportJSON, &aaguid, &signCount, &cloneWarning)
		if err != nil {
			return nil, err
		}

		var transports []protocol.AuthenticatorTransport
		if transportJSON != "" {
			json.Unmarshal([]byte(transportJSON), &transports)
		}

		creds = append(creds, webauthn.Credential{
			ID:              credID,
			PublicKey:       pubKey,
			AttestationType: attestationType,
			Transport:       transports,
			Authenticator: webauthn.Authenticator{
				AAGUID:       aaguid,
				SignCount:    signCount,
				CloneWarning: cloneWarning,
			},
		})
	}
	return creds, rows.Err()
}

func (m *Module) storeCredential(ctx context.Context, memberID int64, cred *webauthn.Credential, name string) error {
	transportJSON, _ := json.Marshal(cred.Transport)
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO passkey_credentials
		(member_id, credential_id, public_key, attestation_type, transport, aaguid, sign_count, name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		memberID, cred.ID, cred.PublicKey, cred.AttestationType,
		string(transportJSON), cred.Authenticator.AAGUID, cred.Authenticator.SignCount, name)
	return err
}

func (m *Module) storeWebAuthnSession(ctx context.Context, session *webauthn.SessionData) (string, error) {
	id := uuid.New().String()
	data, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	expiresAt := time.Now().Add(5 * time.Minute).Unix()
	_, err = m.db.ExecContext(ctx,
		"INSERT INTO webauthn_sessions (id, session_data, expires_at) VALUES (?, ?, ?)",
		id, data, expiresAt)
	return id, err
}

func (m *Module) getWebAuthnSession(r *http.Request) (*webauthn.SessionData, error) {
	cookie, err := r.Cookie("webauthn_session")
	if err != nil {
		return nil, err
	}

	var data []byte
	var expiresAt int64
	err = m.db.QueryRowContext(r.Context(),
		"SELECT session_data, expires_at FROM webauthn_sessions WHERE id = ?",
		cookie.Value).Scan(&data, &expiresAt)
	if err != nil {
		return nil, err
	}

	if time.Now().Unix() > expiresAt {
		m.db.Exec("DELETE FROM webauthn_sessions WHERE id = ?", cookie.Value)
		return nil, fmt.Errorf("session expired")
	}

	var session webauthn.SessionData
	err = json.Unmarshal(data, &session)
	return &session, err
}

func (m *Module) deleteWebAuthnSession(r *http.Request) {
	cookie, err := r.Cookie("webauthn_session")
	if err != nil {
		return
	}
	m.db.Exec("DELETE FROM webauthn_sessions WHERE id = ?", cookie.Value)

	http.SetCookie(nil, &http.Cookie{
		Name:   "webauthn_session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}

func (m *Module) updateCredentialSignCount(ctx context.Context, credID []byte, count uint32) {
	m.db.ExecContext(ctx,
		"UPDATE passkey_credentials SET sign_count = ?, last_used = unixepoch() WHERE credential_id = ?",
		count, credID)
}

// HasPasskeys checks if a member has any registered passkeys.
func (m *Module) HasPasskeys(ctx context.Context, memberID int64) (bool, error) {
	var count int
	err := m.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM passkey_credentials WHERE member_id = ?", memberID).Scan(&count)
	return count > 0, err
}

// ShouldShowPasskeyPrompt checks if we should prompt the user to add a passkey.
func (m *Module) ShouldShowPasskeyPrompt(ctx context.Context, memberID int64) (bool, error) {
	var dismissed bool
	err := m.db.QueryRowContext(ctx,
		"SELECT passkey_prompt_dismissed FROM members WHERE id = ?", memberID).Scan(&dismissed)
	if err != nil {
		return false, err
	}
	if dismissed {
		return false, nil
	}

	hasPasskeys, err := m.HasPasskeys(ctx, memberID)
	if err != nil {
		return false, err
	}
	return !hasPasskeys, nil
}
