package main

import (
	"context"
	"crypto/sha1" //nolint:gosec // HIBP k-anonymity API requires SHA-1
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
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
	TOTPEnabled      bool   `json:"totp_enabled"`
	UserMetadata     string `json:"user_metadata,omitempty"`
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

// hashPassword hashes password with the configured KDF (argon2id by
// default, or pbkdf2 for FIPS 140 environments — see config.go's
// PasswordKDF and #802).
func hashPassword(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	if passwordKDF() == "pbkdf2" {
		return hashPBKDF2(password)
	}
	return hashArgon2id(password)
}

// checkPassword verifies password against stored hash, which may be in any
// format ever produced by hashPassword (argon2id, pbkdf2) or the legacy
// bcrypt/raw-SHA-256 formats it replaced. migrate reports whether stored
// should be replaced with hashPassword(password) — either because it's in
// a deprecated format (bcrypt, legacy SHA-256) or because it doesn't match
// the currently configured KDF.
func checkPassword(password, stored string) (ok, migrate bool) {
	if stored == "" {
		return false, false
	}
	switch {
	case strings.HasPrefix(stored, "$argon2id$"):
		if checkArgon2id(password, stored) {
			return true, passwordKDF() != "argon2id"
		}
		return false, false
	case strings.HasPrefix(stored, "$pbkdf2-sha256$"):
		if checkPBKDF2(password, stored) {
			return true, passwordKDF() != "pbkdf2"
		}
		return false, false
	case strings.HasPrefix(stored, "$2"):
		if bcrypt.CompareHashAndPassword([]byte(stored), passwordBytes(password)) == nil {
			return true, true
		}
		if bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil {
			return true, true
		}
		return false, false
	default:
		sum := sha256.Sum256([]byte(password)) //nolint:gosec
		if fmt.Sprintf("%x", sum) == stored {
			return true, true
		}
		return false, false
	}
}

// validateRole checks if the role is valid
func validateRole(role string) bool {
	return role == RoleAdmin || role == RoleUser || role == RolePublisher
}

const userSelectCols = "id, COALESCE(email,''), COALESCE(display_name,''), role, COALESCE(description,''), COALESCE(telegram,''), COALESCE(telegram_chat_id,''), COALESCE(matrix,''), COALESCE(mastodon,''), COALESCE(website,''), COALESCE(email_verified,0), COALESCE(telegram_verified,0), COALESCE(matrix_verified,0), COALESCE(disabled,0), CASE WHEN password_hash != '' AND password_hash IS NOT NULL THEN 1 ELSE 0 END, CASE WHEN totp_secret IS NOT NULL AND totp_secret != '' THEN 1 ELSE 0 END, COALESCE(user_metadata,''), created_at"

type userScanner interface{ Scan(...any) error }

func scanUser(s userScanner) (User, error) {
	var u User
	var emailVer, telegramVer, matrixVer, disabled, hasPw, totpEnabled int
	if err := s.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Description, &u.Telegram, &u.TelegramChatID, &u.Matrix, &u.Mastodon, &u.Website, &emailVer, &telegramVer, &matrixVer, &disabled, &hasPw, &totpEnabled, &u.UserMetadata, &u.CreatedAt); err != nil {
		return User{}, err
	}
	u.HasPassword = hasPw != 0
	u.TOTPEnabled = totpEnabled != 0
	u.EmailVerified = emailVer == 1
	u.TelegramVerified = telegramVer == 1
	u.MatrixVerified = matrixVer == 1
	u.Disabled = disabled == 1
	return u, nil
}

// publicUser strips PII fields that only the user themselves (or, for the
// list endpoint, no one but the user) should see. Only id, display_name,
// role, description, website, and created_at survive.
func publicUser(u User) User {
	return User{
		ID:          u.ID,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		Description: u.Description,
		Website:     u.Website,
		CreatedAt:   u.CreatedAt,
	}
}

// listUser strips the subset of PII that must never appear in the bulk
// listing endpoint, even to admins. Full PII remains available via
// getUser when the caller is requesting their own record.
func listUser(u User) User {
	u.Email = ""
	u.Telegram = ""
	u.TelegramChatID = ""
	u.Matrix = ""
	return u
}

func getUserByID(id int) (User, error) {
	return scanUser(db.QueryRow("SELECT "+userSelectCols+" FROM users WHERE id=?", id))
}

func getUserByEmail(email string) (User, error) {
	return scanUser(db.QueryRow("SELECT "+userSelectCols+" FROM users WHERE email=?", email))
}

// resolveDisplayName returns callerID's display name (falling back to the
// email local-part, then the numeric ID) for "last updated by" attribution
// on events/locations/organizations/musicians/instructors.
func resolveDisplayName(callerID int) string {
	var name string
	if err := db.QueryRow(
		"SELECT COALESCE(NULLIF(display_name,''), SUBSTR(email,1,INSTR(email,'@')-1)) FROM users WHERE id = ?", callerID,
	).Scan(&name); err != nil || name == "" {
		return strconv.Itoa(callerID)
	}
	return name
}

// isDisplayNameTaken reports whether another user already holds name
// (case-insensitively). excludeID is the current user's ID for updates; pass 0
// for new insertions (no user has id=0 in an AUTOINCREMENT table).
func isDisplayNameTaken(name string, excludeID int64) bool {
	if name == "" {
		return false
	}
	var count int
	db.QueryRow(
		"SELECT COUNT(*) FROM users WHERE display_name=? COLLATE NOCASE AND id!=?",
		name, excludeID,
	).Scan(&count)
	return count > 0
}

// GET /api/v1/users - List all users (admin only; PII stripped even for admins)
func getUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	_, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin {
		writeError(w, "Forbidden: admin only", http.StatusForbidden)
		return
	}

	rows, err := db.Query("SELECT " + userSelectCols + " FROM users")
	if err != nil {
		log.Printf("getUsers: db query: %v", err)
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			log.Printf("getUsers: scan: %v", err)
			writeError(w, "internal error", http.StatusInternalServerError)
			return
		}
		users = append(users, listUser(user))
	}

	json.NewEncoder(w).Encode(users)
}

// GET /api/v1/users/{id} - Get a specific user. Callers requesting their own
// record get full PII; everyone else gets only the public subset.
func getUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	targetID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}
	user, err := scanUser(db.QueryRow("SELECT "+userSelectCols+" FROM users WHERE id = ?", targetID))
	if err == sql.ErrNoRows {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("getUser: scan: %v", err)
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if callerID, _ := callerFromRequest(r); callerID != targetID {
		user = publicUser(user)
	}
	json.NewEncoder(w).Encode(user)
}

// GET /api/v1/users/{id}/organizations - IDs of organizations the user belongs to
func getUserOrganizations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	targetID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	requesterID, requesterRole := callerFromRequest(r)
	if requesterRole != RoleAdmin && requesterID != targetID {
		writeError(w, "Forbidden: only the user or an admin may view this", http.StatusForbidden)
		return
	}

	orgSet := userOrgSet(targetID)
	ids := make([]int, 0, len(orgSet))
	for id := range orgSet {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	json.NewEncoder(w).Encode(map[string][]int{"organization_ids": ids})
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
	if !decodeJSONBody(w, r, &req) {
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
		writeInternalError(w, err)
		return
	}

	// Publishers are locked to their initial organisation — org reassignment is blocked.
	if user.Role == RolePublisher && req.Role != "" && req.Role != RolePublisher {
		writeError(w, "Publisher accounts cannot change role", http.StatusBadRequest)
		return
	}

	if req.Email != "" && req.Email != user.Email {
		// Only self-service email changes go through the API; an admin
		// changing someone else's email is not exposed here (#613).
		if requesterID != targetID {
			writeError(w, "Forbidden: only the account owner may change their email", http.StatusForbidden)
			return
		}
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
		if req.DisplayName != user.DisplayName && isDisplayNameTaken(req.DisplayName, int64(targetID)) {
			writeError(w, "Display name is already taken", http.StatusConflict)
			return
		}
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
	// Evict cached credentials immediately so a role change or disable takes
	// effect on the next request instead of lingering for credCacheTTL.
	credentials.pruneByUserID(targetID)

	json.NewEncoder(w).Encode(user)
}

// GET /api/v1/me - Full profile for the authenticated user, plus token expiry.
func getMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, _ := callerFromRequest(r)
	if callerID == 0 {
		writeError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user, err := getUserByID(callerID)
	if err != nil {
		log.Printf("getMe: %v", err)
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Look up token expiry so callers can re-issue session cookies with the right TTL.
	var rawExpiry string
	if parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2); len(parts) == 2 {
		db.QueryRow("SELECT expires_at FROM tokens WHERE token = ?", sha256Hex(parts[1])).Scan(&rawExpiry)
	}
	type meResponse struct {
		User
		TokenExpiresAt string `json:"token_expires_at,omitempty"`
	}
	json.NewEncoder(w).Encode(meResponse{User: user, TokenExpiresAt: epochStrToRFC3339(rawExpiry)})
}

// GET /api/v1/me/stats - Event counts for the authenticated user.
func getMeStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, _ := callerFromRequest(r)
	var created, lastEdited int
	db.QueryRow("SELECT COUNT(*) FROM events WHERE created_by_id = ?", callerID).Scan(&created)
	db.QueryRow("SELECT COUNT(*) FROM events WHERE changed_by_id = ?", callerID).Scan(&lastEdited)
	json.NewEncoder(w).Encode(map[string]int{
		"events_created":     created,
		"events_last_edited": lastEdited,
	})
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

// POST /api/v1/pending-invites/{id}/resend — admin; generates a fresh invite for an existing preset_email invite.
func resendInvite(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	callerID, _ := callerFromRequest(r)
	id, err := intPathValue(r, "id")
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
	expiresAtTime := time.Now().UTC().Add(time.Duration(config.Server.InviteExpiryHours) * time.Hour)
	var orgVal any
	var orgIDPtr *int
	if orgID.Valid {
		orgVal = orgID.Int64
		id := int(orgID.Int64)
		orgIDPtr = &id
	}
	newToken, err := signInviteJWT(roleVal, orgIDPtr, inviteTokenType(roleVal), expiresAtTime)
	if err != nil {
		writeError(w, "failed to generate token", http.StatusInternalServerError)
		return
	}
	if _, err := db.Exec(
		"INSERT INTO invite_links (token, created_by, role, org_id, expires_at, preset_email) VALUES (?,?,?,?,?,?)",
		newToken, callerID, roleVal, orgVal, expiresAtTime.Unix(), presetEmail,
	); err != nil {
		writeError(w, "failed to create invite", http.StatusInternalServerError)
		return
	}

	base := buildBaseURL(r)
	setupURL := base + "/invites/" + newToken
	go notifyUser("", presetEmail, "Your account setup link has been renewed",
		fmt.Sprintf("A new account setup link has been generated for you.\n\n%s\n\nThe link is valid for %d hours.", setupURL, config.Server.InviteExpiryHours))

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
		writeInternalError(w, err)
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
			writeInternalError(w, err)
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

// isPasswordPwned checks whether the given password appears in the HaveIBeenPwned
// breach database using the k-anonymity API (only the first 5 hex chars of the
// SHA-1 hash are sent to the server). Returns true when the password was found.
// On any network/API error it returns false (fail open) so a transient HIBP
// outage never blocks users from changing their password.
func isPasswordPwned(ctx context.Context, password string) bool {
	h := sha1.New() //nolint:gosec // SHA-1 mandated by HIBP k-anonymity API
	h.Write([]byte(password))
	hash := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
	prefix, suffix := hash[:5], hash[5:]

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.pwnedpasswords.com/range/"+prefix, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Add-Padding", "true")

	resp, err := safeClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false
	}

	for _, line := range strings.Split(string(body), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], suffix) {
			return true
		}
	}
	return false
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
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, "Password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if isPasswordPwned(r.Context(), req.NewPassword) {
		writeError(w, "This password has appeared in a data breach. Please choose a different password.", http.StatusBadRequest)
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

	newHash, err := hashPassword(req.NewPassword)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if _, err := db.Exec("UPDATE users SET password_hash=? WHERE id=?", newHash, userID); err != nil {
		writeError(w, "Failed to update password", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
