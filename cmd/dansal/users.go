package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin     = "admin"
	RoleUser      = "user"
	RolePublisher = "publisher"
	RoleViewer    = "viewer"
)

type User struct {
	ID               int    `json:"id"`
	Username         string `json:"username"`
	Email            string `json:"email"`
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
	CreatedAt        string `json:"created_at"`
}

type UserCreateRequest struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
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

func isReservedUsername(username string) bool {
	lower := strings.ToLower(username)
	for _, r := range config.Server.ReservedUsernames {
		if strings.ToLower(r) == lower {
			return true
		}
	}
	return false
}

// passwordBytes returns the bcrypt input for password.
// SHA-256 collapses the input to 32 bytes, preventing bcrypt's silent 72-byte truncation.
// The SHA-256 here is not used as a standalone password hash — it feeds into bcrypt.
func passwordBytes(password string) []byte {
	h := sha256.Sum256([]byte(password)) //nolint:gosec // pre-hash for bcrypt, not a standalone password hash
	return h[:]
}

// hashPassword hashes password with bcrypt (DefaultCost) over a SHA-256 digest.
func hashPassword(password string) string {
	h, err := bcrypt.GenerateFromPassword(passwordBytes(password), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt.GenerateFromPassword can only fail with an invalid cost (DefaultCost
		// is always valid) or a nil/empty input (passwordBytes always returns 32 bytes).
		// Panic rather than silently storing a weak hash.
		panic(fmt.Sprintf("bcrypt: %v", err))
	}
	return string(h)
}

// checkPassword verifies password against stored hash. Three cases are handled:
//   - bcrypt with SHA-256 pre-hash (current format, migrate=false)
//   - bcrypt with raw password (previous format, migrate=true so caller re-hashes)
//   - legacy hex SHA-256 (migrate=true)
func checkPassword(password, stored string) (ok, migrate bool) {
	if strings.HasPrefix(stored, "$2") {
		if bcrypt.CompareHashAndPassword([]byte(stored), passwordBytes(password)) == nil {
			return true, false
		}
		// Older bcrypt hash created before SHA-256 pre-hashing was introduced.
		if bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil {
			return true, true
		}
		return false, false
	}
	// Legacy hex SHA-256 hash — accepted only to trigger migration to bcrypt.
	sum := sha256.Sum256([]byte(password)) //nolint:gosec // compared against stored legacy hash, triggers re-hash to bcrypt on success
	if fmt.Sprintf("%x", sum) == stored {
		return true, true
	}
	return false, false
}

// validateRole checks if the role is valid
func validateRole(role string) bool {
	return role == RoleAdmin || role == RoleUser || role == RolePublisher || role == RoleViewer
}


// GET /api/v1/users - List all users
func getUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	rows, err := db.Query("SELECT id, username, email, role, COALESCE(description,''), COALESCE(telegram,''), COALESCE(telegram_chat_id,''), COALESCE(matrix,''), COALESCE(mastodon,''), COALESCE(website,''), COALESCE(email_verified,0), COALESCE(telegram_verified,0), COALESCE(matrix_verified,0), COALESCE(disabled,0), created_at FROM users")
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var user User
		var emailVer, telegramVer, matrixVer, disabled int
		if err := rows.Scan(&user.ID, &user.Username, &user.Email, &user.Role, &user.Description, &user.Telegram, &user.TelegramChatID, &user.Matrix, &user.Mastodon, &user.Website, &emailVer, &telegramVer, &matrixVer, &disabled, &user.CreatedAt); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		user.EmailVerified = emailVer == 1
		user.TelegramVerified = telegramVer == 1
		user.MatrixVerified = matrixVer == 1
		user.Disabled = disabled == 1
		users = append(users, user)
	}

	json.NewEncoder(w).Encode(users)
}

// POST /api/v1/users - Create a new user
func createUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden: only admins may create users directly; use invite links instead", http.StatusForbidden)
		return
	}

	var req UserCreateRequest

	// Parse request body based on content type
	contentType := r.Header.Get("Content-Type")
	if contentType == "application/x-www-form-urlencoded" {
		// Parse form data
		if err := r.ParseForm(); err != nil {
			writeError(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		req.Username = r.FormValue("username")
		req.Email = r.FormValue("email")
		req.Password = r.FormValue("password")
		req.Role = r.FormValue("role")
		req.Telegram = r.FormValue("telegram")
		req.Matrix = r.FormValue("matrix")
	} else {
		// Default to JSON parsing
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
	}

	// Validate input
	if req.Username == "" || req.Email == "" || req.Password == "" {
		writeError(w, "Username, email, and password are required", http.StatusBadRequest)
		return
	}

	if isReservedUsername(req.Username) {
		writeError(w, "Username is reserved", http.StatusBadRequest)
		return
	}

	// Set default role if not provided
	if req.Role == "" {
		req.Role = RoleUser
	}

	if !validateRole(req.Role) {
		writeError(w, "Invalid role. Allowed values: admin, user, publisher, viewer", http.StatusBadRequest)
		return
	}

	// Hash password
	passwordHash := hashPassword(req.Password)

	// Insert user
	result, err := db.Exec(
		"INSERT INTO users (username, email, password_hash, role, telegram, matrix) VALUES (?, ?, ?, ?, ?, ?)",
		req.Username, req.Email, passwordHash, req.Role, req.Telegram, req.Matrix,
	)
	if err != nil {
		writeError(w, "Username or email already exists", http.StatusConflict)
		return
	}

	id, _ := result.LastInsertId()
	user := User{
		ID:       int(id),
		Username: req.Username,
		Email:    req.Email,
		Role:     req.Role,
		Telegram: req.Telegram,
		Matrix:   req.Matrix,
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(user)
}

// GET /api/v1/users/{id} - Get a specific user
func getUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	id := r.PathValue("id")

	var user User
	var emailVer, telegramVer, matrixVer, disabled int
	err := db.QueryRow(
		"SELECT id, username, email, role, COALESCE(description,''), COALESCE(telegram,''), COALESCE(telegram_chat_id,''), COALESCE(matrix,''), COALESCE(mastodon,''), COALESCE(website,''), COALESCE(email_verified,0), COALESCE(telegram_verified,0), COALESCE(matrix_verified,0), COALESCE(disabled,0), created_at FROM users WHERE id = ?",
		id,
	).Scan(&user.ID, &user.Username, &user.Email, &user.Role, &user.Description, &user.Telegram, &user.TelegramChatID, &user.Matrix, &user.Mastodon, &user.Website, &emailVer, &telegramVer, &matrixVer, &disabled, &user.CreatedAt)
	user.EmailVerified = emailVer == 1
	user.TelegramVerified = telegramVer == 1
	user.MatrixVerified = matrixVer == 1
	user.Disabled = disabled == 1

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

// PUT /api/v1/users/{id} - Update user (email and role)
func updateUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	id := r.PathValue("id")
	targetID, err := strconv.Atoi(id)
	if err != nil {
		writeError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	requesterIDStr := r.Header.Get("X-User-ID")
	requesterRole := r.Header.Get("X-User-Role")
	requesterID, err := strconv.Atoi(requesterIDStr)
	if err != nil {
		writeError(w, "Invalid requester information", http.StatusUnauthorized)
		return
	}

	var req UserUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate role if provided
	if req.Role != "" && !validateRole(req.Role) {
		writeError(w, "Invalid role. Allowed values: admin, user, publisher, viewer", http.StatusBadRequest)
		return
	}

	// Only admin or the user themself can update user details
	if requesterRole != RoleAdmin && requesterID != targetID {
		writeError(w, "Forbidden: only the user or an admin may update this account", http.StatusForbidden)
		return
	}

	// Regular users may not change their own role
	if req.Role != "" && requesterRole != RoleAdmin {
		writeError(w, "Forbidden: only admin may change role", http.StatusForbidden)
		return
	}

	// Check if user exists
	var user User
	var emailVer, telegramVer, matrixVer, disabledInt int
	err = db.QueryRow("SELECT id, username, email, role, COALESCE(description,''), COALESCE(telegram,''), COALESCE(telegram_chat_id,''), COALESCE(matrix,''), COALESCE(mastodon,''), COALESCE(website,''), COALESCE(email_verified,0), COALESCE(telegram_verified,0), COALESCE(matrix_verified,0), COALESCE(disabled,0), created_at FROM users WHERE id = ?", id).
		Scan(&user.ID, &user.Username, &user.Email, &user.Role, &user.Description, &user.Telegram, &user.TelegramChatID, &user.Matrix, &user.Mastodon, &user.Website, &emailVer, &telegramVer, &matrixVer, &disabledInt, &user.CreatedAt)
	if err == sql.ErrNoRows {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	user.EmailVerified = emailVer == 1
	user.TelegramVerified = telegramVer == 1
	user.MatrixVerified = matrixVer == 1
	user.Disabled = disabledInt == 1

	if req.Email != "" && req.Email != user.Email {
		user.Email = req.Email
		user.EmailVerified = false
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
		"UPDATE users SET email = ?, role = ?, description = ?, telegram = ?, telegram_chat_id = ?, matrix = ?, mastodon = ?, website = ?, email_verified = ?, telegram_verified = ?, matrix_verified = ?, disabled = ? WHERE id = ?",
		user.Email, user.Role, user.Description, user.Telegram, user.TelegramChatID, user.Matrix, user.Mastodon, user.Website, user.EmailVerified, user.TelegramVerified, user.MatrixVerified, user.Disabled, id,
	)
	if err != nil {
		writeError(w, "Failed to update user", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(user)
}

// isValidMatrixID checks that s is a fully-qualified Matrix user ID: @localpart:server
func isValidMatrixID(s string) bool {
	if !strings.HasPrefix(s, "@") {
		return false
	}
	colon := strings.IndexByte(s, ':')
	return colon > 1 && colon < len(s)-1
}

// DELETE /api/v1/users/{id} - Delete a user
func deleteUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	requesterRole := r.Header.Get("X-User-Role")
	if requesterRole != RoleAdmin {
		writeError(w, "Forbidden: only admins may delete users", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")

	// Check if user exists and prevent deletion of admin accounts
	var userID int
	var userRole string
	err := db.QueryRow("SELECT id, role FROM users WHERE id = ?", id).Scan(&userID, &userRole)
	if err == sql.ErrNoRows {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if userRole == RoleAdmin {
		writeError(w, "Forbidden: admin users may not be deleted", http.StatusForbidden)
		return
	}

	// Delete user
	result, err := db.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
