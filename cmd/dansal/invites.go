package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// roleRank returns a numeric rank for role comparison (higher = more privileged).
func roleRank(role string) int {
	switch role {
	case RoleAdmin:
		return 4
	case RoleUser:
		return 3
	case RolePublisher:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

type InviteLink struct {
	ID        int    `json:"id"`
	Token     string `json:"token"`
	Role      string `json:"role"`
	OrgID     *int   `json:"org_id,omitempty"`
	ExpiresAt string `json:"expires_at"`
	UsedAt    string `json:"used_at,omitempty"`
	CreatedAt string `json:"created_at"`
}

type CreateInviteRequest struct {
	Role  string `json:"role"`
	OrgID *int   `json:"org_id,omitempty"`
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
	if req.Role == "" {
		req.Role = RoleUser
	}
	if !validateRole(req.Role) {
		writeError(w, "Invalid role", http.StatusBadRequest)
		return
	}
	if roleRank(req.Role) > roleRank(callerRole) {
		writeError(w, "Forbidden: cannot invite a user with higher role than your own", http.StatusForbidden)
		return
	}

	// If no org_id supplied, inherit the caller's first org membership.
	orgID := req.OrgID
	if orgID == nil {
		var id int
		if err := db.QueryRow("SELECT organization_id FROM organization_members WHERE user_id=? LIMIT 1", callerID).Scan(&id); err == nil {
			orgID = &id
		}
	}
	// Verify the org exists if specified.
	if orgID != nil {
		var exists int
		if err := db.QueryRow("SELECT COUNT(*) FROM organizations WHERE id=?", *orgID).Scan(&exists); err != nil || exists == 0 {
			writeError(w, "Organization not found", http.StatusBadRequest)
			return
		}
	}

	token, err := generateInviteToken()
	if err != nil {
		writeError(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	expiryHours := config.Server.InviteExpiryHours
	expiresAt := time.Now().UTC().Add(time.Duration(expiryHours) * time.Hour)

	var orgVal any
	if orgID != nil {
		orgVal = *orgID
	}
	_, err = db.Exec(
		"INSERT INTO invite_links (token, created_by, role, org_id, expires_at) VALUES (?, ?, ?, ?, ?)",
		token, callerID, req.Role, orgVal, expiresAt.Unix(),
	)
	if err != nil {
		writeError(w, "Failed to create invite link", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(InviteLink{
		Token:     token,
		Role:      req.Role,
		OrgID:     orgID,
		ExpiresAt: expiresAt.Format(time.RFC3339),
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
			"SELECT id, token, role, org_id, expires_at, COALESCE(used_at,''), created_at FROM invite_links ORDER BY created_at DESC",
		)
	} else {
		rows, err = db.Query(
			"SELECT id, token, role, org_id, expires_at, COALESCE(used_at,''), created_at FROM invite_links WHERE created_by=? ORDER BY created_at DESC",
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
		if err := rows.Scan(&l.ID, &l.Token, &l.Role, &orgID, &l.ExpiresAt, &l.UsedAt, &l.CreatedAt); err != nil {
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

	var invite struct {
		ID          int
		Role        string
		OrgID       sql.NullInt64
		ExpiresAt   string
		UsedAt      string
		PresetEmail string
	}
	err := db.QueryRow(
		`SELECT id, role, org_id, expires_at, COALESCE(used_at,''), COALESCE(preset_email,'')
		 FROM invite_links WHERE token=?`,
		token,
	).Scan(&invite.ID, &invite.Role, &invite.OrgID, &invite.ExpiresAt, &invite.UsedAt, &invite.PresetEmail)
	if err == sql.ErrNoRows {
		writeError(w, "Invalid or expired invite link", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if invite.UsedAt != "" {
		writeError(w, "Invite link has already been used", http.StatusGone)
		return
	}
	exp, err := parseTokenExpiration(invite.ExpiresAt)
	if err != nil || time.Now().After(exp) {
		writeError(w, "Invite link has expired", http.StatusGone)
		return
	}

	var req UseInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
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

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		"INSERT INTO users (email, display_name, password_hash, role, telegram, matrix) VALUES (?, ?, ?, ?, ?, ?)",
		req.Email, req.DisplayName, hashPassword(req.Password), invite.Role, req.Telegram, req.Matrix,
	)
	if err != nil {
		writeError(w, "Username or email already exists", http.StatusConflict)
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
