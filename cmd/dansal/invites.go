package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"
)

// inviteRecord holds the invite_links columns needed to validate and redeem
// an invite.
type inviteRecord struct {
	ID          int
	Role        string
	OrgID       sql.NullInt64
	ExpiresAt   string
	UsedAt      string
	PresetEmail string
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
		`SELECT id, role, org_id, expires_at, COALESCE(used_at,''), COALESCE(preset_email,'')
		 FROM invite_links WHERE token=?`, token,
	).Scan(&invite.ID, &invite.Role, &invite.OrgID, &invite.ExpiresAt, &invite.UsedAt, &invite.PresetEmail)
	if err != nil {
		return inviteRecord{}, err
	}
	if invite.UsedAt != "" {
		return inviteRecord{}, errInviteUsed
	}
	if exp, err2 := parseTokenExpiration(invite.ExpiresAt); err2 != nil || time.Now().After(exp) {
		return inviteRecord{}, errInviteExpired
	}
	return invite, nil
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

func generateInviteToken() (string, error) {
	return generateToken(24)
}

// POST /api/v1/invites — create an invite link.
// Requires role admin or user. Invited role must be ≤ creator's role.
func createInvite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)

	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden: only admin and user roles may create invite links", http.StatusForbidden)
		return
	}

	var req CreateInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	// Role is always "user" for UI-created invites.
	req.Role = RoleUser

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

	// Determine TTL from invite type.
	inviteType := req.InviteType
	if inviteType != "qr" && inviteType != "link" {
		inviteType = "link"
	}
	var expiresAt time.Time
	if inviteType == "qr" {
		expiresAt = time.Now().UTC().Add(time.Duration(config.Server.InviteQRExpiryMinutes) * time.Minute)
	} else {
		expiresAt = time.Now().UTC().Add(time.Duration(config.Server.InviteExpiryHours) * time.Hour)
	}

	token, err := generateInviteToken()
	if err != nil {
		writeError(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	var orgVal any
	if orgID != nil {
		orgVal = *orgID
	}
	_, err = db.Exec(
		"INSERT INTO invite_links (token, created_by, role, org_id, expires_at, invite_type) VALUES (?, ?, ?, ?, ?, ?)",
		token, callerID, req.Role, orgVal, expiresAt.Unix(), inviteType,
	)
	if err != nil {
		writeError(w, "Failed to create invite link", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(InviteLink{
		Token:      token,
		Role:       req.Role,
		InviteType: inviteType,
		OrgID:      orgID,
		ExpiresAt:  expiresAt.Format(time.RFC3339),
	})
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
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	links := []InviteLink{}
	for rows.Next() {
		var l InviteLink
		var orgID sql.NullInt64
		if err := rows.Scan(&l.ID, &l.Token, &l.Role, &l.InviteType, &orgID, &l.ExpiresAt, &l.UsedAt, &l.CreatedAt); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
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
		writeError(w, err.Error(), http.StatusInternalServerError)
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

	invite, err := loadValidInvite(token)
	switch {
	case err == nil:
	case err == sql.ErrNoRows:
		writeError(w, "Invalid or expired invite link", http.StatusNotFound)
		return
	case err == errInviteUsed:
		writeError(w, "Invite link has already been used", http.StatusGone)
		return
	case err == errInviteExpired:
		writeError(w, "Invite link has expired", http.StatusGone)
		return
	default:
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	var req UseInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
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
	result, err := tx.Exec(
		"INSERT INTO users (email, display_name, password_hash, role, telegram, matrix, email_verified) VALUES (?, ?, ?, ?, ?, ?, ?)",
		emailVal, req.DisplayName, hashPassword(req.Password), invite.Role, req.Telegram, req.Matrix, emailVerified,
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
