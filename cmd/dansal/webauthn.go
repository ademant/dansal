package main

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

var wauthn *webauthn.WebAuthn

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
		"SELECT credential_id, public_key, sign_count, aaguid FROM webauthn_credentials WHERE user_id=?", userID,
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
		if err := rows.Scan(&credID, &pubKey, &signCount, &aaguid); err != nil {
			continue
		}
		creds = append(creds, webauthn.Credential{
			ID:        credID,
			PublicKey: pubKey,
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
}

// POST /api/v1/invites/{token}/webauthn/begin
// Body: {"username":"…","email":"…"}
// Returns: {"session_id":"…","options":{…}}
func webauthnInviteBegin(w http.ResponseWriter, r *http.Request) {
	if wauthn == nil {
		writeError(w, "WebAuthn not configured (set base_url in config)", http.StatusServiceUnavailable)
		return
	}
	token := r.PathValue("token")

	var invite struct {
		ID             int
		ExpiresAt      string
		UsedAt         string
		PresetUsername string
		PresetEmail    string
	}
	err := db.QueryRow(
		`SELECT id, expires_at, COALESCE(used_at,''), COALESCE(preset_username,''), COALESCE(preset_email,'')
		 FROM invite_links WHERE token=?`, token,
	).Scan(&invite.ID, &invite.ExpiresAt, &invite.UsedAt, &invite.PresetUsername, &invite.PresetEmail)
	if err == sql.ErrNoRows {
		writeError(w, "Invalid or expired invite link", http.StatusNotFound)
		return
	}
	if invite.UsedAt != "" {
		writeError(w, "Invite link already used", http.StatusGone)
		return
	}
	if exp, err2 := parseTokenExpiration(invite.ExpiresAt); err2 != nil || time.Now().After(exp) {
		writeError(w, "Invite link expired", http.StatusGone)
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
	// Preset email from registration overrides whatever the client sends.
	if invite.PresetEmail != "" {
		req.Email = invite.PresetEmail
	}
	if req.Email == "" {
		writeError(w, "email is required", http.StatusBadRequest)
		return
	}

	// Use a stable handle scoped to this invite+email so the ceremony can be
	// completed without the user existing in the DB yet.
	tempUser := &waUser{
		id:       []byte(fmt.Sprintf("pending:%d:%s", invite.ID, req.Email)),
		username: req.Email,
	}

	options, sessionData, err := wauthn.BeginRegistration(tempUser)
	if err != nil {
		writeError(w, "WebAuthn begin failed", http.StatusInternalServerError)
		return
	}

	blob, _ := json.Marshal(waRegSession{
		Session:     *sessionData,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		InviteID:    invite.ID,
	})
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

// POST /api/v1/invites/{token}/webauthn/finish?session_id=…
// Body: PublicKeyCredential JSON from navigator.credentials.create()
// Returns: {"token":"…","username":"…","role":"…"}
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

	// Load full invite details.
	var invite struct {
		ID        int
		Role      string
		OrgID     sql.NullInt64
		ExpiresAt string
		UsedAt    string
	}
	err := db.QueryRow(
		"SELECT id, role, org_id, expires_at, COALESCE(used_at,'') FROM invite_links WHERE token=?", token,
	).Scan(&invite.ID, &invite.Role, &invite.OrgID, &invite.ExpiresAt, &invite.UsedAt)
	if err == sql.ErrNoRows {
		writeError(w, "Invalid invite link", http.StatusNotFound)
		return
	}
	if invite.UsedAt != "" {
		writeError(w, "Invite link already used", http.StatusGone)
		return
	}
	if exp, err2 := parseTokenExpiration(invite.ExpiresAt); err2 != nil || time.Now().After(exp) {
		writeError(w, "Invite link expired", http.StatusGone)
		return
	}

	blob, err := loadWASession(sessionID)
	if err != nil {
		writeError(w, "Session expired or not found", http.StatusBadRequest)
		return
	}
	var stored waRegSession
	if err := json.Unmarshal(blob, &stored); err != nil || stored.InviteID != invite.ID {
		writeError(w, "Session mismatch", http.StatusBadRequest)
		return
	}

	tempUser := &waUser{
		id:       []byte(fmt.Sprintf("pending:%d:%s", invite.ID, stored.Email)),
		username: stored.Email,
	}

	credential, err := wauthn.FinishRegistration(tempUser, stored.Session, r)
	if err != nil {
		writeError(w, "WebAuthn verification failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// password_hash is empty string — password login will not work until set.
	result, err := tx.Exec(
		"INSERT INTO users (email, display_name, password_hash, role, email_verified) VALUES (?, ?, '', ?, 1)",
		stored.Email, stored.DisplayName, invite.Role,
	)
	if err != nil {
		writeError(w, "Username or email already exists", http.StatusConflict)
		return
	}
	userID, _ := result.LastInsertId()

	if _, err := tx.Exec(
		"INSERT INTO webauthn_credentials (user_id, credential_id, public_key, sign_count, aaguid) VALUES (?, ?, ?, ?, ?)",
		userID, credential.ID, credential.PublicKey, credential.Authenticator.SignCount, credential.Authenticator.AAGUID,
	); err != nil {
		writeError(w, "Could not store credential", http.StatusInternalServerError)
		return
	}

	if invite.OrgID.Valid {
		tx.Exec(
			"INSERT OR IGNORE INTO organization_members (organization_id, user_id) VALUES (?, ?)",
			invite.OrgID.Int64, userID,
		)
	}
	tx.Exec("UPDATE invite_links SET used_at=? WHERE id=?", time.Now().UTC().Unix(), invite.ID)

	if err := tx.Commit(); err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	log.Printf("webauthn: new user %q (role=%s) registered via invite id=%d", stored.Email, invite.Role, invite.ID)

	sessionToken, expiresAt, err := createTokenInDB(int(userID), r.UserAgent(), getClientIP(r), "")
	if err != nil {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"status": "created", "email": stored.Email})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"token":      sessionToken,
		"expires_at": expiresAt.Format(time.RFC3339),
		"user_id":    int(userID),
		"email":      stored.Email,
		"role":       invite.Role,
	})
}

// ── Login ─────────────────────────────────────────────────────────────────────

type waLoginSession struct {
	Session webauthn.SessionData `json:"session"`
	UserID  int                  `json:"user_id"`
}

// POST /api/v1/auth/webauthn/login/begin
// Body: {"email":"…"}
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
	if req.Email == "" {
		writeError(w, "email is required", http.StatusBadRequest)
		return
	}

	var userID int
	var userEmail string
	if err := db.QueryRow(
		"SELECT id, email FROM users WHERE email=? AND disabled=0", req.Email,
	).Scan(&userID, &userEmail); err != nil {
		// Don't reveal whether user exists; use generic error.
		writeError(w, "No passkeys found for this user", http.StatusNotFound)
		return
	}

	user := loadWebAuthnUser(userID, userEmail)
	if len(user.credentials) == 0 {
		writeError(w, "No passkeys registered for this user", http.StatusBadRequest)
		return
	}

	options, sessionData, err := wauthn.BeginLogin(user)
	if err != nil {
		writeError(w, "WebAuthn begin failed", http.StatusInternalServerError)
		return
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
// Returns: {"token":"…","expires_at":"…","username":"…","role":"…"}
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

	var userEmail, role string
	if err := db.QueryRow(
		"SELECT email, role FROM users WHERE id=? AND disabled=0", stored.UserID,
	).Scan(&userEmail, &role); err != nil {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}

	user := loadWebAuthnUser(stored.UserID, userEmail)
	credential, err := wauthn.FinishLogin(user, stored.Session, r)
	if err != nil {
		writeError(w, "WebAuthn verification failed", http.StatusUnauthorized)
		return
	}

	// Update sign count to detect credential cloning.
	db.Exec(
		"UPDATE webauthn_credentials SET sign_count=? WHERE user_id=? AND credential_id=?",
		credential.Authenticator.SignCount, stored.UserID, credential.ID,
	)

	sessionToken, expiresAt, err := createTokenInDB(stored.UserID, r.UserAgent(), getClientIP(r), "")
	if err != nil {
		writeError(w, "Could not create session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":      sessionToken,
		"expires_at": expiresAt.Format(time.RFC3339),
		"email":      userEmail,
		"role":       role,
	})
}
