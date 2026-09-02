package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
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

type RegisterRequest struct {
	Email           string `json:"email"`
	Description     string `json:"description"` // motivation shown to admin; not stored on user account
	RegType         string `json:"reg_type"`    // "join_org" | "new_org"
	OrgID           *int   `json:"org_id,omitempty"`
	OrgName         string `json:"org_name,omitempty"`
	OrgActorName    string `json:"org_actor_name,omitempty"`
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
	HasAuthMethod       bool   `json:"has_auth_method"`
	CreatedAt           string `json:"created_at"`
	ExpiresAt           string `json:"expires_at"`
}

// hasAuthMethodClause is true once a pending registration's linked
// placeholder user has a *working* credential -- a passkey bound, or a
// password set (#1223). Not just "user_id IS NOT NULL": webauthnRegBegin
// creates the placeholder before the WebAuthn ceremony finishes, so an
// abandoned passkey attempt must not count as "ready for admin review".
const hasAuthMethodClause = `pr.user_id IS NOT NULL AND (
	EXISTS(SELECT 1 FROM webauthn_credentials wc WHERE wc.user_id = pr.user_id)
	OR EXISTS(SELECT 1 FROM users u WHERE u.id = pr.user_id AND u.password_hash != '')
)`

// POST /api/v1/register — create a pending registration.
func registerHandler(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if !registerRateLimiter.Allow(ip) {
		writeError(w, "Rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	body, ok := readBodyOrError(w, r)
	if !ok {
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
		if looksLikeGmailDotSpam(req.Email) {
			writeError(w, "invalid email address", http.StatusUnprocessableEntity)
			return
		}
	}
	if req.Telegram != "" && !isValidTelegramContact(req.Telegram) {
		writeError(w, "invalid Telegram identifier: provide a username (@handle), a t.me link, or a phone number", http.StatusUnprocessableEntity)
		return
	}
	if len(req.Description) > 500 {
		writeError(w, "description must be 500 characters or fewer", http.StatusUnprocessableEntity)
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

	// Contact-free path (no email, no telegram): skip channel validation and mark
	// verified immediately so the registration enters the approval queue without
	// requiring an email link. The admin sees a "no contact" badge.
	contactFree := req.Email == "" && req.Telegram == ""

	// Board session shortcut (#1047): if the caller has a valid board session for
	// the same email address, skip email verification (treat as contact-free path
	// for channel purposes while preserving email for admin review).
	boardSessionVerified := false
	if !contactFree && req.Email != "" {
		if _, bsEmail, _, bsOk := lookupBoardSession(r); bsOk && strings.EqualFold(bsEmail, req.Email) {
			boardSessionVerified = true
		}
	}

	// Validate channel when contact info is provided.
	var channel string
	if !contactFree {
		channel = req.Channel
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
	} else {
		channel = "none"
	}

	// Uniqueness checks — only when email is non-empty to avoid matching all
	// email-less accounts when contact-free registration is used.
	var c int
	if req.Email != "" {
		db.QueryRow("SELECT COUNT(*) FROM users WHERE email=?", req.Email).Scan(&c)
		if c > 0 {
			writeError(w, "Email already registered", http.StatusConflict)
			return
		}
		db.QueryRow("SELECT COUNT(*) FROM pending_registrations WHERE email=?", req.Email).Scan(&c)
		if c > 0 {
			writeError(w, "A registration with this email is already pending", http.StatusConflict)
			return
		}
	}

	// Per-address open-token limit: prevent spam floods targeting a single address.
	limit := config.Server.MaxOpenTokensPerAddress
	if req.Email != "" {
		var openCount int
		db.QueryRow(
			"SELECT COUNT(*) FROM pending_registrations WHERE LOWER(email)=LOWER(?) AND verified=0 AND expires_at>strftime('%s','now')",
			req.Email,
		).Scan(&openCount)
		if openCount >= limit {
			writeError(w, "Too many pending verifications for this address. Please complete or expire existing ones first.", http.StatusTooManyRequests)
			return
		}
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

	verificationToken, err := generateToken(32)
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	approvalToken, err := generateToken(32)
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour).Unix()

	var orgIDArg any
	if req.OrgID != nil {
		orgIDArg = *req.OrgID
	}

	// Contact-free and board-session-verified registrations are marked verified
	// immediately — no email link needed, so they enter the approval queue right away.
	verifiedInitial := 0
	if contactFree || boardSessionVerified {
		verifiedInitial = 1
	}

	// Tokens are single-factor account-enabling credentials (whoever presents
	// one gets an invite) — hash at rest like magic_login_tokens, rather than
	// storing plaintext (#1014). The raw values are still what's emailed/
	// returned to the client; only the DB copy is hashed.
	var pendingID int64
	dbErr := db.QueryRow(
		`INSERT INTO pending_registrations
		 (verification_token, approval_token, email, description,
		  reg_type, org_id, org_name, org_actor_name, org_description, org_website, org_contact_email,
		  verification_channel, telegram, verified, expires_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		sha256Hex(verificationToken), sha256Hex(approvalToken), req.Email, req.Description,
		req.RegType, orgIDArg, req.OrgName, req.OrgActorName, req.OrgDescription, req.OrgWebsite, req.OrgContactEmail,
		channel, req.Telegram, verifiedInitial, expiresAt,
	).Scan(&pendingID)
	if dbErr != nil {
		log.Printf("register: db error: %v", dbErr)
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)

	if contactFree || boardSessionVerified {
		// Admin notification now fires once onboarding (choosing an auth
		// method) completes -- see webauthnRegFinish / registerPasswordHandler
		// (#1223) -- not here. There's nothing for an admin to act on yet:
		// the applicant hasn't even set up a way to sign in.
		json.NewEncoder(w).Encode(map[string]string{
			"status":             "pending_approval",
			"pending_id":         strconv.FormatInt(pendingID, 10),
			"verification_token": verificationToken,
		})
	} else if channel == "email" {
		base := buildBaseURL(r)
		verifyURL := base + "/register/verify/email/" + verificationToken
		go func() {
			msg := fmt.Sprintf(
				"You requested an account on this event calendar. Please confirm your email address:\n\n%s\n\nThis link expires in 72 hours. If you did not request this, you can ignore this email.",
				verifyURL,
			)
			if msgID, err := SendEmail(req.Email, "Confirm your registration", msg, false); err != nil {
				log.Printf("register: send verify email: %v", err)
			} else {
				db.Exec("UPDATE pending_registrations SET message_id=? WHERE verification_token=?", msgID, sha256Hex(verificationToken))
			}
		}()
		json.NewEncoder(w).Encode(map[string]string{
			"status":             "verification_email_sent",
			"pending_id":         strconv.FormatInt(pendingID, 10),
			"verification_token": verificationToken,
		})
	} else {
		json.NewEncoder(w).Encode(map[string]string{
			"status":             "telegram_verification_required",
			"pending_id":         strconv.FormatInt(pendingID, 10),
			"verification_token": verificationToken,
			"telegram_token":     verificationToken,
			"bot_name":           config.Server.TelegramBotName,
		})
	}
}

// GET /api/v1/register/status/{id} — return the status of a pending registration for cookie-based resumption.
// The verification_token query param is required for approved registrations to return the invite URL.
func registerStatusHandler(w http.ResponseWriter, r *http.Request) {
	id, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	token := r.URL.Query().Get("token")

	var verified, approved int
	var expiresAt, approvedInviteURL, storedToken string
	var userID sql.NullInt64
	err = db.QueryRow(
		"SELECT verified, approved, COALESCE(approved_invite_url,''), expires_at, verification_token, user_id FROM pending_registrations WHERE id=?", id,
	).Scan(&verified, &approved, &approvedInviteURL, &expiresAt, &storedToken, &userID)
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

	// Check if a passkey has been bound, or a password set, on this pending
	// registration's placeholder user (#1223) -- onboarding complete either way.
	hasPasskey := false
	hasAuthMethod := false
	if userID.Valid {
		var credCount int
		db.QueryRow("SELECT COUNT(*) FROM webauthn_credentials WHERE user_id=?", userID.Int64).Scan(&credCount)
		hasPasskey = credCount > 0
		var pwHash string
		db.QueryRow("SELECT COALESCE(password_hash,'') FROM users WHERE id=?", userID.Int64).Scan(&pwHash)
		hasAuthMethod = hasPasskey || pwHash != ""
	}

	resp := map[string]any{
		"id":              id,
		"verified":        verified == 1,
		"approved":        approved == 1,
		"expired":         expired,
		"has_passkey":     hasPasskey,
		"has_auth_method": hasAuthMethod,
	}
	// Only return invite URL when the caller proves ownership via the verification token
	// and the invite is still valid (not used, not expired). storedToken is the
	// sha256 hash at rest; compare constant-time against the hash of the
	// presented token (#1014).
	tokenMatches := token != "" && subtle.ConstantTimeCompare([]byte(sha256Hex(token)), []byte(storedToken)) == 1
	if approved == 1 && tokenMatches && approvedInviteURL != "" {
		inviteToken := inviteTokenFromURL(approvedInviteURL)
		if inviteToken != "" && isInviteUsable(inviteToken) {
			resp["invite_url"] = approvedInviteURL
		}
		// No invite_url in response when expired/used — the web page shows the approved
		// state without an actionable link, prompting the user to contact the admin.
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// POST /api/v1/register/resend/{token} — resend verification message (rate-limited).
var resendRateLimiter *RateLimiter

func initResendRateLimiter() {
	resendRateLimiter = NewRateLimiter(3, time.Hour)
}

func registerResendHandler(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	ip := getClientIP(r)
	if !resendRateLimiter.Allow(ip) {
		writeError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var id int
	var email, telegram, channel, expiresAt string
	var verified int
	err := db.QueryRow(
		"SELECT id, COALESCE(email,''), COALESCE(telegram,''), verification_channel, expires_at, verified FROM pending_registrations WHERE verification_token=?",
		sha256Hex(token),
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
			if _, err := SendEmail(email, "Confirm your registration", msg, false); err != nil {
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
		"SELECT id, verified FROM pending_registrations WHERE verification_token=?", sha256Hex(token),
	).Scan(&id, &verified)
	if err == sql.ErrNoRows {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	// Once verified or approved the record stays for admin visibility; just acknowledge.
	if verified == 1 {
		w.WriteHeader(http.StatusNoContent)
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
		sha256Hex(token),
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
	// Admin notification now fires once onboarding (choosing an auth method)
	// completes -- see webauthnRegFinish / registerPasswordHandler (#1223) --
	// not here. The web frontend routes the user straight into that
	// onboarding step on this response, using the id below.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"pending_id": strconv.Itoa(id)})
}

// POST /api/v1/register/password
// Body: {"pending_id":123,"verification_token":"...","display_name":"optional","password":"..."}
// Password-track counterpart to webauthnRegBegin/Finish (#1223): creates a
// disabled=1 placeholder user bound to the pending registration and sets its
// password directly (no ceremony needed, unlike WebAuthn), then notifies
// admins that the registration is ready for review. Account stays disabled
// until an admin approves the pending registration.
func registerPasswordHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PendingID         int    `json:"pending_id"`
		VerificationToken string `json:"verification_token"`
		DisplayName       string `json:"display_name"`
		Password          string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PendingID == 0 || req.VerificationToken == "" {
		writeError(w, "pending_id and verification_token are required", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		writeError(w, "Password must be at least 8 characters", http.StatusUnprocessableEntity)
		return
	}
	if isPasswordPwned(r.Context(), req.Password) {
		writeError(w, "This password has appeared in a data breach. Please choose a different password.", http.StatusUnprocessableEntity)
		return
	}

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
	if pr.UserID.Valid {
		writeError(w, "An auth method has already been set for this registration", http.StatusConflict)
		return
	}

	passwordHash, err := hashPassword(req.Password)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	var emailVal interface{}
	if pr.Email != "" {
		emailVal = pr.Email
	}
	result, err := db.Exec(
		"INSERT INTO users (email, display_name, password_hash, role, email_verified, disabled) VALUES (?, ?, ?, 'user', 0, 1)",
		emailVal, req.DisplayName, passwordHash,
	)
	if err != nil {
		writeError(w, "Could not create account", http.StatusInternalServerError)
		return
	}
	userID, _ := result.LastInsertId()
	db.Exec("UPDATE pending_registrations SET user_id=? WHERE id=?", userID, pr.ID)

	log.Printf("register: password set for pending registration %d (user_id=%d)", pr.ID, userID)
	go notifyApprovers(pr.ID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "password_set"})
}

// GET /api/v1/pending-registrations
func listPendingRegsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, callerRole := callerFromRequest(r)

	var rows interface {
		Next() bool
		Scan(...any) error
		Close() error
	}
	var err error
	if callerRole == RoleAdmin {
		rows, err = db.Query(
			`SELECT pr.id, pr.email, COALESCE(pr.description,''), pr.reg_type, pr.org_id,
			 COALESCE(NULLIF(pr.org_name,''), o.name, ''), pr.org_description, pr.org_website,
			 pr.org_contact_email, pr.verification_channel, pr.telegram, COALESCE(pr.telegram_chat_id,''),
			 pr.verified, (` + hasAuthMethodClause + `), pr.created_at, pr.expires_at
			 FROM pending_registrations pr
			 LEFT JOIN organizations o ON o.id = pr.org_id
			 WHERE pr.approved=0
			 ORDER BY pr.created_at ASC`,
		)
	} else {
		rows, err = db.Query(
			`SELECT pr.id, pr.email, COALESCE(pr.description,''), pr.reg_type, pr.org_id,
			 COALESCE(NULLIF(pr.org_name,''), o.name, ''), pr.org_description, pr.org_website,
			 pr.org_contact_email, pr.verification_channel, pr.telegram,
			 COALESCE(pr.telegram_chat_id,''), pr.verified, (`+hasAuthMethodClause+`), pr.created_at, pr.expires_at
			 FROM pending_registrations pr
			 LEFT JOIN organizations o ON o.id = pr.org_id
			 JOIN organization_members om ON om.organization_id = pr.org_id AND om.user_id = ?
			 WHERE pr.reg_type='join_org' AND pr.approved=0 ORDER BY pr.created_at ASC`,
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
			&pr.Verified, &pr.HasAuthMethod, &pr.CreatedAt, &pr.ExpiresAt,
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
		ID                                                                 int
		Email                                                              string
		RegType                                                            string
		OrgID                                                              sql.NullInt64
		UserID                                                             sql.NullInt64
		OrgName, OrgActorName, OrgDescription, OrgWebsite, OrgContactEmail string
		VerificationChannel, Telegram, TelegramChatID                      string
		Verified                                                           int
	}
	err = db.QueryRow(
		`SELECT id, COALESCE(email,''), reg_type, org_id, org_name, COALESCE(org_actor_name,''), org_description,
		 org_website, org_contact_email, verification_channel, COALESCE(telegram,''), COALESCE(telegram_chat_id,''), verified, user_id
		 FROM pending_registrations WHERE id=?`, id,
	).Scan(
		&pr.ID, &pr.Email, &pr.RegType,
		&pr.OrgID, &pr.OrgName, &pr.OrgActorName, &pr.OrgDescription, &pr.OrgWebsite, &pr.OrgContactEmail,
		&pr.VerificationChannel, &pr.Telegram, &pr.TelegramChatID, &pr.Verified, &pr.UserID,
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
		if !requireExistingOrgMember(w, callerID, pr.OrgID) {
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

	// Onboarding-already-complete path (#1223): the applicant already bound a
	// password or passkey during onboarding — just enable the account.
	if pr.UserID.Valid {
		userID := pr.UserID.Int64
		var hasPasskey bool
		db.QueryRow("SELECT COUNT(*) > 0 FROM webauthn_credentials WHERE user_id=?", userID).Scan(&hasPasskey)

		// For new_org: create the org and assign the user.
		if pr.RegType == "new_org" {
			var orgID int64
			if err := tx.QueryRow(
				"INSERT INTO organizations (name, actor_name, description, website, contact_email) VALUES (?,?,?,?,?) RETURNING id",
				pr.OrgName, pr.OrgActorName, pr.OrgDescription, pr.OrgWebsite, pr.OrgContactEmail,
			).Scan(&orgID); err != nil {
				writeError(w, "failed to create organization", http.StatusInternalServerError)
				return
			}
			tx.Exec("INSERT OR IGNORE INTO organization_members (organization_id, user_id) VALUES (?,?)", orgID, userID)
			tx.Exec("UPDATE users SET role=? WHERE id=?", role, userID)
		} else if pr.OrgID.Valid {
			tx.Exec("INSERT OR IGNORE INTO organization_members (organization_id, user_id) VALUES (?,?)", pr.OrgID.Int64, userID)
			tx.Exec("UPDATE users SET role=? WHERE id=?", role, userID)
		}

		tx.Exec("UPDATE users SET disabled=0 WHERE id=?", userID)
		tx.Exec("DELETE FROM pending_registrations WHERE id=?", id)

		if err := tx.Commit(); err != nil {
			writeError(w, "db error", http.StatusInternalServerError)
			return
		}

		log.Printf("register: approved pending registration %d — enabled user %d (role=%s, passkey=%v)", id, userID, role, hasPasskey)

		signInHint := "You can now sign in with the password you set."
		if hasPasskey {
			signInHint = "You can now sign in with the passkey you registered."
		}
		go notifyUser(pr.TelegramChatID, "", false, pr.Email, "Your registration was approved",
			"Your registration has been approved. "+signInHint)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "user_enabled",
			"user_id": userID,
		})
		return
	}

	// Fallback path (#1223): every new self-registration now binds an auth
	// method (password or passkey) during onboarding, right after email
	// verification, before it ever reaches this handler -- so pr.UserID.Valid
	// above is the normal case going forward. This branch remains only for
	// rows that reach approval without a bound user: pre-#1223 pending
	// registrations left over from before this change deployed, or an admin
	// approving from the list before the applicant finished onboarding. It
	// creates an invite link and emails a setup link, same as the old flow.
	var orgID int64
	if pr.RegType == "new_org" {
		if err := tx.QueryRow(
			"INSERT INTO organizations (name, actor_name, description, website, contact_email) VALUES (?,?,?,?,?) RETURNING id",
			pr.OrgName, pr.OrgActorName, pr.OrgDescription, pr.OrgWebsite, pr.OrgContactEmail,
		).Scan(&orgID); err != nil {
			writeError(w, "failed to create organization", http.StatusInternalServerError)
			return
		}
	} else if pr.OrgID.Valid {
		orgID = pr.OrgID.Int64
	}

	expiresAtTime := time.Now().UTC().Add(time.Duration(config.Server.InviteExpiryHours) * time.Hour)
	var orgVal any
	var orgIDPtr *int
	if orgID != 0 {
		orgVal = orgID
		id := int(orgID)
		orgIDPtr = &id
	}
	inviteToken, err := signInviteJWT(role, orgIDPtr, inviteTokenType(role), expiresAtTime)
	if err != nil {
		writeError(w, "failed to generate invite token", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO invite_links (token, created_by, role, org_id, expires_at, preset_email)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		inviteToken, callerID, role, orgVal, expiresAtTime.Unix(), pr.Email,
	); err != nil {
		writeError(w, "failed to create invite link", http.StatusInternalServerError)
		return
	}

	base := buildBaseURL(r)
	setupURL := base + "/invites/" + inviteToken

	contactFree := pr.Email == "" && pr.Telegram == ""
	if contactFree {
		// No contact info — keep the pending record but mark it approved so the user
		// can discover the invite URL by visiting /register with their cookie.
		tx.Exec("UPDATE pending_registrations SET approved=1, approved_invite_url=? WHERE id=?", setupURL, id)
	} else {
		tx.Exec("DELETE FROM pending_registrations WHERE id=?", id)
	}

	if err := tx.Commit(); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	log.Printf("register: approved pending registration %d — invite sent to %q (role=%s)", id, pr.Email, role)

	go notifyUser(pr.TelegramChatID, "", false, pr.Email, "Your registration was approved",
		fmt.Sprintf("Your registration has been approved.\n\nUse the link below to complete your account setup. The setup page will guide you through choosing how you want to sign in.\n\n%s\n\nThe link is valid for %d hours.", setupURL, config.Server.InviteExpiryHours))

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
		UserID                         sql.NullInt64
	}
	err = db.QueryRow(
		"SELECT reg_type, COALESCE(email,''), COALESCE(telegram_chat_id,''), verified, org_id, user_id FROM pending_registrations WHERE id=?", id,
	).Scan(&pr.RegType, &pr.Email, &pr.TelegramChatID, &pr.Verified, &pr.OrgID, &pr.UserID)
	if err != nil {
		writeError(w, "pending registration not found", http.StatusNotFound)
		return
	}

	if pr.RegType == "join_org" && callerRole != RoleAdmin {
		if !requireExistingOrgMember(w, callerID, pr.OrgID) {
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
	if pr.UserID.Valid {
		db.Exec("DELETE FROM users WHERE id=?", pr.UserID.Int64)
	}
	log.Printf("register: rejected pending registration %d", id)

	if pr.Verified == 1 {
		rejectMsg := "Your registration request was not approved."
		if body.Reason != "" {
			rejectMsg += "\n\nReason: " + body.Reason
		}
		rejectMsg += "\n\nAll submitted data has been deleted. You are welcome to register again at any time using the same email address."
		go notifyUser(pr.TelegramChatID, "", false, pr.Email, "Registration not approved", rejectMsg)
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
	// Always notify admins, each with their own one-time direct-login link.
	rows, err := db.Query(
		"SELECT id, COALESCE(email,''), COALESCE(telegram_chat_id,''), COALESCE(matrix,''), COALESCE(matrix_verified,0) FROM users WHERE role='admin'",
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var adminID int
			var email, chatID, matrixID string
			var matrixVerified bool
			rows.Scan(&adminID, &email, &chatID, &matrixID, &matrixVerified)
			notifyUser(chatID, matrixID, matrixVerified, email, "New registration request", msg+" — "+adminReviewLink(adminID, "/admin/registrations"))
		}
	}

	// For join_org, also notify org members.
	if regType == "join_org" && orgID.Valid {
		orgRows, err := db.Query(
			`SELECT COALESCE(u.email,''), COALESCE(u.telegram_chat_id,''), COALESCE(u.matrix,''), COALESCE(u.matrix_verified,0)
			 FROM users u JOIN organization_members om ON om.user_id=u.id
			 WHERE om.organization_id=? AND u.role != 'admin'`,
			orgID.Int64,
		)
		if err == nil {
			defer orgRows.Close()
			for orgRows.Next() {
				var email, chatID, matrixID string
				var matrixVerified bool
				orgRows.Scan(&email, &chatID, &matrixID, &matrixVerified)
				notifyUser(chatID, matrixID, matrixVerified, email, "New registration request for your organisation", msg)
			}
		}
	}
}

// GET /api/v1/pending-registrations/count — scoped count of verified, unactioned registrations.
func pendingRegCountHandler(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	var count int
	if callerRole == RoleAdmin {
		db.QueryRow(
			"SELECT COUNT(*) FROM pending_registrations pr WHERE pr.verified=1 AND pr.expires_at > strftime('%s','now') AND " + hasAuthMethodClause,
		).Scan(&count)
	} else {
		db.QueryRow(`
			SELECT COUNT(*) FROM pending_registrations pr
			JOIN organization_members om ON om.organization_id = pr.org_id AND om.user_id = ?
			WHERE pr.reg_type='join_org' AND pr.verified=1 AND pr.expires_at > strftime('%s','now') AND `+hasAuthMethodClause,
			callerID,
		).Scan(&count)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"count": count})
}

// GET /api/v1/dashboard/attention — scoped counts of items needing review.
// Admins get instance-wide counts; RoleUser gets counts scoped to their
// organization(s), including events at locations linked to their org via
// location_organizations (covers shared venues like a community house).
func dashboardAttentionHandler(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	var regCount, suggestionCount, duplicateCount, pendingEditCount, notVerifiedCount int
	if callerRole == RoleAdmin {
		db.QueryRow(
			"SELECT COUNT(*) FROM pending_registrations pr WHERE pr.verified=1 AND pr.expires_at > strftime('%s','now') AND " + hasAuthMethodClause,
		).Scan(&regCount)
		db.QueryRow(
			"SELECT COUNT(*) FROM events WHERE is_published=0 AND email_verified=1",
		).Scan(&suggestionCount)
		db.QueryRow(
			"SELECT COUNT(*) FROM events WHERE needs_duplicate_review=1",
		).Scan(&duplicateCount)
		db.QueryRow(
			"SELECT COUNT(*) FROM events WHERE pending_edit_json IS NOT NULL AND pending_edit_json != ''",
		).Scan(&pendingEditCount)
		db.QueryRow(
			"SELECT COUNT(*) FROM events WHERE email_verified=0",
		).Scan(&notVerifiedCount)
	} else {
		db.QueryRow(`
			SELECT COUNT(*) FROM pending_registrations pr
			JOIN organization_members om ON om.organization_id = pr.org_id AND om.user_id = ?
			WHERE pr.reg_type='join_org' AND pr.verified=1 AND pr.expires_at > strftime('%s','now') AND `+hasAuthMethodClause,
			callerID,
		).Scan(&regCount)

		const orgScopeClause = `AND (
			e.organization_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)
			OR e.location_id IN (
				SELECT location_id FROM location_organizations
				WHERE organization_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)
			)
		)`
		db.QueryRow(
			"SELECT COUNT(*) FROM events e WHERE e.is_published=0 AND e.email_verified=1 "+orgScopeClause,
			callerID, callerID,
		).Scan(&suggestionCount)
		db.QueryRow(
			"SELECT COUNT(*) FROM events e WHERE e.needs_duplicate_review=1 "+orgScopeClause,
			callerID, callerID,
		).Scan(&duplicateCount)
		db.QueryRow(
			"SELECT COUNT(*) FROM events e WHERE e.pending_edit_json IS NOT NULL AND e.pending_edit_json != '' "+orgScopeClause,
			callerID, callerID,
		).Scan(&pendingEditCount)
		db.QueryRow(
			"SELECT COUNT(*) FROM events e WHERE e.email_verified=0 "+orgScopeClause,
			callerID, callerID,
		).Scan(&notVerifiedCount)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{
		"pending_registrations":     regCount,
		"pending_event_suggestions": suggestionCount,
		"possible_duplicates":       duplicateCount,
		"pending_edits":             pendingEditCount,
		"not_verified_event_count":  notVerifiedCount,
	})
}

// startAutoDeclineJob runs an hourly sweep that deletes expired pending registrations
// and notifies verified ones that their request was not approved in time.
func startAutoDeclineJob() {
	go func() {
		t := time.NewTicker(time.Hour)
		for range t.C {
			processExpiredRegistrations()
			processExpiredInviteLinks()
		}
	}()
}

// processExpiredInviteLinks deletes invite_links that have expired without
// being used, so the admin "awaiting setup" list doesn't show stale rows
// indefinitely.
func processExpiredInviteLinks() {
	db.Exec("DELETE FROM invite_links WHERE used_at IS NULL AND expires_at < strftime('%s','now')")
}

func processExpiredRegistrations() {
	rows, err := db.Query(
		"SELECT id, COALESCE(email,''), COALESCE(telegram_chat_id,''), verified, user_id FROM pending_registrations WHERE expires_at < strftime('%s','now')",
	)
	if err != nil {
		return
	}
	type expReg struct {
		id, verified          int
		email, telegramChatID string
		userID                sql.NullInt64
	}
	var expired []expReg
	for rows.Next() {
		var e expReg
		rows.Scan(&e.id, &e.email, &e.telegramChatID, &e.verified, &e.userID)
		expired = append(expired, e)
	}
	rows.Close()

	for _, e := range expired {
		if e.verified == 1 {
			go notifyUser(e.telegramChatID, "", false, e.email,
				"Registration not approved",
				"Your registration request was not approved within the review period. Your contact information has been deleted.",
			)
		}
		db.Exec("DELETE FROM pending_registrations WHERE id=?", e.id)
		if e.userID.Valid {
			db.Exec("DELETE FROM users WHERE id=?", e.userID.Int64)
		}
	}
}

// inviteTokenFromURL extracts the invite token from a full URL like
// https://example.org/invites/TOKEN. Returns "" if the URL has no /invites/ segment.
func inviteTokenFromURL(rawURL string) string {
	const prefix = "/invites/"
	idx := len(rawURL) - 1
	for idx >= 0 && rawURL[idx] != '/' {
		idx--
	}
	if idx < 0 {
		return ""
	}
	seg := rawURL[:idx+1]
	if len(seg) >= len(prefix) && seg[len(seg)-len(prefix):] == prefix {
		return rawURL[idx+1:]
	}
	return ""
}

// isInviteUsable returns true when the invite link exists, has not been used,
// and has not expired.
func isInviteUsable(token string) bool {
	var usedAt, expiresAt string
	if err := db.QueryRow(
		"SELECT COALESCE(used_at,''), expires_at FROM invite_links WHERE token=?", token,
	).Scan(&usedAt, &expiresAt); err != nil {
		return false
	}
	if usedAt != "" {
		return false
	}
	exp, err := parseTokenExpiration(expiresAt)
	return err == nil && !time.Now().After(exp)
}
