package main

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

var wauthn *webauthn.WebAuthn

// waTotpPending holds a short-lived record created after WebAuthn credential
// verification when the user has TOTP enrolled. The pending token is consumed
// by webauthnTOTPChallenge to issue a full session without re-doing the ceremony.
type waTotpPending struct {
	UserID    int
	Email     string
	Role      string
	ExpiresAt time.Time
}

var webauthnPendingTOTP sync.Map

// issueWATOTPPending creates a pending TOTP token for a user who passed WebAuthn
// but has TOTP enrolled. Returns 200 with totp_required instead of a full session.
func issueWATOTPPending(w http.ResponseWriter, userID int, email, role string) {
	token, err := generateVerificationToken()
	if err != nil {
		writeError(w, "Could not create pending token", http.StatusInternalServerError)
		return
	}
	webauthnPendingTOTP.Store(token, waTotpPending{
		UserID: userID, Email: email, Role: role,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	go time.AfterFunc(5*time.Minute, func() { webauthnPendingTOTP.Delete(token) })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"totp_required": true,
		"pending_token": token,
	})
}

// initWebAuthn initialises the WebAuthn relying party from config.
// A missing or unparseable BaseURL simply disables the feature.
func initWebAuthn() {
	if config == nil || config.Server.BaseURL == "" {
		return
	}
	u, err := url.Parse(config.Server.BaseURL)
	if err != nil {
		log.Printf("webauthn: cannot parse base_url: %v", err)
		return
	}
	name := config.Server.WebAuthnRPName
	if name == "" {
		name = "Dansal"
	}
	wauthn, err = webauthn.New(&webauthn.Config{
		RPDisplayName: name,
		RPID:          u.Hostname(),
		RPOrigins:     []string{strings.TrimRight(config.Server.BaseURL, "/")},
	})
	if err != nil {
		log.Printf("webauthn: init failed: %v", err)
		wauthn = nil
	}
}

// webauthnUserVerificationOpt returns a LoginOption based on the configured
// webauthn_user_verification setting ("preferred" by default).
func webauthnUserVerificationOpt() webauthn.LoginOption {
	uv := protocol.VerificationPreferred
	if config != nil {
		switch config.Server.WebAuthnUserVerification {
		case "required":
			uv = protocol.VerificationRequired
		case "discouraged":
			uv = protocol.VerificationDiscouraged
		}
	}
	return webauthn.WithUserVerification(uv)
}

// waUser implements webauthn.User for an existing or pending account.
type waUser struct {
	id          []byte
	username    string
	credentials []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return u.id }
func (u *waUser) WebAuthnName() string                       { return u.username }
func (u *waUser) WebAuthnDisplayName() string                { return u.username }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// userHandle encodes a DB user ID as 8 big-endian bytes for use as a WebAuthn user handle.
func userHandle(id int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(id))
	return b
}

// loadWebAuthnUser fetches stored passkey credentials for a user.
func loadWebAuthnUser(userID int, username string) *waUser {
	rows, err := db.Query(
		"SELECT credential_id, public_key, sign_count, aaguid, flags FROM webauthn_credentials WHERE user_id=?", userID,
	)
	if err != nil {
		return &waUser{id: userHandle(userID), username: username}
	}
	defer rows.Close()
	var creds []webauthn.Credential
	for rows.Next() {
		var credID, pubKey []byte
		var aaguid []byte
		var signCount uint32
		var flags byte
		if err := rows.Scan(&credID, &pubKey, &signCount, &aaguid, &flags); err != nil {
			continue
		}
		creds = append(creds, webauthn.Credential{
			ID:        credID,
			PublicKey: pubKey,
			Flags:     webauthn.NewCredentialFlags(protocol.AuthenticatorFlags(flags)),
			Authenticator: webauthn.Authenticator{
				AAGUID:    aaguid,
				SignCount: signCount,
			},
		})
	}
	return &waUser{id: userHandle(userID), username: username, credentials: creds}
}

// saveWASession stores serialised WebAuthn SessionData in SQLite with a 5-minute TTL.
func saveWASession(sessionID string, data []byte) error {
	exp := time.Now().Add(5 * time.Minute).Unix()
	_, err := db.Exec(
		"INSERT OR REPLACE INTO webauthn_sessions (id, data, expires_at) VALUES (?, ?, ?)",
		sessionID, string(data), exp,
	)
	return err
}

// loadWASession retrieves and immediately deletes a WebAuthn session (one-time use).
func loadWASession(sessionID string) ([]byte, error) {
	var data string
	var expiresAt int64
	err := db.QueryRow(
		"SELECT data, expires_at FROM webauthn_sessions WHERE id=?", sessionID,
	).Scan(&data, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found")
	}
	if err != nil {
		return nil, err
	}
	db.Exec("DELETE FROM webauthn_sessions WHERE id=?", sessionID)
	if time.Now().Unix() > expiresAt {
		return nil, fmt.Errorf("session expired")
	}
	return []byte(data), nil
}

// ── Registration (invite flow) ────────────────────────────────────────────────

type waRegSession struct {
	Session     webauthn.SessionData `json:"session"`
	Email       string               `json:"email"`
	DisplayName string               `json:"display_name"`
	InviteID    int                  `json:"invite_id"`
	UserID      int                  `json:"user_id"`    // placeholder user created in begin; 0 = legacy
	PendingID   int                  `json:"pending_id"` // set for registration-flow passkey binding
}

// POST /api/v1/invites/{token}/webauthn/begin
// Body: {"display_name":"…","email":"…"} — email is optional
// Returns: {"session_id":"…","options":{…}}
func webauthnInviteBegin(w http.ResponseWriter, r *http.Request) {
	if wauthn == nil {
		writeError(w, "WebAuthn not configured (set base_url in config)", http.StatusServiceUnavailable)
		return
	}
	token := r.PathValue("token")

	invite, err := loadValidInvite(token)
	switch {
	case err == nil:
	case err == sql.ErrNoRows:
		writeError(w, "Invalid or expired invite link", http.StatusNotFound)
		return
	case err == errInviteUsed:
		writeError(w, "Invite link already used", http.StatusGone)
		return
	case err == errInviteExpired:
		writeError(w, "Invite link expired", http.StatusGone)
		return
	default:
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	var req struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if invite.PresetEmail != "" {
		req.Email = invite.PresetEmail
	}
	// Email no longer required — passkey-only accounts are supported.

	// Create a placeholder user upfront so we have a stable numeric ID to use
	// as the WebAuthn user handle. This enables discoverable (resident-key) login
	// without any temp-handle workarounds.
	emailVerified := 0
	if invite.PresetEmail != "" {
		emailVerified = 1
	}
	var emailVal interface{} = nil
	if req.Email != "" {
		emailVal = req.Email
	}
	if isDisplayNameTaken(req.DisplayName, 0) {
		writeError(w, "Display name is already taken", http.StatusConflict)
		return
	}
	result, err := db.Exec(
		"INSERT INTO users (email, display_name, password_hash, role, email_verified) VALUES (?, ?, '', ?, ?)",
		emailVal, req.DisplayName, invite.Role, emailVerified,
	)
	if err != nil {
		writeError(w, "Account with this email already exists", http.StatusConflict)
		return
	}
	userID, _ := result.LastInsertId()

	username := req.Email
	if username == "" {
		username = req.DisplayName
	}
	if username == "" {
		username = fmt.Sprintf("user#%d", userID)
	}
	regUser := &waUser{
		id:       userHandle(int(userID)),
		username: username,
	}

	options, sessionData, err := wauthn.BeginRegistration(regUser,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
	)
	if err != nil {
		db.Exec("DELETE FROM users WHERE id=?", userID)
		writeError(w, "WebAuthn begin failed", http.StatusInternalServerError)
		return
	}

	blob, _ := json.Marshal(waRegSession{
		Session:     *sessionData,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		InviteID:    invite.ID,
		UserID:      int(userID),
	})
	sessionID, err := generateSessionToken()
	if err != nil {
		db.Exec("DELETE FROM users WHERE id=?", userID)
		writeError(w, "Could not generate session", http.StatusInternalServerError)
		return
	}
	if err := saveWASession(sessionID, blob); err != nil {
		db.Exec("DELETE FROM users WHERE id=?", userID)
		writeError(w, "Could not store session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"session_id": sessionID,
		"options":    options,
	})
}

// issueSessionResponse creates a session token for userID and writes the
// standard {token, expires_at, user_id, email, role} JSON response with the
// given status code. On token-creation failure, onErr is called to write an
// alternate response — each call site has its own fallback behavior.
func issueSessionResponse(w http.ResponseWriter, r *http.Request, status int, userID int, email, role string, onErr func()) {
	sessionToken, expiresAt, err := createTokenInDB(userID, r.UserAgent(), getClientIP(r), "")
	if err != nil {
		onErr()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	json.NewEncoder(w).Encode(map[string]any{
		"token":      sessionToken,
		"expires_at": expiresAt.Format(time.RFC3339),
		"user_id":    userID,
		"email":      email,
		"role":       role,
	})
}

// POST /api/v1/invites/{token}/webauthn/finish?session_id=…
// Body: PublicKeyCredential JSON from navigator.credentials.create()
// Returns: {"token":"…","user_id":…,"role":"…"}
func webauthnInviteFinish(w http.ResponseWriter, r *http.Request) {
	if wauthn == nil {
		writeError(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}
	token := r.PathValue("token")
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeError(w, "session_id query parameter required", http.StatusBadRequest)
		return
	}

	invite, err := loadValidInvite(token)
	switch {
	case err == nil:
	case err == sql.ErrNoRows:
		writeError(w, "Invalid invite link", http.StatusNotFound)
		return
	case err == errInviteUsed:
		writeError(w, "Invite link already used", http.StatusGone)
		return
	case err == errInviteExpired:
		writeError(w, "Invite link expired", http.StatusGone)
		return
	default:
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	blob, err := loadWASession(sessionID)
	if err != nil {
		writeError(w, "Session expired or not found", http.StatusBadRequest)
		return
	}
	var stored waRegSession
	if err := json.Unmarshal(blob, &stored); err != nil || stored.InviteID != invite.ID || stored.UserID == 0 {
		writeError(w, "Session mismatch", http.StatusBadRequest)
		return
	}

	// The user was pre-created in Begin; use the same stable handle.
	regUser := loadWebAuthnUser(stored.UserID, stored.Email)

	credential, err := wauthn.FinishRegistration(regUser, stored.Session, r)
	if err != nil {
		db.Exec("DELETE FROM users WHERE id=?", stored.UserID)
		writeError(w, "WebAuthn verification failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		db.Exec("DELETE FROM users WHERE id=?", stored.UserID)
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		"INSERT INTO webauthn_credentials (user_id, credential_id, public_key, sign_count, aaguid, flags) VALUES (?, ?, ?, ?, ?, ?)",
		stored.UserID, credential.ID, credential.PublicKey, credential.Authenticator.SignCount, credential.Authenticator.AAGUID, byte(credential.Flags.ProtocolValue()),
	); err != nil {
		db.Exec("DELETE FROM users WHERE id=?", stored.UserID)
		writeError(w, "Could not store credential", http.StatusInternalServerError)
		return
	}

	if invite.OrgID.Valid {
		tx.Exec(
			"INSERT OR IGNORE INTO organization_members (organization_id, user_id) VALUES (?, ?)",
			invite.OrgID.Int64, stored.UserID,
		)
	}
	tx.Exec("UPDATE invite_links SET used_at=? WHERE id=?", time.Now().UTC().Unix(), invite.ID)

	if err := tx.Commit(); err != nil {
		db.Exec("DELETE FROM users WHERE id=?", stored.UserID)
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	identifier := stored.Email
	if identifier == "" {
		identifier = stored.DisplayName
	}
	log.Printf("webauthn: new user %q (role=%s) registered via invite id=%d", identifier, invite.Role, invite.ID)

	issueSessionResponse(w, r, http.StatusCreated, stored.UserID, stored.Email, invite.Role, func() {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"status": "created"})
	})
}

// ── Login ─────────────────────────────────────────────────────────────────────

type waLoginSession struct {
	Session webauthn.SessionData `json:"session"`
	UserID  int                  `json:"user_id"`
}

// POST /api/v1/auth/webauthn/login/begin
// Body: {"email":"…"} — email optional; omitting triggers discoverable (resident-key) login
// Returns: {"session_id":"…","options":{…}}
func webauthnLoginBegin(w http.ResponseWriter, r *http.Request) {
	if wauthn == nil {
		writeError(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	var options *protocol.CredentialAssertion
	var sessionData *webauthn.SessionData
	var userID int

	uvOpt := webauthnUserVerificationOpt()
	if req.Email == "" {
		// Discoverable login: browser presents resident-key credentials, no identifier needed.
		var err error
		options, sessionData, err = wauthn.BeginDiscoverableLogin(uvOpt)
		if err != nil {
			writeError(w, "WebAuthn begin failed", http.StatusInternalServerError)
			return
		}
		// userID stays 0 as sentinel for the discoverable path in finish.
	} else {
		var userEmail string
		var userDisabled int
		if err := db.QueryRow(
			"SELECT id, COALESCE(email,''), COALESCE(disabled,0) FROM users WHERE email=?", req.Email,
		).Scan(&userID, &userEmail, &userDisabled); err != nil {
			// Fall back to display_name for users registered without an email address.
			rows, err2 := db.Query(
				"SELECT id, COALESCE(email,''), COALESCE(disabled,0) FROM users WHERE display_name=? COLLATE NOCASE LIMIT 2",
				req.Email,
			)
			if err2 != nil {
				writeError(w, "No passkeys found for this user", http.StatusNotFound)
				return
			}
			defer rows.Close()
			var matched int
			for rows.Next() {
				matched++
				rows.Scan(&userID, &userEmail, &userDisabled)
			}
			if matched == 0 {
				writeError(w, "No passkeys found for this user", http.StatusNotFound)
				return
			}
			if matched > 1 {
				writeError(w, "Multiple accounts share that username; please use your email address", http.StatusConflict)
				return
			}
		}
		if userDisabled != 0 {
			writeError(w, "This account has been disabled. Please contact the administrator.", http.StatusForbidden)
			return
		}
		user := loadWebAuthnUser(userID, userEmail)
		if len(user.credentials) == 0 {
			writeError(w, "No passkeys registered for this user", http.StatusBadRequest)
			return
		}
		var err error
		options, sessionData, err = wauthn.BeginLogin(user, uvOpt)
		if err != nil {
			writeError(w, "WebAuthn begin failed", http.StatusInternalServerError)
			return
		}
	}

	blob, _ := json.Marshal(waLoginSession{Session: *sessionData, UserID: userID})
	sessionID, err := generateSessionToken()
	if err != nil {
		writeError(w, "Could not generate session", http.StatusInternalServerError)
		return
	}
	if err := saveWASession(sessionID, blob); err != nil {
		writeError(w, "Could not store session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"session_id": sessionID,
		"options":    options,
	})
}

// POST /api/v1/auth/webauthn/login/finish?session_id=…
// Body: PublicKeyCredential JSON from navigator.credentials.get()
// Returns: {"token":"…","expires_at":"…","user_id":…,"email":"…","role":"…"}
func webauthnLoginFinish(w http.ResponseWriter, r *http.Request) {
	if wauthn == nil {
		writeError(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeError(w, "session_id query parameter required", http.StatusBadRequest)
		return
	}

	blob, err := loadWASession(sessionID)
	if err != nil {
		writeError(w, "Session expired or not found", http.StatusBadRequest)
		return
	}
	var stored waLoginSession
	if err := json.Unmarshal(blob, &stored); err != nil {
		writeError(w, "Corrupt session", http.StatusBadRequest)
		return
	}

	// Discoverable path: UserID==0 means no email was given at begin time.
	// Look up the user by credential ID after the browser presents its resident key.
	if stored.UserID == 0 {
		var discUserID int
		var discEmail, discRole string
		discHandler := func(rawID, _ []byte) (webauthn.User, error) {
			if err := db.QueryRow(
				`SELECT c.user_id, COALESCE(u.email,''), u.role
				 FROM webauthn_credentials c
				 JOIN users u ON u.id=c.user_id
				 WHERE c.credential_id=? AND u.disabled=0`,
				rawID,
			).Scan(&discUserID, &discEmail, &discRole); err != nil {
				return nil, fmt.Errorf("credential not found")
			}
			return loadWebAuthnUser(discUserID, discEmail), nil
		}
		_, credential, err := wauthn.FinishPasskeyLogin(discHandler, stored.Session, r)
		if err != nil {
			log.Printf("webauthn: discoverable login failed: %v", err)
			writeError(w, "WebAuthn verification failed", http.StatusUnauthorized)
			return
		}
		db.Exec(
			"UPDATE webauthn_credentials SET sign_count=?, flags=? WHERE user_id=? AND credential_id=?",
			credential.Authenticator.SignCount, byte(credential.Flags.ProtocolValue()), discUserID, credential.ID,
		)
		var discTOTPSecret string
		db.QueryRow("SELECT COALESCE(totp_secret,'') FROM users WHERE id=?", discUserID).Scan(&discTOTPSecret)
		if discTOTPSecret != "" {
			issueWATOTPPending(w, discUserID, discEmail, discRole)
			return
		}
		issueSessionResponse(w, r, http.StatusOK, discUserID, discEmail, discRole, func() {
			writeError(w, "Could not create session", http.StatusInternalServerError)
		})
		return
	}

	// Non-discoverable path: email was specified at begin time.
	var userEmail, role string
	var finishDisabled int
	if err := db.QueryRow(
		"SELECT COALESCE(email,''), role, COALESCE(disabled,0) FROM users WHERE id=?", stored.UserID,
	).Scan(&userEmail, &role, &finishDisabled); err != nil {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if finishDisabled != 0 {
		writeError(w, "This account has been disabled. Please contact the administrator.", http.StatusForbidden)
		return
	}

	user := loadWebAuthnUser(stored.UserID, userEmail)

	parsedResponse, err := protocol.ParseCredentialRequestResponse(r)
	if err != nil {
		writeError(w, "WebAuthn verification failed", http.StatusUnauthorized)
		return
	}

	// Migrate flags=0 credentials (registered before the flags column existed).
	for i := range user.credentials {
		if user.credentials[i].Flags.ProtocolValue() == 0 {
			user.credentials[i].Flags = webauthn.NewCredentialFlags(parsedResponse.Response.AuthenticatorData.Flags)
		}
	}

	// Adopt whatever user handle the authenticator echoes back so the library's
	// identity check passes. Credential ownership and signature still enforce security.
	loginSession := stored.Session
	if uh := parsedResponse.Response.UserHandle; len(uh) > 0 {
		user.id = uh
		loginSession.UserID = uh
	}

	credential, err := wauthn.ValidateLogin(user, loginSession, parsedResponse)
	if err != nil {
		log.Printf("webauthn: login validation failed for user %d: %v", stored.UserID, err)
		writeError(w, "WebAuthn verification failed", http.StatusUnauthorized)
		return
	}

	db.Exec(
		"UPDATE webauthn_credentials SET sign_count=?, flags=? WHERE user_id=? AND credential_id=?",
		credential.Authenticator.SignCount, byte(credential.Flags.ProtocolValue()), stored.UserID, credential.ID,
	)

	var totpSecret string
	db.QueryRow("SELECT COALESCE(totp_secret,'') FROM users WHERE id=?", stored.UserID).Scan(&totpSecret)
	if totpSecret != "" {
		issueWATOTPPending(w, stored.UserID, userEmail, role)
		return
	}
	issueSessionResponse(w, r, http.StatusOK, stored.UserID, userEmail, role, func() {
		writeError(w, "Could not create session", http.StatusInternalServerError)
	})
}

// POST /api/v1/auth/webauthn/totp-challenge — validates a pending WebAuthn TOTP token
// and issues a full session. Called after webauthnLoginFinish returns totp_required.
func webauthnTOTPChallenge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		PendingToken string `json:"pending_token"`
		TotpCode     string `json:"totp_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	v, ok := webauthnPendingTOTP.LoadAndDelete(req.PendingToken)
	if !ok {
		writeError(w, "invalid or expired pending token", http.StatusUnauthorized)
		return
	}
	pending := v.(waTotpPending)
	if time.Now().After(pending.ExpiresAt) {
		writeError(w, "pending token expired", http.StatusUnauthorized)
		return
	}
	var totpSecret string
	if err := db.QueryRow("SELECT COALESCE(totp_secret,'') FROM users WHERE id=?", pending.UserID).Scan(&totpSecret); err != nil || totpSecret == "" {
		writeError(w, "TOTP not configured", http.StatusBadRequest)
		return
	}
	if !totpCheckAndMark(pending.UserID, totpSecret, req.TotpCode, time.Now()) {
		log.Printf("webauthn: invalid or replayed TOTP for user %d (%s)", pending.UserID, pending.Email)
		writeError(w, "Invalid TOTP code", http.StatusUnauthorized)
		return
	}
	issueSessionResponse(w, r, http.StatusOK, pending.UserID, pending.Email, pending.Role, func() {
		writeError(w, "Could not create session", http.StatusInternalServerError)
	})
}

// ── User passkey management (authenticated) ───────────────────────────────────

// GET /api/v1/user/webauthn/credentials
func webauthnUserCredentialsList(w http.ResponseWriter, r *http.Request) {
	callerID, _ := callerFromRequest(r)
	rows, err := db.Query(
		"SELECT id, name, created_at FROM webauthn_credentials WHERE user_id=? ORDER BY created_at",
		callerID,
	)
	if err != nil {
		writeError(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type credItem struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		CreatedAt string `json:"created_at"`
	}
	items := []credItem{}
	for rows.Next() {
		var item credItem
		if rows.Scan(&item.ID, &item.Name, &item.CreatedAt) == nil {
			items = append(items, item)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// POST /api/v1/user/webauthn/register/begin
func webauthnUserRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if wauthn == nil {
		writeError(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}
	callerID, _ := callerFromRequest(r)
	var userEmail string
	if err := db.QueryRow("SELECT COALESCE(email,'') FROM users WHERE id=?", callerID).Scan(&userEmail); err != nil {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	user := loadWebAuthnUser(callerID, userEmail)
	options, sessionData, err := wauthn.BeginRegistration(user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
	)
	if err != nil {
		writeError(w, "WebAuthn begin failed", http.StatusInternalServerError)
		return
	}
	blob, _ := json.Marshal(waLoginSession{Session: *sessionData, UserID: callerID})
	sessionID, err := generateSessionToken()
	if err != nil {
		writeError(w, "Could not generate session", http.StatusInternalServerError)
		return
	}
	if err := saveWASession(sessionID, blob); err != nil {
		writeError(w, "Could not store session", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"session_id": sessionID, "options": options})
}

// POST /api/v1/user/webauthn/register/finish?session_id=…
func webauthnUserRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if wauthn == nil {
		writeError(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}
	callerID, _ := callerFromRequest(r)
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeError(w, "session_id query parameter required", http.StatusBadRequest)
		return
	}
	blob, err := loadWASession(sessionID)
	if err != nil {
		writeError(w, "Session expired or not found", http.StatusBadRequest)
		return
	}
	var stored waLoginSession
	if err := json.Unmarshal(blob, &stored); err != nil || stored.UserID != callerID {
		writeError(w, "Session mismatch", http.StatusBadRequest)
		return
	}
	var userEmail string
	if err := db.QueryRow("SELECT COALESCE(email,'') FROM users WHERE id=?", callerID).Scan(&userEmail); err != nil {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	user := loadWebAuthnUser(callerID, userEmail)
	credential, err := wauthn.FinishRegistration(user, stored.Session, r)
	if err != nil {
		writeError(w, "WebAuthn verification failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := db.Exec(
		"INSERT INTO webauthn_credentials (user_id, credential_id, public_key, sign_count, aaguid, flags) VALUES (?, ?, ?, ?, ?, ?)",
		callerID, credential.ID, credential.PublicKey, credential.Authenticator.SignCount, credential.Authenticator.AAGUID, byte(credential.Flags.ProtocolValue()),
	); err != nil {
		writeError(w, "Could not store credential", http.StatusInternalServerError)
		return
	}
	log.Printf("webauthn: user %d added a new passkey", callerID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{"status": "created"})
}

// DELETE /api/v1/user/webauthn/credentials/{id}
func webauthnUserCredentialDelete(w http.ResponseWriter, r *http.Request) {
	callerID, _ := callerFromRequest(r)
	credID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	// Prevent locking out a user who has no password and this is their last passkey.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM webauthn_credentials WHERE user_id=?", callerID).Scan(&count)
	if count <= 1 {
		var passwordHash string
		db.QueryRow("SELECT COALESCE(password_hash,'') FROM users WHERE id=?", callerID).Scan(&passwordHash)
		if passwordHash == "" {
			writeError(w, "Cannot delete last passkey when no password is set", http.StatusConflict)
			return
		}
	}
	res, err := db.Exec("DELETE FROM webauthn_credentials WHERE id=? AND user_id=?", credID, callerID)
	if err != nil {
		writeError(w, "Database error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, "Not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/register/passkey/begin
// Body: {"pending_id":123,"verification_token":"...","display_name":"optional"}
// Creates a disabled=1 placeholder user, links it to the pending registration,
// and begins a WebAuthn registration ceremony.
func webauthnRegBegin(w http.ResponseWriter, r *http.Request) {
	if wauthn == nil {
		writeError(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		PendingID         int    `json:"pending_id"`
		VerificationToken string `json:"verification_token"`
		DisplayName       string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PendingID == 0 || req.VerificationToken == "" {
		writeError(w, "pending_id and verification_token are required", http.StatusBadRequest)
		return
	}

	// Validate the pending registration belongs to the caller.
	var pr struct {
		ID        int
		Email     string
		Verified  int
		UserID    sql.NullInt64
		ExpiresAt string
	}
	if err := db.QueryRow(
		"SELECT id, COALESCE(email,''), verified, user_id, expires_at FROM pending_registrations WHERE id=? AND verification_token=?",
		req.PendingID, sha256Hex(req.VerificationToken),
	).Scan(&pr.ID, &pr.Email, &pr.Verified, &pr.UserID, &pr.ExpiresAt); err != nil {
		writeError(w, "Pending registration not found", http.StatusNotFound)
		return
	}
	if exp, err := parseTokenExpiration(pr.ExpiresAt); err != nil || time.Now().After(exp) {
		writeError(w, "Registration has expired", http.StatusGone)
		return
	}
	if pr.Verified == 0 {
		writeError(w, "Email not yet verified", http.StatusConflict)
		return
	}
	// If a placeholder user already exists for this pending reg, delete it so we
	// start fresh (handles retries after a failed ceremony).
	if pr.UserID.Valid {
		db.Exec("DELETE FROM users WHERE id=? AND password_hash='' AND disabled=1", pr.UserID.Int64)
		db.Exec("UPDATE pending_registrations SET user_id=NULL WHERE id=?", pr.ID)
	}

	// Create a disabled placeholder user.
	var emailVal interface{} = nil
	if pr.Email != "" {
		emailVal = pr.Email
	}
	result, err := db.Exec(
		"INSERT INTO users (email, display_name, password_hash, role, email_verified, disabled) VALUES (?, ?, '', 'user', 0, 1)",
		emailVal, req.DisplayName,
	)
	if err != nil {
		writeError(w, "Could not create placeholder user", http.StatusInternalServerError)
		return
	}
	userID, _ := result.LastInsertId()

	db.Exec("UPDATE pending_registrations SET user_id=? WHERE id=?", userID, pr.ID)

	username := pr.Email
	if username == "" {
		username = req.DisplayName
	}
	if username == "" {
		username = fmt.Sprintf("user#%d", userID)
	}
	regUser := &waUser{
		id:       userHandle(int(userID)),
		username: username,
	}

	options, sessionData, err := wauthn.BeginRegistration(regUser,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
	)
	if err != nil {
		db.Exec("DELETE FROM users WHERE id=?", userID)
		db.Exec("UPDATE pending_registrations SET user_id=NULL WHERE id=?", pr.ID)
		writeError(w, "WebAuthn begin failed", http.StatusInternalServerError)
		return
	}

	blob, _ := json.Marshal(waRegSession{
		Session:   *sessionData,
		Email:     pr.Email,
		UserID:    int(userID),
		PendingID: pr.ID,
	})
	sessionID, err := generateSessionToken()
	if err != nil {
		db.Exec("DELETE FROM users WHERE id=?", userID)
		db.Exec("UPDATE pending_registrations SET user_id=NULL WHERE id=?", pr.ID)
		writeError(w, "Could not generate session", http.StatusInternalServerError)
		return
	}
	if err := saveWASession(sessionID, blob); err != nil {
		db.Exec("DELETE FROM users WHERE id=?", userID)
		db.Exec("UPDATE pending_registrations SET user_id=NULL WHERE id=?", pr.ID)
		writeError(w, "Could not store session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"session_id": sessionID,
		"options":    options,
	})
}

// POST /api/v1/register/passkey/finish?session_id=…
// Body: PublicKeyCredential JSON from navigator.credentials.create()
// Stores the credential against the placeholder user; account remains disabled
// until an admin approves the pending registration.
func webauthnRegFinish(w http.ResponseWriter, r *http.Request) {
	if wauthn == nil {
		writeError(w, "WebAuthn not configured", http.StatusServiceUnavailable)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeError(w, "session_id query parameter required", http.StatusBadRequest)
		return
	}

	blob, err := loadWASession(sessionID)
	if err != nil {
		writeError(w, "Session expired or not found", http.StatusBadRequest)
		return
	}
	var stored waRegSession
	if err := json.Unmarshal(blob, &stored); err != nil || stored.UserID == 0 || stored.PendingID == 0 {
		writeError(w, "Session mismatch", http.StatusBadRequest)
		return
	}

	// Verify the pending registration still exists and is not yet approved.
	var pendingExists int
	db.QueryRow("SELECT COUNT(*) FROM pending_registrations WHERE id=? AND user_id=? AND approved=0",
		stored.PendingID, stored.UserID).Scan(&pendingExists)
	if pendingExists == 0 {
		db.Exec("DELETE FROM users WHERE id=?", stored.UserID)
		writeError(w, "Pending registration not found or already processed", http.StatusConflict)
		return
	}

	regUser := loadWebAuthnUser(stored.UserID, stored.Email)
	credential, err := wauthn.FinishRegistration(regUser, stored.Session, r)
	if err != nil {
		db.Exec("DELETE FROM users WHERE id=?", stored.UserID)
		db.Exec("UPDATE pending_registrations SET user_id=NULL WHERE id=?", stored.PendingID)
		writeError(w, "WebAuthn verification failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	if _, err := db.Exec(
		"INSERT INTO webauthn_credentials (user_id, credential_id, public_key, sign_count, aaguid, flags) VALUES (?, ?, ?, ?, ?, ?)",
		stored.UserID, credential.ID, credential.PublicKey, credential.Authenticator.SignCount, credential.Authenticator.AAGUID, byte(credential.Flags.ProtocolValue()),
	); err != nil {
		db.Exec("DELETE FROM users WHERE id=?", stored.UserID)
		db.Exec("UPDATE pending_registrations SET user_id=NULL WHERE id=?", stored.PendingID)
		writeError(w, "Could not store credential", http.StatusInternalServerError)
		return
	}

	log.Printf("webauthn: passkey bound to pending registration %d (user_id=%d)", stored.PendingID, stored.UserID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "passkey_bound"})
}
