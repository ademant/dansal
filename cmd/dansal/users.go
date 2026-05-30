package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin     = "admin"
	RoleUser      = "user"
	RolePublisher = "publisher"
)

type User struct {
	ID               int    `json:"id"`
	Email            string `json:"email"`
	DisplayName      string `json:"display_name,omitempty"`
	Role             string `json:"role"`
	Description      string `json:"description,omitempty"`
	Telegram         string `json:"telegram,omitempty"`
	TelegramChatID   string `json:"telegram_chat_id,omitempty"`
	Matrix           string `json:"matrix,omitempty"`
	Mastodon         string `json:"mastodon,omitempty"`
	Website          string `json:"website,omitempty"`
	EmailVerified    bool   `json:"email_verified"`
	TelegramVerified bool   `json:"telegram_verified"`
	MatrixVerified   bool   `json:"matrix_verified"`
	Disabled         bool   `json:"disabled"`
	HasPassword      bool   `json:"has_password"`
	CreatedAt        string `json:"created_at"`
}

// DisplayOrEmail returns the user's display name, or the local part of the email if unset.
func (u User) DisplayOrEmail() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	if idx := strings.Index(u.Email, "@"); idx > 0 {
		return u.Email[:idx]
	}
	return u.Email
}

type UserCreateRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Role        string `json:"role"`
	Description string `json:"description"`
	Telegram    string `json:"telegram"`
	Matrix      string `json:"matrix"`
	Mastodon    string `json:"mastodon"`
	Website     string `json:"website"`
}

type UserUpdateRequest struct {
	Email            string `json:"email"`
	DisplayName      string `json:"display_name"`
	Role             string `json:"role"`
	Description      string `json:"description"`
	Telegram         string `json:"telegram"`
	Matrix           string `json:"matrix"`
	Mastodon         string `json:"mastodon"`
	Website          string `json:"website"`
	EmailVerified    *bool  `json:"email_verified"`
	TelegramVerified *bool  `json:"telegram_verified"`
	MatrixVerified   *bool  `json:"matrix_verified"`
	Disabled         *bool  `json:"disabled"`
}

// passwordBytes returns the bcrypt input for password.
func passwordBytes(password string) []byte {
	h := sha256.Sum256([]byte(password)) //nolint:gosec
	return h[:]
}

// hashPassword hashes password with bcrypt (DefaultCost) over a SHA-256 digest.
func hashPassword(password string) string {
	if password == "" {
		return ""
	}
	h, err := bcrypt.GenerateFromPassword(passwordBytes(password), bcrypt.DefaultCost)
	if err != nil {
		panic(fmt.Sprintf("bcrypt: %v", err))
	}
	return string(h)
}

// checkPassword verifies password against stored hash.
func checkPassword(password, stored string) (ok, migrate bool) {
	if stored == "" {
		return false, false
	}
	if strings.HasPrefix(stored, "$2") {
		if bcrypt.CompareHashAndPassword([]byte(stored), passwordBytes(password)) == nil {
			return true, false
		}
		if bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil {
			return true, true
		}
		return false, false
	}
	sum := sha256.Sum256([]byte(password)) //nolint:gosec
	if fmt.Sprintf("%x", sum) == stored {
		return true, true
	}
	return false, false
}

// validateRole checks if the role is valid
func validateRole(role string) bool {
	return role == RoleAdmin || role == RoleUser || role == RolePublisher
}

const userSelectCols = "id, email, COALESCE(display_name,''), role, COALESCE(description,''), COALESCE(telegram,''), COALESCE(telegram_chat_id,''), COALESCE(matrix,''), COALESCE(mastodon,''), COALESCE(website,''), COALESCE(email_verified,0), COALESCE(telegram_verified,0), COALESCE(matrix_verified,0), COALESCE(disabled,0), CASE WHEN password_hash != '' AND password_hash IS NOT NULL THEN 1 ELSE 0 END, created_at"

type userScanner interface{ Scan(...any) error }

func scanUser(s userScanner) (User, error) {
	var u User
	var emailVer, telegramVer, matrixVer, disabled, hasPw int
	if err := s.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Description, &u.Telegram, &u.TelegramChatID, &u.Matrix, &u.Mastodon, &u.Website, &emailVer, &telegramVer, &matrixVer, &disabled, &hasPw, &u.CreatedAt); err != nil {
		return User{}, err
	}
	u.HasPassword = hasPw != 0
	u.EmailVerified = emailVer == 1
	u.TelegramVerified = telegramVer == 1
	u.MatrixVerified = matrixVer == 1
	u.Disabled = disabled == 1
	return u, nil
}

func getUserByID(id int) (User, error) {
	return scanUser(db.QueryRow("SELECT "+userSelectCols+" FROM users WHERE id=?", id))
}

func getUserByEmail(email string) (User, error) {
	return scanUser(db.QueryRow("SELECT "+userSelectCols+" FROM users WHERE email=?", email))
}

// GET /api/v1/users - List all users
func getUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	rows, err := db.Query("SELECT " + userSelectCols + " FROM users")
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		users = append(users, user)
	}

	json.NewEncoder(w).Encode(users)
}

// POST /api/v1/users - Create a new user (admin only)
func createUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden: only admins may create users directly; use invite links instead", http.StatusForbidden)
		return
	}

	var req UserCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Email == "" {
		writeError(w, "Email is required", http.StatusBadRequest)
		return
	}

	if req.Role == "" {
		req.Role = RoleUser
	}
	if !validateRole(req.Role) {
		writeError(w, "Invalid role. Allowed values: admin, user, publisher", http.StatusBadRequest)
		return
	}

	result, err := db.Exec(
		"INSERT INTO users (email, display_name, password_hash, role, telegram, matrix) VALUES (?, ?, ?, ?, ?, ?)",
		req.Email, req.DisplayName, hashPassword(req.Password), req.Role, req.Telegram, req.Matrix,
	)
	if err != nil {
		writeError(w, "Email already exists", http.StatusConflict)
		return
	}

	id, _ := result.LastInsertId()
	user := User{
		ID:          int(id),
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Role:        req.Role,
		Telegram:    req.Telegram,
		Matrix:      req.Matrix,
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(user)
}

// GET /api/v1/users/{id} - Get a specific user
func getUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	id := r.PathValue("id")
	user, err := scanUser(db.QueryRow("SELECT "+userSelectCols+" FROM users WHERE id = ?", id))
	if err == sql.ErrNoRows {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(user)
}

// PUT /api/v1/users/{id} - Update user
func updateUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	id := r.PathValue("id")
	targetID, err := strconv.Atoi(id)
	if err != nil {
		writeError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	requesterID, requesterRole := callerFromRequest(r)

	var req UserUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Role != "" && !validateRole(req.Role) {
		writeError(w, "Invalid role. Allowed values: admin, user, publisher", http.StatusBadRequest)
		return
	}

	if requesterRole != RoleAdmin && requesterID != targetID {
		writeError(w, "Forbidden: only the user or an admin may update this account", http.StatusForbidden)
		return
	}

	if req.Role != "" && requesterRole != RoleAdmin {
		writeError(w, "Forbidden: only admin may change role", http.StatusForbidden)
		return
	}

	user, err := scanUser(db.QueryRow("SELECT "+userSelectCols+" FROM users WHERE id = ?", id))
	if err == sql.ErrNoRows {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if req.Email != "" && req.Email != user.Email {
		if !isValidEmail(req.Email) {
			writeError(w, "invalid email address", http.StatusUnprocessableEntity)
			return
		}
		if err := validateEmailDomain(r.Context(), req.Email); err != nil {
			writeError(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		user.Email = req.Email
		user.EmailVerified = false
	}
	if req.DisplayName != "" {
		user.DisplayName = req.DisplayName
	}
	if req.Role != "" {
		user.Role = req.Role
	}
	if req.Description != "" {
		user.Description = req.Description
	}
	if req.Telegram != "" && req.Telegram != user.Telegram {
		user.Telegram = req.Telegram
		user.TelegramVerified = false
		user.TelegramChatID = ""
	}
	if req.Matrix != "" && req.Matrix != user.Matrix {
		if !isValidMatrixID(req.Matrix) {
			writeError(w, "Invalid Matrix ID: must be @localpart:server", http.StatusBadRequest)
			return
		}
		user.Matrix = req.Matrix
		user.MatrixVerified = false
	}
	if req.Mastodon != "" {
		user.Mastodon = req.Mastodon
	}
	if req.Website != "" {
		user.Website = req.Website
	}
	if req.EmailVerified != nil {
		if requesterRole != RoleAdmin {
			writeError(w, "Forbidden: only admin may change verification flags", http.StatusForbidden)
			return
		}
		user.EmailVerified = *req.EmailVerified
	}
	if req.TelegramVerified != nil {
		if requesterRole != RoleAdmin {
			writeError(w, "Forbidden: only admin may change verification flags", http.StatusForbidden)
			return
		}
		user.TelegramVerified = *req.TelegramVerified
	}
	if req.MatrixVerified != nil {
		if requesterRole != RoleAdmin {
			writeError(w, "Forbidden: only admin may change verification flags", http.StatusForbidden)
			return
		}
		user.MatrixVerified = *req.MatrixVerified
	}
	if req.Disabled != nil {
		if requesterRole != RoleAdmin {
			writeError(w, "Forbidden: only admin may change disabled flag", http.StatusForbidden)
			return
		}
		user.Disabled = *req.Disabled
	}

	_, err = db.Exec(
		"UPDATE users SET email=?, display_name=?, role=?, description=?, telegram=?, telegram_chat_id=?, matrix=?, mastodon=?, website=?, email_verified=?, telegram_verified=?, matrix_verified=?, disabled=? WHERE id=?",
		user.Email, user.DisplayName, user.Role, user.Description, user.Telegram, user.TelegramChatID, user.Matrix, user.Mastodon, user.Website, user.EmailVerified, user.TelegramVerified, user.MatrixVerified, user.Disabled, id,
	)
	if err != nil {
		writeError(w, "Failed to update user", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(user)
}

// DELETE /api/v1/users/{id} - Delete a user (admin only)
func deleteUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden: only admins or users may delete users", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	var targetID int
	var targetRole string
	err := db.QueryRow("SELECT id, role FROM users WHERE id = ?", id).Scan(&targetID, &targetRole)
	if err == sql.ErrNoRows {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if targetRole == RoleAdmin {
		writeError(w, "Forbidden: admin users may not be deleted", http.StatusForbidden)
		return
	}

	if callerRole != RoleAdmin {
		// User may only delete publishers in their own organization.
		if targetRole != RolePublisher {
			writeError(w, "Forbidden: users may only delete publishers", http.StatusForbidden)
			return
		}
		var shared int
		db.QueryRow(`
			SELECT COUNT(*) FROM organization_members om1
			JOIN organization_members om2 ON om1.organization_id = om2.organization_id
			WHERE om1.user_id = ? AND om2.user_id = ?`, callerID, targetID).Scan(&shared)
		if shared == 0 {
			writeError(w, "Forbidden: publisher does not share an organization with you", http.StatusForbidden)
			return
		}
	}

	result, err := db.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/users/me - Self-deletion by the authenticated user.
func deleteOwnAccount(w http.ResponseWriter, r *http.Request) {
	callerID, _ := callerFromRequest(r)
	if callerID == 0 {
		writeError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var role string
	if err := db.QueryRow("SELECT role FROM users WHERE id=?", callerID).Scan(&role); err != nil {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if role == RoleAdmin {
		writeError(w, "Admin accounts cannot be self-deleted", http.StatusForbidden)
		return
	}
	db.Exec("DELETE FROM users WHERE id=?", callerID)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/users/{id}/password - Admin sets a user's password directly
func setUserPassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden: only admins may set passwords", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Password == "" {
		writeError(w, "Password is required", http.StatusBadRequest)
		return
	}

	result, err := db.Exec("UPDATE users SET password_hash=? WHERE id=?", hashPassword(req.Password), id)
	if err != nil {
		writeError(w, "Failed to update password", http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/pending-invites/{id}/resend — admin; generates a fresh invite for an existing preset_email invite.
func resendInvite(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	callerID, _ := callerFromRequest(r)
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var presetEmail, roleVal string
	var orgID sql.NullInt64
	if err := db.QueryRow(
		"SELECT preset_email, role, org_id FROM invite_links WHERE id=? AND preset_email IS NOT NULL AND preset_email != '' AND used_at IS NULL",
		id,
	).Scan(&presetEmail, &roleVal, &orgID); err == sql.ErrNoRows {
		writeError(w, "pending invite not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Delete the old invite.
	db.Exec("DELETE FROM invite_links WHERE id=?", id)

	// Create a fresh one.
	newToken, err := generateInviteToken()
	if err != nil {
		writeError(w, "failed to generate token", http.StatusInternalServerError)
		return
	}
	expiresAt := time.Now().UTC().Add(72 * time.Hour).Unix()
	var orgVal any
	if orgID.Valid {
		orgVal = orgID.Int64
	}
	if _, err := db.Exec(
		"INSERT INTO invite_links (token, created_by, role, org_id, expires_at, preset_email) VALUES (?,?,?,?,?,?)",
		newToken, callerID, roleVal, orgVal, expiresAt, presetEmail,
	); err != nil {
		writeError(w, "failed to create invite", http.StatusInternalServerError)
		return
	}

	base := buildBaseURL(r)
	setupURL := base + "/invites/" + newToken
	go notifyUser("", presetEmail, "Your account setup link has been renewed",
		fmt.Sprintf("A new account setup link has been generated for you.\n\n%s\n\nThe link is valid for 72 hours.", setupURL))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"invite_url": setupURL})
}

// GET /api/v1/pending-invites — admin; lists unused invite_links with preset_email set.
func listPendingInvites(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	rows, err := db.Query(
		`SELECT id, token, role, org_id, expires_at, created_at, preset_email
		 FROM invite_links WHERE preset_email IS NOT NULL AND preset_email != '' AND used_at IS NULL
		 ORDER BY created_at DESC`,
	)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type pendingInvite struct {
		ID          int    `json:"id"`
		Token       string `json:"token"`
		Role        string `json:"role"`
		OrgID       *int   `json:"org_id,omitempty"`
		ExpiresAt   string `json:"expires_at"`
		CreatedAt   string `json:"created_at"`
		PresetEmail string `json:"preset_email"`
	}
	result := []pendingInvite{}
	for rows.Next() {
		var p pendingInvite
		var orgID sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Token, &p.Role, &orgID, &p.ExpiresAt, &p.CreatedAt, &p.PresetEmail); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		p.ExpiresAt = epochStrToRFC3339(p.ExpiresAt)
		if orgID.Valid {
			id := int(orgID.Int64)
			p.OrgID = &id
		}
		result = append(result, p)
	}
	json.NewEncoder(w).Encode(result)
}

// POST /api/v1/user/password — authenticated user sets or changes their own password.
// If the user already has a password, old_password is required for verification.
func changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	userIDStr := r.Header.Get("X-User-ID")
	userID, err := strconv.Atoi(userIDStr)
	if err != nil || userID == 0 {
		writeError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, "Password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	var existingHash string
	db.QueryRow("SELECT COALESCE(password_hash,'') FROM users WHERE id=?", userID).Scan(&existingHash)

	if existingHash != "" {
		if req.OldPassword == "" {
			writeError(w, "Current password is required", http.StatusBadRequest)
			return
		}
		ok, _ := checkPassword(req.OldPassword, existingHash)
		if !ok {
			writeError(w, "Current password is incorrect", http.StatusUnauthorized)
			return
		}
	}

	if _, err := db.Exec("UPDATE users SET password_hash=? WHERE id=?", hashPassword(req.NewPassword), userID); err != nil {
		writeError(w, "Failed to update password", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
