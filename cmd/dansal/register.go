package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

var registerRateLimiter *RateLimiter

func initRegisterRateLimiter() {
	registerRateLimiter = NewRateLimiter(3, 10*time.Minute)
}

func selfRegEnabled() bool {
	return config.SMTP.Host != "" || config.Server.TelegramBotToken != ""
}

func randomHex32() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type RegisterRequest struct {
	Username        string `json:"username"`
	Email           string `json:"email"`
	Password        string `json:"password"`
	RegType         string `json:"reg_type"` // "join_org" | "new_org"
	OrgID           *int   `json:"org_id,omitempty"`
	OrgName         string `json:"org_name,omitempty"`
	OrgDescription  string `json:"org_description,omitempty"`
	OrgWebsite      string `json:"org_website,omitempty"`
	OrgContactEmail string `json:"org_contact_email,omitempty"`
	Channel         string `json:"channel"` // "email" | "telegram"
	Telegram        string `json:"telegram,omitempty"`
	Phone2          string `json:"phone2"` // honeypot
}

type PendingRegistration struct {
	ID                  int    `json:"id"`
	Username            string `json:"username"`
	Email               string `json:"email"`
	RegType             string `json:"reg_type"`
	OrgID               *int   `json:"org_id,omitempty"`
	OrgName             string `json:"org_name,omitempty"`
	OrgDescription      string `json:"org_description,omitempty"`
	OrgWebsite          string `json:"org_website,omitempty"`
	OrgContactEmail     string `json:"org_contact_email,omitempty"`
	VerificationChannel string `json:"verification_channel"`
	Telegram            string `json:"telegram,omitempty"`
	TelegramChatID      string `json:"telegram_chat_id,omitempty"`
	Verified            bool   `json:"verified"`
	CreatedAt           string `json:"created_at"`
	ExpiresAt           string `json:"expires_at"`
}

// POST /api/v1/register — create a pending registration.
func registerHandler(w http.ResponseWriter, r *http.Request) {
	if !selfRegEnabled() {
		writeError(w, "Self-registration is not enabled", http.StatusForbidden)
		return
	}

	ip := getIP(r)
	if !registerRateLimiter.Allow(ip) {
		writeError(w, "Rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, config.Server.MaxBodyBytes))
	if err != nil {
		writeError(w, "read error", http.StatusBadRequest)
		return
	}
	var req RegisterRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Honeypot.
	if req.Phone2 != "" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if req.Username == "" || req.Email == "" || req.Password == "" {
		writeError(w, "username, email and password are required", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		writeError(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if isReservedUsername(req.Username) {
		writeError(w, "Username is reserved", http.StatusBadRequest)
		return
	}
	if req.RegType != "join_org" && req.RegType != "new_org" {
		writeError(w, "reg_type must be 'join_org' or 'new_org'", http.StatusBadRequest)
		return
	}
	if req.RegType == "join_org" && req.OrgID == nil {
		writeError(w, "org_id is required for join_org", http.StatusBadRequest)
		return
	}
	if req.RegType == "new_org" && req.OrgName == "" {
		writeError(w, "org_name is required for new_org", http.StatusBadRequest)
		return
	}

	// Validate channel.
	channel := req.Channel
	if channel == "" {
		if config.SMTP.Host != "" {
			channel = "email"
		} else {
			channel = "telegram"
		}
	}
	if channel == "email" && config.SMTP.Host == "" {
		writeError(w, "email channel not available", http.StatusBadRequest)
		return
	}
	if channel == "telegram" && config.Server.TelegramBotToken == "" {
		writeError(w, "telegram channel not available", http.StatusBadRequest)
		return
	}

	// Check uniqueness in users table.
	var c int
	db.QueryRow("SELECT COUNT(*) FROM users WHERE username=? OR email=?", req.Username, req.Email).Scan(&c)
	if c > 0 {
		writeError(w, "Username or email already registered", http.StatusConflict)
		return
	}
	// Check uniqueness in pending_registrations.
	db.QueryRow("SELECT COUNT(*) FROM pending_registrations WHERE username=? OR email=?", req.Username, req.Email).Scan(&c)
	if c > 0 {
		writeError(w, "A registration with this username or email is already pending", http.StatusConflict)
		return
	}

	// Validate org_id for join_org.
	if req.RegType == "join_org" {
		db.QueryRow("SELECT COUNT(*) FROM organizations WHERE id=?", *req.OrgID).Scan(&c)
		if c == 0 {
			writeError(w, "Organization not found", http.StatusNotFound)
			return
		}
	}

	verificationToken, err := randomHex32()
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	approvalToken, err := randomHex32()
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().UTC().Add(72 * time.Hour).Unix()

	var orgIDArg any
	if req.OrgID != nil {
		orgIDArg = *req.OrgID
	}

	_, dbErr := db.Exec(
		`INSERT INTO pending_registrations
		 (verification_token, approval_token, username, email, password_hash,
		  reg_type, org_id, org_name, org_description, org_website, org_contact_email,
		  verification_channel, telegram, expires_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		verificationToken, approvalToken, req.Username, req.Email, hashPassword(req.Password),
		req.RegType, orgIDArg, req.OrgName, req.OrgDescription, req.OrgWebsite, req.OrgContactEmail,
		channel, req.Telegram, expiresAt,
	)
	if dbErr != nil {
		writeError(w, "db error: "+dbErr.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)

	if channel == "email" {
		base := buildBaseURL(r)
		verifyURL := base + "/api/v1/register/verify/email/" + verificationToken
		go func() {
			msg := fmt.Sprintf(
				"Hello %s,\n\nYou requested an account on this event calendar. Please confirm your email address:\n\n%s\n\nThis link expires in 72 hours. If you did not request this, you can ignore this email.",
				req.Username, verifyURL,
			)
			if err := SendEmail(req.Email, "Confirm your registration", msg); err != nil {
				log.Printf("register: send verify email: %v", err)
			}
		}()
		json.NewEncoder(w).Encode(map[string]string{"status": "verification_email_sent"})
	} else {
		// Return token for Telegram bot instructions.
		json.NewEncoder(w).Encode(map[string]string{
			"status":        "telegram_verification_required",
			"telegram_token": verificationToken,
			"bot_name":      config.Server.TelegramBotName,
		})
	}
}

// GET /api/v1/register/verify/email/{token}
func verifyEmailRegHandler(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, "token required", http.StatusBadRequest)
		return
	}

	var id int
	var expiresAt string
	err := db.QueryRow(
		"SELECT id, expires_at FROM pending_registrations WHERE verification_token=? AND verified=0",
		token,
	).Scan(&id, &expiresAt)
	if err != nil {
		writeError(w, "token not found", http.StatusNotFound)
		return
	}
	exp, err := parseTokenExpiration(expiresAt)
	if err != nil || time.Now().After(exp) {
		db.Exec("DELETE FROM pending_registrations WHERE id=?", id)
		writeError(w, "token expired", http.StatusGone)
		return
	}

	db.Exec("UPDATE pending_registrations SET verified=1 WHERE id=?", id)
	go notifyApprovers(id)
	w.WriteHeader(http.StatusOK)
}

// GET /api/v1/pending-registrations
func listPendingRegsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerRole := r.Header.Get("X-User-Role")
	callerID, _ := intHeader(r, "X-User-ID")

	var rows interface{ Next() bool; Scan(...any) error; Close() error }
	var err error
	if callerRole == RoleAdmin {
		rows, err = db.Query(
			`SELECT id, username, email, reg_type, org_id, org_name, org_description, org_website,
			 org_contact_email, verification_channel, telegram, telegram_chat_id, verified, created_at, expires_at
			 FROM pending_registrations WHERE verified=1 ORDER BY created_at ASC`,
		)
	} else {
		rows, err = db.Query(
			`SELECT pr.id, pr.username, pr.email, pr.reg_type, pr.org_id, pr.org_name, pr.org_description,
			 pr.org_website, pr.org_contact_email, pr.verification_channel, pr.telegram, pr.telegram_chat_id,
			 pr.verified, pr.created_at, pr.expires_at
			 FROM pending_registrations pr
			 JOIN organization_members om ON om.organization_id = pr.org_id AND om.user_id = ?
			 WHERE pr.verified=1 AND pr.reg_type='join_org' ORDER BY pr.created_at ASC`,
			callerID,
		)
	}
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var regs []PendingRegistration
	for rows.Next() {
		var pr PendingRegistration
		var orgID sql.NullInt64
		if err := rows.Scan(
			&pr.ID, &pr.Username, &pr.Email, &pr.RegType, &orgID,
			&pr.OrgName, &pr.OrgDescription, &pr.OrgWebsite, &pr.OrgContactEmail,
			&pr.VerificationChannel, &pr.Telegram, &pr.TelegramChatID,
			&pr.Verified, &pr.CreatedAt, &pr.ExpiresAt,
		); err != nil {
			continue
		}
		pr.ExpiresAt = epochStrToRFC3339(pr.ExpiresAt)
		if orgID.Valid {
			n := int(orgID.Int64)
			pr.OrgID = &n
		}
		regs = append(regs, pr)
	}
	if regs == nil {
		regs = []PendingRegistration{}
	}
	json.NewEncoder(w).Encode(regs)
}

// POST /api/v1/pending-registrations/{id}/approve
func approveRegHandler(w http.ResponseWriter, r *http.Request) {
	callerRole := r.Header.Get("X-User-Role")
	callerID, _ := intHeader(r, "X-User-ID")

	id, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var pr struct {
		ID                  int
		Username, Email, PasswordHash string
		RegType             string
		OrgID               sql.NullInt64
		OrgName, OrgDescription, OrgWebsite, OrgContactEmail string
		VerificationChannel, Telegram, TelegramChatID string
		Verified            int
	}
	err = db.QueryRow(
		`SELECT id, username, email, password_hash, reg_type, org_id, org_name, org_description,
		 org_website, org_contact_email, verification_channel, telegram, telegram_chat_id, verified
		 FROM pending_registrations WHERE id=?`, id,
	).Scan(
		&pr.ID, &pr.Username, &pr.Email, &pr.PasswordHash, &pr.RegType,
		&pr.OrgID, &pr.OrgName, &pr.OrgDescription, &pr.OrgWebsite, &pr.OrgContactEmail,
		&pr.VerificationChannel, &pr.Telegram, &pr.TelegramChatID, &pr.Verified,
	)
	if err != nil {
		writeError(w, "pending registration not found", http.StatusNotFound)
		return
	}

	// Only admins can approve new_org; users can approve join_org for their orgs.
	if pr.RegType == "new_org" && callerRole != RoleAdmin {
		writeError(w, "only admins can approve new_org registrations", http.StatusForbidden)
		return
	}
	if pr.RegType == "join_org" && callerRole != RoleAdmin {
		if !pr.OrgID.Valid || !isOrgMember(callerID, int(pr.OrgID.Int64)) {
			writeError(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	var roleReq struct {
		Role string `json:"role"`
	}
	json.NewDecoder(r.Body).Decode(&roleReq)
	role := roleReq.Role
	if role != RoleUser && role != RolePublisher {
		role = RoleUser
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Insert user.
	result, err := tx.Exec(
		"INSERT INTO users (username, email, password_hash, role, telegram, email_verified) VALUES (?, ?, ?, ?, ?, 1)",
		pr.Username, pr.Email, pr.PasswordHash, role, pr.Telegram,
	)
	if err != nil {
		writeError(w, "username or email already exists", http.StatusConflict)
		return
	}
	userID, _ := result.LastInsertId()

	var orgID int64
	if pr.RegType == "new_org" {
		// Create organization.
		if err := tx.QueryRow(
			"INSERT INTO organizations (name, description, website, contact_email) VALUES (?,?,?,?) RETURNING id",
			pr.OrgName, pr.OrgDescription, pr.OrgWebsite, pr.OrgContactEmail,
		).Scan(&orgID); err != nil {
			writeError(w, "failed to create organization", http.StatusInternalServerError)
			return
		}
	} else if pr.OrgID.Valid {
		orgID = pr.OrgID.Int64
	}

	if orgID != 0 {
		tx.Exec("INSERT OR IGNORE INTO organization_members (organization_id, user_id) VALUES (?,?)", orgID, userID)
	}

	tx.Exec("DELETE FROM pending_registrations WHERE id=?", id)

	if err := tx.Commit(); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	log.Printf("register: approved pending registration %d — new user %q (role=%s)", id, pr.Username, role)

	// Send welcome notification.
	go func() {
		msg := fmt.Sprintf("Your registration has been approved! You can now log in as %q.", pr.Username)
		if pr.TelegramChatID != "" {
			if err := sendTelegramMessage(pr.TelegramChatID, msg); err != nil {
				log.Printf("register: welcome telegram: %v", err)
			}
		} else if pr.Email != "" && config.SMTP.Host != "" {
			if err := SendEmail(pr.Email, "Your registration was approved", msg); err != nil {
				log.Printf("register: welcome email: %v", err)
			}
		}
	}()

	user := User{
		ID:       int(userID),
		Username: pr.Username,
		Email:    pr.Email,
		Role:     role,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(user)
}

// DELETE /api/v1/pending-registrations/{id}
func rejectRegHandler(w http.ResponseWriter, r *http.Request) {
	callerRole := r.Header.Get("X-User-Role")
	callerID, _ := intHeader(r, "X-User-ID")

	id, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var pr struct {
		RegType, Email, TelegramChatID string
		OrgID sql.NullInt64
	}
	err = db.QueryRow(
		"SELECT reg_type, email, COALESCE(telegram_chat_id,''), org_id FROM pending_registrations WHERE id=?", id,
	).Scan(&pr.RegType, &pr.Email, &pr.TelegramChatID, &pr.OrgID)
	if err != nil {
		writeError(w, "pending registration not found", http.StatusNotFound)
		return
	}

	if pr.RegType == "join_org" && callerRole != RoleAdmin {
		if !pr.OrgID.Valid || !isOrgMember(callerID, int(pr.OrgID.Int64)) {
			writeError(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if callerRole != RoleAdmin {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	db.Exec("DELETE FROM pending_registrations WHERE id=?", id)
	log.Printf("register: rejected pending registration %d", id)

	// Notify user.
	go func() {
		msg := "Your registration request was not approved."
		if pr.TelegramChatID != "" {
			sendTelegramMessage(pr.TelegramChatID, msg)
		} else if pr.Email != "" && config.SMTP.Host != "" {
			SendEmail(pr.Email, "Registration not approved", msg)
		}
	}()

	w.WriteHeader(http.StatusNoContent)
}

// notifyApprovers sends admin/org notification when a registration is verified.
func notifyApprovers(pendingID int) {
	var regType, username, orgName string
	var orgID sql.NullInt64
	err := db.QueryRow(
		"SELECT reg_type, username, org_name, org_id FROM pending_registrations WHERE id=?", pendingID,
	).Scan(&regType, &username, &orgName, &orgID)
	if err != nil {
		return
	}

	msg := fmt.Sprintf("New registration request from %q", username)
	if regType == "new_org" {
		msg += fmt.Sprintf(" (new organisation: %q)", orgName)
	} else if orgID.Valid {
		var oName string
		db.QueryRow("SELECT name FROM organizations WHERE id=?", orgID.Int64).Scan(&oName)
		msg += fmt.Sprintf(" (join org: %q)", oName)
	}
	msg += " — review in the admin panel."

	// Always notify admins.
	rows, err := db.Query(
		"SELECT COALESCE(email,''), COALESCE(telegram_chat_id,'') FROM users WHERE role='admin'",
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var email, chatID string
			rows.Scan(&email, &chatID)
			if chatID != "" {
				sendTelegramMessage(chatID, msg)
			} else if email != "" && config.SMTP.Host != "" {
				SendEmail(email, "New registration request", msg)
			}
		}
	}

	// For join_org, also notify org members.
	if regType == "join_org" && orgID.Valid {
		orgRows, err := db.Query(
			`SELECT COALESCE(u.email,''), COALESCE(u.telegram_chat_id,'')
			 FROM users u JOIN organization_members om ON om.user_id=u.id
			 WHERE om.organization_id=? AND u.role != 'admin'`,
			orgID.Int64,
		)
		if err == nil {
			defer orgRows.Close()
			for orgRows.Next() {
				var email, chatID string
				orgRows.Scan(&email, &chatID)
				if chatID != "" {
					sendTelegramMessage(chatID, msg)
				} else if email != "" && config.SMTP.Host != "" {
					SendEmail(email, "New registration request for your organisation", msg)
				}
			}
		}
	}
}

func intHeader(r *http.Request, key string) (int, error) {
	v := r.Header.Get(key)
	if v == "" {
		return 0, fmt.Errorf("missing header %s", key)
	}
	var n int
	_, err := fmt.Sscan(v, &n)
	return n, err
}

func intPathValue(r *http.Request, key string) (int, error) {
	v := r.PathValue(key)
	if v == "" {
		return 0, fmt.Errorf("missing path value %s", key)
	}
	var n int
	_, err := fmt.Sscan(v, &n)
	return n, err
}
