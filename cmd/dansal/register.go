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

func smtpEnabled() bool {
	return config.SMTP.Host != "" || config.SMTP.Sendmail != ""
}

func selfRegEnabled() bool {
	return smtpEnabled() || config.Server.TelegramBotToken != ""
}

func randomHex32() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type RegisterRequest struct {
	Email           string `json:"email"`
	Description     string `json:"description"` // motivation shown to admin; not stored on user account
	RegType         string `json:"reg_type"`    // "join_org" | "new_org"
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
	Email               string `json:"email"`
	Description         string `json:"description,omitempty"`
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

	if req.Email != "" {
		if !isValidEmail(req.Email) {
			writeError(w, "invalid email address", http.StatusUnprocessableEntity)
			return
		}
		if err := validateEmailDomain(r.Context(), req.Email); err != nil {
			writeError(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
	}
	// At least one contact channel or a non-empty motivation is required, but
	// we allow contact-free registration — the form warns the user.
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
		if smtpEnabled() {
			channel = "email"
		} else {
			channel = "telegram"
		}
	}
	if channel == "email" && !smtpEnabled() {
		writeError(w, "email channel not available", http.StatusBadRequest)
		return
	}
	if channel == "telegram" && config.Server.TelegramBotToken == "" {
		writeError(w, "telegram channel not available", http.StatusBadRequest)
		return
	}

	// Check uniqueness in users table.
	var c int
	db.QueryRow("SELECT COUNT(*) FROM users WHERE email=?", req.Email).Scan(&c)
	if c > 0 {
		writeError(w, "Email already registered", http.StatusConflict)
		return
	}
	// Check uniqueness in pending_registrations.
	db.QueryRow("SELECT COUNT(*) FROM pending_registrations WHERE email=?", req.Email).Scan(&c)
	if c > 0 {
		writeError(w, "A registration with this email is already pending", http.StatusConflict)
		return
	}

	// Per-address open-token limit: prevent spam floods targeting a single address.
	limit := config.Server.MaxOpenTokensPerAddress
	var openCount int
	db.QueryRow(
		"SELECT COUNT(*) FROM pending_registrations WHERE LOWER(email)=LOWER(?) AND verified=0 AND expires_at>strftime('%s','now')",
		req.Email,
	).Scan(&openCount)
	if openCount >= limit {
		writeError(w, "Too many pending verifications for this address. Please complete or expire existing ones first.", http.StatusTooManyRequests)
		return
	}
	if req.Telegram != "" {
		var openTg int
		db.QueryRow(
			"SELECT COUNT(*) FROM pending_registrations WHERE telegram=? AND verified=0 AND expires_at>strftime('%s','now')",
			req.Telegram,
		).Scan(&openTg)
		if openTg >= limit {
			writeError(w, "Too many pending verifications for this address. Please complete or expire existing ones first.", http.StatusTooManyRequests)
			return
		}
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

	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour).Unix()

	var orgIDArg any
	if req.OrgID != nil {
		orgIDArg = *req.OrgID
	}

	var pendingID int64
	dbErr := db.QueryRow(
		`INSERT INTO pending_registrations
		 (verification_token, approval_token, email, description,
		  reg_type, org_id, org_name, org_description, org_website, org_contact_email,
		  verification_channel, telegram, expires_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		verificationToken, approvalToken, req.Email, req.Description,
		req.RegType, orgIDArg, req.OrgName, req.OrgDescription, req.OrgWebsite, req.OrgContactEmail,
		channel, req.Telegram, expiresAt,
	).Scan(&pendingID)
	if dbErr != nil {
		writeError(w, "db error: "+dbErr.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)

	if channel == "email" {
		base := buildBaseURL(r)
		verifyURL := base + "/register/verify/email/" + verificationToken
		go func() {
			msg := fmt.Sprintf(
				"You requested an account on this event calendar. Please confirm your email address:\n\n%s\n\nThis link expires in 72 hours. If you did not request this, you can ignore this email.",
				verifyURL,
			)
			if msgID, err := SendEmail(req.Email, "Confirm your registration", msg); err != nil {
				log.Printf("register: send verify email: %v", err)
			} else {
				db.Exec("UPDATE pending_registrations SET message_id=? WHERE verification_token=?", msgID, verificationToken)
			}
		}()
		json.NewEncoder(w).Encode(map[string]any{
			"status":             "verification_email_sent",
			"pending_id":         pendingID,
			"verification_token": verificationToken,
		})
	} else {
		// Return token for Telegram bot instructions.
		json.NewEncoder(w).Encode(map[string]any{
			"status":             "telegram_verification_required",
			"pending_id":         pendingID,
			"verification_token": verificationToken,
			"telegram_token":     verificationToken,
			"bot_name":           config.Server.TelegramBotName,
		})
	}
}

// GET /api/v1/register/status/{id} — return the status of a pending registration for cookie-based resumption.
// Public endpoint; returns minimal info (verified/expired) without leaking contact details.
func registerStatusHandler(w http.ResponseWriter, r *http.Request) {
	id, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var verified int
	var expiresAt string
	err = db.QueryRow(
		"SELECT verified, expires_at FROM pending_registrations WHERE id=?", id,
	).Scan(&verified, &expiresAt)
	if err == sql.ErrNoRows {
		writeError(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	exp, err := parseTokenExpiration(expiresAt)
	expired := err != nil || time.Now().After(exp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":       id,
		"verified": verified == 1,
		"expired":  expired,
	})
}

// POST /api/v1/register/resend/{token} — resend verification message (rate-limited).
var resendRateLimiter *RateLimiter

func initResendRateLimiter() {
	resendRateLimiter = NewRateLimiter(3, time.Hour)
}

func registerResendHandler(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	ip := getIP(r)
	if !resendRateLimiter.Allow(ip) {
		writeError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var id int
	var email, telegram, channel, expiresAt string
	var verified int
	err := db.QueryRow(
		"SELECT id, COALESCE(email,''), COALESCE(telegram,''), verification_channel, expires_at, verified FROM pending_registrations WHERE verification_token=?",
		token,
	).Scan(&id, &email, &telegram, &channel, &expiresAt, &verified)
	if err == sql.ErrNoRows {
		writeError(w, "registration not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	if verified == 1 {
		writeError(w, "already verified", http.StatusConflict)
		return
	}
	exp, err := parseTokenExpiration(expiresAt)
	if err != nil || time.Now().After(exp) {
		db.Exec("DELETE FROM pending_registrations WHERE id=?", id)
		writeError(w, "registration expired", http.StatusGone)
		return
	}

	if channel == "email" && email != "" {
		base := buildBaseURL(r)
		verifyURL := base + "/register/verify/email/" + token
		go func() {
			msg := fmt.Sprintf(
				"You requested an account on this event calendar. Please confirm your email address:\n\n%s\n\nThis link expires in 72 hours. If you did not request this, you can ignore this email.",
				verifyURL,
			)
			if _, err := SendEmail(email, "Confirm your registration", msg); err != nil {
				log.Printf("register resend: send verify email: %v", err)
			}
		}()
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/register/{token} — self-service cancellation of a pending registration.
func registerCancelHandler(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	var id int
	var verified int
	err := db.QueryRow(
		"SELECT id, verified FROM pending_registrations WHERE verification_token=?", token,
	).Scan(&id, &verified)
	if err == sql.ErrNoRows {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	// Only allow self-cancellation before verification or admin approval.
	if verified == 1 {
		// Already verified and in the approval queue — don't allow silent self-cancellation.
		writeError(w, "registration is already verified; contact an admin to withdraw it", http.StatusConflict)
		return
	}
	db.Exec("DELETE FROM pending_registrations WHERE id=?", id)
	w.WriteHeader(http.StatusNoContent)
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
	callerID, callerRole := callerFromRequest(r)

	var rows interface{ Next() bool; Scan(...any) error; Close() error }
	var err error
	if callerRole == RoleAdmin {
		rows, err = db.Query(
			`SELECT id, email, COALESCE(description,''), reg_type, org_id, org_name, org_description, org_website,
			 org_contact_email, verification_channel, telegram, COALESCE(telegram_chat_id,''), verified, created_at, expires_at
			 FROM pending_registrations ORDER BY created_at ASC`,
		)
	} else {
		rows, err = db.Query(
			`SELECT pr.id, pr.email, COALESCE(pr.description,''), pr.reg_type, pr.org_id, pr.org_name, pr.org_description,
			 pr.org_website, pr.org_contact_email, pr.verification_channel, pr.telegram,
			 COALESCE(pr.telegram_chat_id,''), pr.verified, pr.created_at, pr.expires_at
			 FROM pending_registrations pr
			 JOIN organization_members om ON om.organization_id = pr.org_id AND om.user_id = ?
			 WHERE pr.reg_type='join_org' ORDER BY pr.created_at ASC`,
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
			&pr.ID, &pr.Email, &pr.Description, &pr.RegType, &orgID,
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
	callerID, callerRole := callerFromRequest(r)

	id, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var pr struct {
		ID                  int
		Email               string
		RegType             string
		OrgID               sql.NullInt64
		OrgName, OrgDescription, OrgWebsite, OrgContactEmail string
		VerificationChannel, Telegram, TelegramChatID string
		Verified            int
	}
	err = db.QueryRow(
		`SELECT id, email, reg_type, org_id, org_name, org_description,
		 org_website, org_contact_email, verification_channel, telegram, COALESCE(telegram_chat_id,''), verified
		 FROM pending_registrations WHERE id=?`, id,
	).Scan(
		&pr.ID, &pr.Email, &pr.RegType,
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

	// For new_org registrations: create the organisation now so it exists when
	// the user completes the invite. For join_org the org already exists.
	var orgID int64
	if pr.RegType == "new_org" {
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

	// Generate a 72-hour invite link pre-seeded with the approved username/email.
	inviteToken, err := generateInviteToken()
	if err != nil {
		writeError(w, "failed to generate invite token", http.StatusInternalServerError)
		return
	}
	expiresAt := time.Now().UTC().Add(72 * time.Hour).Unix()
	var orgVal any
	if orgID != 0 {
		orgVal = orgID
	}
	if _, err := tx.Exec(
		`INSERT INTO invite_links (token, created_by, role, org_id, expires_at, preset_email)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		inviteToken, callerID, role, orgVal, expiresAt, pr.Email,
	); err != nil {
		writeError(w, "failed to create invite link", http.StatusInternalServerError)
		return
	}

	tx.Exec("DELETE FROM pending_registrations WHERE id=?", id)

	if err := tx.Commit(); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	log.Printf("register: approved pending registration %d — invite sent to %q (role=%s)", id, pr.Email, role)

	base := buildBaseURL(r)
	setupURL := base + "/invites/" + inviteToken
	go notifyUser(pr.TelegramChatID, pr.Email, "Your registration was approved",
		fmt.Sprintf("Your registration has been approved.\n\nUse the link below to complete your account setup. The setup page will guide you through choosing how you want to sign in.\n\n%s\n\nThe link is valid for 72 hours.", setupURL))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "invite_sent",
		"email":      pr.Email,
		"invite_url": setupURL,
	})
}

// DELETE /api/v1/pending-registrations/{id}
func rejectRegHandler(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	id, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var pr struct {
		RegType, Email, TelegramChatID string
		Verified                       int
		OrgID                          sql.NullInt64
	}
	err = db.QueryRow(
		"SELECT reg_type, COALESCE(email,''), COALESCE(telegram_chat_id,''), verified, org_id FROM pending_registrations WHERE id=?", id,
	).Scan(&pr.RegType, &pr.Email, &pr.TelegramChatID, &pr.Verified, &pr.OrgID)
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

	var body struct {
		Reason string `json:"reason"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	if pr.Verified == 1 && body.Reason == "" {
		writeError(w, "reason is required when rejecting a verified registration", http.StatusBadRequest)
		return
	}

	db.Exec("DELETE FROM pending_registrations WHERE id=?", id)
	log.Printf("register: rejected pending registration %d", id)

	if pr.Verified == 1 {
		rejectMsg := "Your registration request was not approved."
		if body.Reason != "" {
			rejectMsg += "\n\nReason: " + body.Reason
		}
		go notifyUser(pr.TelegramChatID, pr.Email, "Registration not approved", rejectMsg)
	}

	w.WriteHeader(http.StatusNoContent)
}

// notifyApprovers sends admin/org notification when a registration is verified.
func notifyApprovers(pendingID int) {
	var regType, orgName, email string
	var orgID sql.NullInt64
	err := db.QueryRow(
		"SELECT reg_type, COALESCE(org_name,''), COALESCE(email,''), org_id FROM pending_registrations WHERE id=?", pendingID,
	).Scan(&regType, &orgName, &email, &orgID)
	if err != nil {
		return
	}

	identifier := email
	if identifier == "" {
		identifier = "(no contact info)"
	}
	msg := fmt.Sprintf("New registration request from %q", identifier)
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
			notifyUser(chatID, email, "New registration request", msg)
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
				notifyUser(chatID, email, "New registration request for your organisation", msg)
			}
		}
	}
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

// GET /api/v1/pending-registrations/count — scoped count of verified, unactioned registrations.
func pendingRegCountHandler(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	var count int
	if callerRole == RoleAdmin {
		db.QueryRow(
			"SELECT COUNT(*) FROM pending_registrations WHERE verified=1 AND expires_at > strftime('%s','now')",
		).Scan(&count)
	} else {
		db.QueryRow(`
			SELECT COUNT(*) FROM pending_registrations pr
			JOIN organization_members om ON om.organization_id = pr.org_id AND om.user_id = ?
			WHERE pr.reg_type='join_org' AND pr.verified=1 AND pr.expires_at > strftime('%s','now')`,
			callerID,
		).Scan(&count)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"count": count})
}

// startAutoDeclineJob runs an hourly sweep that deletes expired pending registrations
// and notifies verified ones that their request was not approved in time.
func startAutoDeclineJob() {
	go func() {
		t := time.NewTicker(time.Hour)
		for range t.C {
			processExpiredRegistrations()
		}
	}()
}

func processExpiredRegistrations() {
	rows, err := db.Query(
		"SELECT id, COALESCE(email,''), COALESCE(telegram_chat_id,''), verified FROM pending_registrations WHERE expires_at < strftime('%s','now')",
	)
	if err != nil {
		return
	}
	type expReg struct {
		id, verified     int
		email, telegramChatID string
	}
	var expired []expReg
	for rows.Next() {
		var e expReg
		rows.Scan(&e.id, &e.email, &e.telegramChatID, &e.verified)
		expired = append(expired, e)
	}
	rows.Close()

	for _, e := range expired {
		if e.verified == 1 {
			go notifyUser(e.telegramChatID, e.email,
				"Registration not approved",
				"Your registration request was not approved within the review period. Your contact information has been deleted.",
			)
		}
		db.Exec("DELETE FROM pending_registrations WHERE id=?", e.id)
	}
}
