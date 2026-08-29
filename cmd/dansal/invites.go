package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// inviteRecord holds the invite_links columns needed to validate and redeem
// an invite.
type inviteRecord struct {
	ID           int
	Role         string
	OrgID        sql.NullInt64
	ExpiresAt    string
	UsedAt       string
	PresetEmail  string
	TargetUserID sql.NullInt64 // #1190: set on publisher reconnect invites — see redeemPublisherReconnectInvite
}

var (
	errInviteUsed    = errors.New("invite link already used")
	errInviteExpired = errors.New("invite link expired")
)

// loadValidInvite looks up an invite by token and validates it has not been
// used or expired. Returns sql.ErrNoRows if no invite exists for token,
// errInviteUsed or errInviteExpired if found but no longer redeemable, or
// another error on a DB failure. Callers map these to the appropriate HTTP
// status and message.
func loadValidInvite(token string) (inviteRecord, error) {
	var invite inviteRecord
	err := db.QueryRow(
		`SELECT id, role, org_id, expires_at, COALESCE(used_at,''), COALESCE(preset_email,''), target_user_id
		 FROM invite_links WHERE token=?`, token,
	).Scan(&invite.ID, &invite.Role, &invite.OrgID, &invite.ExpiresAt, &invite.UsedAt, &invite.PresetEmail, &invite.TargetUserID)
	if err != nil {
		return inviteRecord{}, err
	}
	if invite.UsedAt != "" {
		return inviteRecord{}, errInviteUsed
	}
	if exp, err2 := parseTokenExpiration(invite.ExpiresAt); err2 != nil || time.Now().After(exp) {
		return inviteRecord{}, errInviteExpired
	}
	// Link and publisher invite tokens are JWTs (two dots); verify signature
	// and claims as an additional authenticity check. QR invites use short
	// opaque tokens (no dots) validated solely by the DB lookup above — they
	// need no JWT verification and don't carry org_id in the token itself.
	if strings.Count(token, ".") == 2 {
		claims, err := verifyInviteJWT(token, inviteTokenType(invite.Role))
		if err != nil {
			return inviteRecord{}, errInviteBadToken
		}
		wantOrgID := 0
		if invite.OrgID.Valid {
			wantOrgID = int(invite.OrgID.Int64)
		}
		gotOrgID := 0
		if claims.OrgID != nil {
			gotOrgID = *claims.OrgID
		}
		if wantOrgID != gotOrgID {
			return inviteRecord{}, errInviteBadToken
		}
	}
	return invite, nil
}

// loadValidInviteOrError loads and validates the invite for token, writing
// the appropriate HTTP error response and returning ok=false on any
// failure. Consolidates the err→HTTP-status switch that had drifted across
// 4 call sites — two (webauthn.go) were silently missing the
// errInviteBadToken case and falling through to the wrong 500 (#1013).
func loadValidInviteOrError(w http.ResponseWriter, token string) (inviteRecord, bool) {
	invite, err := loadValidInvite(token)
	switch {
	case err == nil:
		return invite, true
	case err == sql.ErrNoRows:
		writeError(w, "Invalid or expired invite link", http.StatusNotFound)
	case err == errInviteUsed:
		writeError(w, "Invite link already used", http.StatusGone)
	case err == errInviteExpired:
		writeError(w, "Invite link expired", http.StatusGone)
	case err == errInviteBadToken:
		writeError(w, "Invalid invite link", http.StatusBadRequest)
	default:
		writeError(w, "Internal server error", http.StatusInternalServerError)
	}
	return inviteRecord{}, false
}

// roleRank returns a numeric rank for role comparison (higher = more privileged).
func roleRank(role string) int {
	switch role {
	case RoleAdmin:
		return 3
	case RoleUser:
		return 2
	case RolePublisher:
		return 1
	}
	return 0
}

type InviteLink struct {
	ID         int    `json:"id"`
	Token      string `json:"token"`
	Role       string `json:"role"`
	InviteType string `json:"type,omitempty"`
	OrgID      *int   `json:"org_id,omitempty"`
	ExpiresAt  string `json:"expires_at"`
	UsedAt     string `json:"used_at,omitempty"`
	CreatedAt  string `json:"created_at"`
}

type CreateInviteRequest struct {
	Role       string `json:"role"`
	InviteType string `json:"type"`
	OrgID      *int   `json:"org_id,omitempty"`
}

type UseInviteRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Telegram    string `json:"telegram,omitempty"`
	Matrix      string `json:"matrix,omitempty"`
}

// inviteTokenType maps an invite role to the JWT token_type claim used by
// signInviteJWT/verifyInviteJWT.
func inviteTokenType(role string) string {
	return role + "_invite"
}

var errInvalidChallenge = errors.New("user_metadata.challenge must be between 16 and 256 characters")

// extractInviteChallenge pulls an optional challenge string out of a raw
// user_metadata JSON blob (see #771). Returns "" with no error if absent.
// The server treats the challenge as an opaque value to echo back, not a
// secret — validation is limited to a sane length.
func extractInviteChallenge(userMetadata json.RawMessage) (string, error) {
	if len(userMetadata) == 0 {
		return "", nil
	}
	var um struct {
		Challenge string `json:"challenge"`
	}
	json.Unmarshal(userMetadata, &um)
	if um.Challenge == "" {
		return "", nil
	}
	if len(um.Challenge) < 16 || len(um.Challenge) > 256 {
		return "", errInvalidChallenge
	}
	return um.Challenge, nil
}

// createInviteRecord generates a token and inserts the invite_links row.
// Shared by the public createInvite HTTP handler (always role=user) and the
// admin-socket invite-admin command (role=admin, sysadmin-only).
func createInviteRecord(creatorID int, role, inviteType string, orgID *int) (InviteLink, error) {
	if inviteType != "qr" && inviteType != "link" {
		inviteType = "link"
	}
	var expiresAt time.Time
	switch {
	case role == RolePublisher:
		// Publisher invites mint an API key on redemption, so a leaked link is
		// higher-stakes than a plain user-signup invite — keep the window short
		// regardless of invite_type.
		expiresAt = time.Now().UTC().Add(time.Duration(config.Server.InvitePublisherExpiryMinutes) * time.Minute)
	case inviteType == "qr":
		expiresAt = time.Now().UTC().Add(time.Duration(config.Server.InviteQRExpiryMinutes) * time.Minute)
	default:
		expiresAt = time.Now().UTC().Add(time.Duration(config.Server.InviteExpiryHours) * time.Hour)
	}

	// QR invites use a short opaque token (16 random bytes, base64url-encoded,
	// 22 chars) so the resulting URL fits in a small QR code that can be
	// scanned reliably from a phone screen. The DB stores all state needed for
	// validation (role, org_id, expires_at, used_at), so stateless JWT
	// verification is not required. Link and publisher invites keep JWTs for
	// WordPress plugin off-server validation (see issue #769).
	var (
		token string
		err   error
	)
	if inviteType == "qr" {
		token, err = generateOpaqueToken()
	} else {
		token, err = signInviteJWT(role, orgID, inviteTokenType(role), expiresAt)
	}
	if err != nil {
		return InviteLink{}, err
	}

	var orgVal any
	if orgID != nil {
		orgVal = *orgID
	}
	_, err = db.Exec(
		"INSERT INTO invite_links (token, created_by, role, org_id, expires_at, invite_type) VALUES (?, ?, ?, ?, ?, ?)",
		token, creatorID, role, orgVal, expiresAt.Unix(), inviteType,
	)
	if err != nil {
		return InviteLink{}, err
	}

	return InviteLink{
		Token:      token,
		Role:       role,
		InviteType: inviteType,
		OrgID:      orgID,
		ExpiresAt:  expiresAt.Format(time.RFC3339),
	}, nil
}

// createPublisherReconnectInviteRecord mints a publisher-role invite tied to
// an existing user (target_user_id set) rather than a fresh signup (#1190).
// Redeeming it via POST /api/v1/invites/{token}/publisher rotates that
// user's API key instead of creating a new user/org-membership — same
// endpoint and response shape as a regular publisher invite, so a client
// (e.g. wp-dansal) that already knows how to redeem a connect link needs no
// changes to consume a reconnect link too.
func createPublisherReconnectInviteRecord(creatorID, targetUserID int, orgID *int) (InviteLink, error) {
	expiresAt := time.Now().UTC().Add(time.Duration(config.Server.InvitePublisherExpiryMinutes) * time.Minute)
	token, err := signInviteJWT(RolePublisher, orgID, inviteTokenType(RolePublisher), expiresAt)
	if err != nil {
		return InviteLink{}, err
	}
	var orgVal any
	if orgID != nil {
		orgVal = *orgID
	}
	_, err = db.Exec(
		"INSERT INTO invite_links (token, created_by, role, org_id, expires_at, invite_type, target_user_id) VALUES (?, ?, ?, ?, ?, 'link', ?)",
		token, creatorID, RolePublisher, orgVal, expiresAt.Unix(), targetUserID,
	)
	if err != nil {
		return InviteLink{}, err
	}
	return InviteLink{
		Token:      token,
		Role:       RolePublisher,
		InviteType: "link",
		OrgID:      orgID,
		ExpiresAt:  expiresAt.Format(time.RFC3339),
	}, nil
}

// POST /api/v1/invites — create an invite link.
// Requires role admin or user. Role is always forced to "user" — this is the
// public web UI path and must never be able to mint an admin invite. See
// adminCreateAdminInvite for the sysadmin-only path that can.
func createInvite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)

	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden: only admin and user roles may create invite links", http.StatusForbidden)
		return
	}

	var req CreateInviteRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	// Only "user" and "publisher" roles are allowed via the UI; admin invites
	// go through the sysadmin-only adminCreateAdminInvite path.
	if req.Role != RolePublisher {
		req.Role = RoleUser
	}

	// org_id is always required for all invite creation.
	orgID := req.OrgID
	if orgID == nil {
		writeError(w, "org_id is required", http.StatusBadRequest)
		return
	}
	// Verify the org exists.
	var exists int
	if err := db.QueryRow("SELECT COUNT(*) FROM organizations WHERE id=?", *orgID).Scan(&exists); err != nil || exists == 0 {
		writeError(w, "Organization not found", http.StatusBadRequest)
		return
	}
	// Non-admin callers must belong to the specified org.
	if callerRole != RoleAdmin && !isOrgMember(callerID, *orgID) {
		writeError(w, "Forbidden: you must be a member of the specified organization", http.StatusForbidden)
		return
	}

	link, err := createInviteRecord(callerID, req.Role, req.InviteType, orgID)
	if err != nil {
		writeError(w, "Failed to create invite link", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(link)
}

// GET /api/v1/invites — list invite links.
// Admins see all; others see only their own.
func listInvites(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)

	var rows *sql.Rows
	var err error
	if callerRole == RoleAdmin {
		rows, err = db.Query(
			"SELECT id, token, role, COALESCE(invite_type,'link'), org_id, expires_at, COALESCE(used_at,''), created_at FROM invite_links ORDER BY created_at DESC",
		)
	} else {
		rows, err = db.Query(
			"SELECT id, token, role, COALESCE(invite_type,'link'), org_id, expires_at, COALESCE(used_at,''), created_at FROM invite_links WHERE created_by=? ORDER BY created_at DESC",
			callerID,
		)
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	links := []InviteLink{}
	for rows.Next() {
		var l InviteLink
		var orgID sql.NullInt64
		if err := rows.Scan(&l.ID, &l.Token, &l.Role, &l.InviteType, &orgID, &l.ExpiresAt, &l.UsedAt, &l.CreatedAt); err != nil {
			writeInternalError(w, err)
			return
		}
		l.ExpiresAt = epochStrToRFC3339(l.ExpiresAt)
		l.UsedAt = epochStrToRFC3339(l.UsedAt)
		if orgID.Valid {
			id := int(orgID.Int64)
			l.OrgID = &id
		}
		links = append(links, l)
	}
	json.NewEncoder(w).Encode(links)
}

// DELETE /api/v1/invites/{token} — revoke an unused invite link.
// Admins may revoke any link; others may only revoke their own.
func revokeInvite(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	token := r.PathValue("token")

	var ownerID int
	var usedAt string
	err := db.QueryRow("SELECT created_by, COALESCE(used_at,'') FROM invite_links WHERE token=?", token).Scan(&ownerID, &usedAt)
	if err == sql.ErrNoRows {
		writeError(w, "Invite link not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if usedAt != "" {
		writeError(w, "Invite link has already been used", http.StatusConflict)
		return
	}
	if ownerID != callerID && callerRole != RoleAdmin {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	db.Exec("DELETE FROM invite_links WHERE token=?", token)
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/invites/{token} — public; returns non-sensitive invite info for the setup page.
func getInviteInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	token := r.PathValue("token")

	var id int
	var role, expiresAt, usedAt, presetEmail sql.NullString
	err := db.QueryRow(
		`SELECT id, role, expires_at, COALESCE(used_at,''), COALESCE(preset_email,'')
		 FROM invite_links WHERE token=?`, token,
	).Scan(&id, &role, &expiresAt, &usedAt, &presetEmail)
	if err == sql.ErrNoRows {
		writeError(w, "Invalid invite link", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	expired := false
	if usedAt.String != "" {
		expired = true
	} else if exp, err2 := parseTokenExpiration(expiresAt.String); err2 != nil || time.Now().After(exp) {
		expired = true
	}
	json.NewEncoder(w).Encode(map[string]any{
		"id":           id,
		"role":         role.String,
		"expired":      expired,
		"preset_email": presetEmail.String,
	})
}

// POST /api/v1/invites/{token} — public endpoint; register a new user via invite.
func useInvite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	token := r.PathValue("token")

	invite, ok := loadValidInviteOrError(w, token)
	if !ok {
		return
	}

	var req UseInviteRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if invite.PresetEmail != "" {
		req.Email = invite.PresetEmail
	}
	// Password requires an identifier so the user can log in later.
	if req.Password != "" && req.Email == "" && req.DisplayName == "" {
		writeError(w, "email or display name is required when setting a password", http.StatusBadRequest)
		return
	}
	// Email, display_name, and password are all optional — passkey-only accounts are supported.
	if req.Email != "" && invite.PresetEmail == "" {
		if !isValidEmail(req.Email) {
			writeError(w, "invalid email address", http.StatusUnprocessableEntity)
			return
		}
		if err := validateEmailDomain(r.Context(), req.Email); err != nil {
			writeError(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

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
	passwordHash, err := hashPassword(req.Password)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	result, err := tx.Exec(
		"INSERT INTO users (email, display_name, password_hash, role, telegram, matrix, email_verified) VALUES (?, ?, ?, ?, ?, ?, ?)",
		emailVal, req.DisplayName, passwordHash, invite.Role, req.Telegram, req.Matrix, emailVerified,
	)
	if err != nil {
		writeError(w, "Account with this email already exists", http.StatusConflict)
		return
	}
	userID, _ := result.LastInsertId()

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

	log.Printf("invite: new user %q (role=%s) registered via invite link id=%d", req.Email, invite.Role, invite.ID)

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(User{
		ID:          int(userID),
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Role:        invite.Role,
		Telegram:    req.Telegram,
		Matrix:      req.Matrix,
	})
}

// POST /api/v1/invites/{token}/publisher — public endpoint; redeem a
// publisher invite: creates a service account + API key and returns
// credentials. Body (all optional):
//
//	{"name":"wp-dansal @ example.com","user_metadata":{"client_name":"...","client_url":"..."}}
func redeemPublisherInvite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	token := r.PathValue("token")

	invite, ok := loadValidInviteOrError(w, token)
	if !ok {
		return
	}

	if invite.Role != RolePublisher {
		writeError(w, "this invite link is not for a publisher account", http.StatusBadRequest)
		return
	}
	if !invite.OrgID.Valid {
		writeError(w, "publisher invite must be tied to an organisation", http.StatusInternalServerError)
		return
	}
	orgID := int(invite.OrgID.Int64)

	var req struct {
		Name         string          `json:"name"`
		UserMetadata json.RawMessage `json:"user_metadata,omitempty"`
		ClientPubkey string          `json:"client_pubkey,omitempty"`
	}
	json.NewDecoder(r.Body).Decode(&req) // body fully optional

	// Validate client_pubkey before touching the DB, so a malformed key
	// fails fast without creating a publisher account we'd have to discard.
	if req.ClientPubkey != "" {
		if _, err := parseClientRSAPubkey(req.ClientPubkey); err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	var metadataVal any
	if len(req.UserMetadata) > 0 && strings.TrimSpace(string(req.UserMetadata)) != "null" {
		metadataVal = string(req.UserMetadata)
	}

	// Optional client challenge (user_metadata.challenge, see #771): echoed
	// back in the response so the client can confirm this specific response
	// is fresh, rather than a replayed one. Validated for a sane length only
	// — the server treats it as an opaque value, not a secret.
	challenge, err := extractInviteChallenge(req.UserMetadata)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// #1190: a reconnect invite (target_user_id set) rotates the existing
	// publisher's key instead of creating a new user/org-membership.
	if invite.TargetUserID.Valid {
		redeemPublisherReconnectInvite(w, invite, int(invite.TargetUserID.Int64), orgID, req.ClientPubkey, challenge)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		"INSERT INTO users (role, display_name, password_hash, user_metadata) VALUES (?, ?, '', ?)",
		RolePublisher, req.Name, metadataVal,
	)
	if err != nil {
		writeError(w, "failed to create publisher account", http.StatusInternalServerError)
		return
	}
	userID, _ := result.LastInsertId()

	name := req.Name
	if name == "" {
		name = fmt.Sprintf("publisher#%d", userID)
		tx.Exec("UPDATE users SET display_name=? WHERE id=?", name, userID)
	}

	if _, err := tx.Exec(
		"INSERT INTO organization_members (organization_id, user_id) VALUES (?, ?)",
		orgID, userID,
	); err != nil {
		writeError(w, "failed to assign publisher to org", http.StatusInternalServerError)
		return
	}

	key, err := generateAPIKey()
	if err != nil {
		writeError(w, "failed to generate API key", http.StatusInternalServerError)
		return
	}
	var keyID int
	var keyExpiresAt sql.NullString
	if err := tx.QueryRow(
		"INSERT INTO api_keys (user_id, name, api_key) VALUES (?, ?, ?) RETURNING id, expires_at",
		userID, name, hashAPIKey(key),
	).Scan(&keyID, &keyExpiresAt); err != nil {
		writeError(w, "failed to create API key", http.StatusInternalServerError)
		return
	}

	tx.Exec("UPDATE invite_links SET used_at=? WHERE id=?", time.Now().UTC().Unix(), invite.ID)

	if err := tx.Commit(); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	var orgName, actorName string
	db.QueryRow("SELECT name, COALESCE(actor_name,'') FROM organizations WHERE id=?", orgID).Scan(&orgName, &actorName)

	log.Printf("invite: publisher %q (user_id=%d) created via invite link id=%d for org %d", name, userID, invite.ID, orgID)

	resp := map[string]any{
		"user_id":  int(userID),
		"org_id":   orgID,
		"org_name": orgName,
		"org_slug": actorName,
		"base_url": strings.TrimRight(config.Server.BaseURL, "/"),
	}
	// #1189: surface expires_at (same field name/format as POST
	// /api/v1/apikeys/renew) so a client can record a real expiry at connect
	// time instead of discovering one only via a later renew attempt.
	// Currently always empty since invite-created keys have no default
	// expiry, but this keeps the contract explicit and forward-compatible.
	if keyExpiresAt.Valid && keyExpiresAt.String != "" {
		resp["expires_at"] = epochStrToRFC3339(keyExpiresAt.String)
	}
	if challenge != "" {
		resp["challenge"] = challenge
	}
	if req.ClientPubkey != "" {
		encrypted, algorithm, err := encryptAPIKeyForClient(req.ClientPubkey, key)
		if err != nil {
			// The API key was already committed to the DB; the account exists
			// and its key can still be reset via the admin UI, so fail closed
			// on the response rather than leaking it in plaintext.
			writeError(w, "failed to encrypt API key for client_pubkey", http.StatusInternalServerError)
			return
		}
		resp["api_key_encrypted"] = encrypted
		resp["encryption_algorithm"] = algorithm
	} else {
		resp["api_key"] = key
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// redeemPublisherReconnectInvite handles redemption of an invite carrying
// target_user_id (#1190): rotates the existing publisher's API key instead
// of creating a new user/org-membership, returning the same response shape
// as a fresh publisher invite (see redeemPublisherInvite) so a client that
// already knows how to redeem a connect link needs no changes to consume a
// reconnect link too.
func redeemPublisherReconnectInvite(w http.ResponseWriter, invite inviteRecord, userID, orgID int, clientPubkey, challenge string) {
	tx, err := db.Begin()
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var name, role string
	if err := tx.QueryRow("SELECT COALESCE(display_name,''), role FROM users WHERE id=?", userID).Scan(&name, &role); err != nil {
		writeError(w, "publisher account no longer exists", http.StatusGone)
		return
	}
	if role != RolePublisher {
		writeError(w, "target account is no longer a publisher", http.StatusGone)
		return
	}
	if name == "" {
		name = fmt.Sprintf("publisher#%d", userID)
	}

	// Same rotation as regeneratePublisherKey: drop any existing key(s) for
	// this user before minting the new one.
	tx.Exec("DELETE FROM api_keys WHERE user_id=?", userID)

	key, err := generateAPIKey()
	if err != nil {
		writeError(w, "failed to generate API key", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(
		"INSERT INTO api_keys (user_id, name, api_key) VALUES (?, ?, ?)",
		userID, name, hashAPIKey(key),
	); err != nil {
		writeError(w, "failed to create API key", http.StatusInternalServerError)
		return
	}

	tx.Exec("UPDATE invite_links SET used_at=? WHERE id=?", time.Now().UTC().Unix(), invite.ID)

	if err := tx.Commit(); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	credentials.pruneByUserID(userID)

	var orgName, actorName string
	db.QueryRow("SELECT name, COALESCE(actor_name,'') FROM organizations WHERE id=?", orgID).Scan(&orgName, &actorName)

	log.Printf("invite: publisher %q (user_id=%d) reconnected via invite link id=%d for org %d", name, userID, invite.ID, orgID)

	resp := map[string]any{
		"user_id":  userID,
		"org_id":   orgID,
		"org_name": orgName,
		"org_slug": actorName,
		"base_url": strings.TrimRight(config.Server.BaseURL, "/"),
	}
	if challenge != "" {
		resp["challenge"] = challenge
	}
	if clientPubkey != "" {
		encrypted, algorithm, err := encryptAPIKeyForClient(clientPubkey, key)
		if err != nil {
			// The new key was already committed to the DB; it can still be reset
			// via the admin UI, so fail closed on the response rather than
			// leaking it in plaintext.
			writeError(w, "failed to encrypt API key for client_pubkey", http.StatusInternalServerError)
			return
		}
		resp["api_key_encrypted"] = encrypted
		resp["encryption_algorithm"] = algorithm
	} else {
		resp["api_key"] = key
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}
