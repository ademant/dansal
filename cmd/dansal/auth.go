package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Token struct {
	ID        int    `json:"id"`
	UserID    int    `json:"user_id"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
}

type LoginRequest struct {
	Email       string `json:"email"`
	Username    string `json:"username"` // legacy field — treated as email for backward compat
	Password    string `json:"password"`
	TotpCode    string `json:"totp_code,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	User      User   `json:"user"`
}

// recordFailedLogin increments the per-user failure counter and disables the
// account when the configured threshold is reached within the window.
func recordFailedLogin(userID int, email, clientIP string, storedCount int, failedSince string) {
	maxFailures := config.Server.LoginMaxFailures
	windowSecs := config.Server.LoginFailureWindowSecs

	now := time.Now().UTC()
	window := time.Duration(windowSecs) * time.Second

	var newCount int
	var newSince any // int64 epoch or nil
	if failedSince != "" {
		if since, err := parseTokenExpiration(failedSince); err == nil && now.Sub(since) < window {
			newCount = storedCount + 1
		} else {
			newCount = 1
			newSince = now.Unix()
		}
	} else {
		newCount = 1
		newSince = now.Unix()
	}

	if newCount >= maxFailures {
		if newSince != nil {
			db.Exec("UPDATE users SET disabled=1, failed_login_count=?, failed_login_since=? WHERE id=?", newCount, newSince, userID)
		} else {
			db.Exec("UPDATE users SET disabled=1, failed_login_count=? WHERE id=?", newCount, userID)
		}
		log.Printf("auth: user %q disabled after %d failed logins within window (last from %s)", email, newCount, clientIP)
		credentials.pruneByUserID(userID)
		db.Exec("DELETE FROM tokens WHERE user_id=?", userID)
	} else if newSince != nil {
		db.Exec("UPDATE users SET failed_login_count=1, failed_login_since=? WHERE id=?", newSince, userID)
	} else {
		db.Exec("UPDATE users SET failed_login_count=? WHERE id=?", newCount, userID)
	}
}

// authUserRow holds the DB columns needed to resolve and authenticate a
// login identifier — a superset of the public User type (adds
// PasswordHash/FailedLogin* bookkeeping that never belongs in an API
// response).
type authUserRow struct {
	ID               int
	Email            string
	DisplayName      string
	Role             string
	CreatedAt        string
	PasswordHash     string
	Disabled         int
	FailedLoginCount int
	FailedLoginSince string
}

// errAmbiguousIdentifier is returned by resolveLoginUser when a
// display_name fallback match is ambiguous (shared by 2+ accounts).
var errAmbiguousIdentifier = errors.New("multiple accounts share that identifier")

const authUserCols = "id, COALESCE(email,''), COALESCE(display_name,''), role, created_at, COALESCE(password_hash,''), COALESCE(disabled,0), COALESCE(failed_login_count,0), COALESCE(failed_login_since,'')"

func scanAuthUserRow(row interface{ Scan(...any) error }, u *authUserRow) error {
	return row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.CreatedAt, &u.PasswordHash, &u.Disabled, &u.FailedLoginCount, &u.FailedLoginSince)
}

// resolveLoginUser resolves a login identifier to a user row: first by exact
// email match, then falling back to a case-insensitive display_name match
// when exactly one account has it (rejecting the login as ambiguous
// otherwise). This is the shared implementation of the same
// security-sensitive resolution algorithm previously duplicated between
// password login (here) and WebAuthn login (webauthn.go), which had already
// drifted (#1013).
//
// requirePassword restricts the display_name fallback to accounts that have
// a password set — password login only wants candidates it could
// conceivably authenticate; WebAuthn login doesn't care and passes false.
//
// Returns sql.ErrNoRows if no account matches, errAmbiguousIdentifier if the
// display_name fallback matched more than one account, or another error on
// a DB failure.
func resolveLoginUser(identifier string, requirePassword bool) (authUserRow, error) {
	var u authUserRow
	err := scanAuthUserRow(db.QueryRow("SELECT "+authUserCols+" FROM users WHERE email = ?", identifier), &u)
	if err == nil {
		return u, nil
	}
	if err != sql.ErrNoRows {
		return authUserRow{}, err
	}

	query := "SELECT " + authUserCols + " FROM users WHERE display_name = ? COLLATE NOCASE"
	if requirePassword {
		query += " AND password_hash IS NOT NULL AND password_hash != ''"
	}
	query += " LIMIT 2"
	rows, qerr := db.Query(query, identifier)
	if qerr != nil {
		return authUserRow{}, sql.ErrNoRows
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		count++
		if count == 1 {
			if serr := scanAuthUserRow(rows, &u); serr != nil {
				return authUserRow{}, serr
			}
		}
	}
	switch {
	case count == 0:
		return authUserRow{}, sql.ErrNoRows
	case count > 1:
		return authUserRow{}, errAmbiguousIdentifier
	}
	return u, nil
}

// createTokenInDB stores the token with session metadata in the database.
func createTokenInDB(userID int, userAgent, ip, fingerprint string) (string, time.Time, error) {
	token, err := generateToken(32)
	if err != nil {
		return "", time.Time{}, err
	}

	expirationHours := 24
	if config != nil && config.Server.TokenExpirationHours > 0 {
		expirationHours = config.Server.TokenExpirationHours
	}
	expiresAt := time.Now().UTC().Add(time.Duration(expirationHours) * time.Hour)

	var fpVal any
	if fingerprint != "" {
		fpVal = fingerprint
	}
	_, err = db.Exec(
		"INSERT INTO tokens (user_id, token, expires_at, user_agent, ip, fingerprint) VALUES (?, ?, ?, ?, ?, ?)",
		userID, sha256Hex(token), expiresAt.Unix(), userAgent, ip, fpVal,
	)
	if err != nil {
		return "", time.Time{}, err
	}

	// Keep only the N most recent tokens per user; drop older ones.
	// N is the smaller of 5 and the configured concurrent session limit (when > 0).
	limit := 5
	if config != nil && config.Server.SessionMaxConcurrent > 0 && config.Server.SessionMaxConcurrent < limit {
		limit = config.Server.SessionMaxConcurrent
	}
	db.Exec(`DELETE FROM tokens WHERE user_id=? AND id NOT IN (
		SELECT id FROM tokens WHERE user_id=? ORDER BY created_at DESC LIMIT ?
	)`, userID, userID, limit)

	return token, expiresAt, nil
}

// createPinnedTokenInDB mints a short-lived session token bound to a specific
// IP address — used by publishers exchanging their API key for a token that's
// useless if exfiltrated to another network (see POST /api/v1/publishers/token).
// Any existing pinned tokens for the user are deleted first, so re-authenticating
// from a new IP immediately invalidates the old one.
func createPinnedTokenInDB(userID int, ip string) (string, time.Time, error) {
	token, err := generateToken(32)
	if err != nil {
		return "", time.Time{}, err
	}

	hours := 1
	if config != nil && config.Server.PublisherTokenExpirationHours > 0 {
		hours = config.Server.PublisherTokenExpirationHours
	}
	expiresAt := time.Now().UTC().Add(time.Duration(hours) * time.Hour)

	if _, err := db.Exec("DELETE FROM tokens WHERE user_id=? AND ip_pinned=1", userID); err != nil {
		return "", time.Time{}, err
	}
	if _, err := db.Exec(
		"INSERT INTO tokens (user_id, token, expires_at, ip, ip_pinned) VALUES (?, ?, ?, ?, 1)",
		userID, sha256Hex(token), expiresAt.Unix(), ip,
	); err != nil {
		return "", time.Time{}, err
	}

	return token, expiresAt, nil
}

// validateToken checks if a token is valid and not expired, and — for
// IP-pinned tokens — that currentIP matches the IP it was minted for.
// Results are cached for up to credCacheTTL to avoid a DB round-trip per request.
// Returns userID, role, tokenID (DB row id of the token).
func validateToken(token, currentIP string) (int, string, int, error) {
	if userID, role, tokenID, ok := credentials.get(token, currentIP); ok {
		return userID, role, tokenID, nil
	}

	var userID, tokenID int
	var userRole, expiresAt string
	var ipPinned bool
	var tokenIP sql.NullString

	err := db.QueryRow(
		"SELECT users.id, users.role, tokens.expires_at, tokens.id, tokens.ip_pinned, tokens.ip FROM tokens JOIN users ON tokens.user_id = users.id WHERE tokens.token = ? AND users.disabled = 0",
		sha256Hex(token),
	).Scan(&userID, &userRole, &expiresAt, &tokenID, &ipPinned, &tokenIP)

	if err == sql.ErrNoRows {
		return 0, "", 0, fmt.Errorf("invalid token")
	}
	if err != nil {
		return 0, "", 0, err
	}

	expTime, err := parseTokenExpiration(expiresAt)
	if err != nil {
		return 0, "", 0, fmt.Errorf("invalid token expiration format")
	}

	if time.Now().After(expTime) {
		return 0, "", 0, fmt.Errorf("token expired")
	}

	pinnedIP := ""
	if ipPinned {
		pinnedIP = tokenIP.String
		if pinnedIP == "" || pinnedIP != currentIP {
			return 0, "", 0, fmt.Errorf("invalid token")
		}
	}

	credentials.set(token, userID, userRole, tokenID, expTime, pinnedIP)
	return userID, userRole, tokenID, nil
}

func parseTokenExpiration(value string) (time.Time, error) {
	// New format: unix epoch integer stored as integer, scanned as string by SQLite driver.
	if epoch, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		return time.Unix(epoch, 0).UTC(), nil
	}
	// Legacy RFC3339 text formats kept for backward compatibility during migration.
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported expiration format")
}

// epochStrToRFC3339 converts a stored epoch integer (possibly scanned as string) to RFC3339.
// Falls back to returning the value unchanged if it is already formatted text or empty.
func epochStrToRFC3339(s string) string {
	if s == "" {
		return ""
	}
	if epoch, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
		return time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	}
	return s
}

// GET /api/v1/login - Login endpoint to get OAuth token
func login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req LoginRequest

	// Parse request body based on content type
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			status := http.StatusBadRequest
			if errors.As(err, new(*http.MaxBytesError)) {
				status = http.StatusRequestEntityTooLarge
			}
			writeError(w, "Invalid form data", status)
			return
		}
		req.Email = r.FormValue("email")
		if req.Email == "" {
			req.Email = r.FormValue("username") // legacy webmin compat
		}
		req.Password = r.FormValue("password")
	} else {
		if !decodeJSONBody(w, r, &req) {
			return
		}
	}

	// Accept email field; fall back to username field for backward compat.
	if req.Email == "" {
		req.Email = req.Username
	}

	// Validate input
	if req.Email == "" || req.Password == "" {
		writeError(w, "Email and password are required", http.StatusBadRequest)
		return
	}

	clientIP := getClientIP(r)

	if !loginRateLimiter.Allow(clientIP) {
		log.Printf("auth failed from %s: login rate limit exceeded", clientIP)
		writeError(w, "Too many login attempts", http.StatusTooManyRequests)
		return
	}

	// Verify user credentials
	authUser, err := resolveLoginUser(req.Email, true)
	if err == sql.ErrNoRows || err == errAmbiguousIdentifier {
		log.Printf("auth failed from %s: invalid credentials", clientIP)
		writeError(w, "Invalid email or password", http.StatusUnauthorized)
		return
	}
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	user := User{ID: authUser.ID, Email: authUser.Email, DisplayName: authUser.DisplayName, Role: authUser.Role, CreatedAt: authUser.CreatedAt}
	passwordHash := authUser.PasswordHash
	failedCount := authUser.FailedLoginCount
	failedSince := authUser.FailedLoginSince

	if authUser.Disabled != 0 {
		log.Printf("auth failed from %s: user %q is disabled", clientIP, user.Email)
		writeError(w, "Invalid email or password", http.StatusUnauthorized)
		return
	}

	// Reject empty password logins — user must use passkey or magic link.
	if passwordHash == "" {
		log.Printf("auth failed from %s: no password set for user %q", clientIP, user.Email)
		writeError(w, "No password set — use a passkey or magic link", http.StatusUnauthorized)
		return
	}

	// Verify password; migrate legacy SHA-256 hashes to bcrypt on first successful login.
	ok, migrate := checkPassword(req.Password, passwordHash)
	if !ok {
		log.Printf("auth failed from %s: invalid credentials", clientIP)
		recordFailedLogin(user.ID, user.Email, clientIP, failedCount, failedSince)
		writeError(w, "Invalid email or password", http.StatusUnauthorized)
		return
	}
	if migrate {
		if newHash, err := hashPassword(req.Password); err != nil {
			log.Printf("auth: hash migration for user %d skipped: %v", user.ID, err)
		} else {
			db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", newHash, user.ID)
		}
	}
	db.Exec("UPDATE users SET failed_login_count=0, failed_login_since=NULL WHERE id=?", user.ID)

	// Check TOTP when enabled.
	totpSecret := getUserTOTPSecret(user.ID)
	if totpSecret != "" {
		if req.TotpCode == "" {
			writeError(w, "totp_required", http.StatusUnauthorized)
			return
		}
		if !totpCheckAndMark(user.ID, totpSecret, req.TotpCode, time.Now()) {
			log.Printf("auth failed from %s: invalid or replayed TOTP code for %q", clientIP, user.Email)
			writeError(w, "Invalid TOTP code", http.StatusUnauthorized)
			return
		}
	}

	// Generate token / session
	token, expiresAt, err := createTokenInDB(user.ID, r.UserAgent(), clientIP, req.Fingerprint)
	if err != nil {
		log.Printf("Error creating token: %v\n", err)
		writeError(w, "Failed to create token", http.StatusInternalServerError)
		return
	}

	// Return token and user info
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(LoginResponse{
		Token:     token,
		ExpiresAt: expiresAt.Format(time.RFC3339),
		User:      user,
	})
}

// POST /api/v1/cert-login — create a session for a user authenticated via mTLS cert.
// Only accepts requests from loopback (127.0.0.1 / ::1); only issues tokens for admin users.
func certLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Restrict to loopback only
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Email == "" {
		writeError(w, "email is required", http.StatusBadRequest)
		return
	}

	var user User
	err := db.QueryRow(
		"SELECT id, email, COALESCE(display_name,''), role, created_at, COALESCE(disabled,0) FROM users WHERE email=?",
		req.Email,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.CreatedAt, new(int))
	if err == sql.ErrNoRows {
		writeError(w, "user not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if user.Role != RoleAdmin {
		writeError(w, "Forbidden: only admin users may authenticate via certificate", http.StatusForbidden)
		return
	}

	token, expiresAt, err := createTokenInDB(user.ID, r.UserAgent(), "cert", "")
	if err != nil {
		writeError(w, "failed to create session", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(LoginResponse{
		Token:     token,
		ExpiresAt: expiresAt.Format(time.RFC3339),
		User:      user,
	})
}

// DELETE /api/v1/login — revoke the current session token
func logout(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) == 2 && parts[0] == "Bearer" {
		token := parts[1]
		// tokens.token stores sha256Hex(token) (see createTokenInDB) — deleting
		// by the raw token never matched any row, so logout never actually
		// revoked the session server-side (#1004).
		db.Exec("DELETE FROM tokens WHERE token = ?", sha256Hex(token))
		credentials.invalidate(token)
		lastSeenMu.Lock()
		delete(lastSeenCache, token)
		lastSeenMu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveCaller validates the Authorization header (Bearer token or API
// key) and, on success, sets X-User-ID/X-User-Role/X-Session-ID on r for
// downstream handlers.
//
// ok=true means a valid credential was resolved and the request headers
// were set. ok=false has two cases, distinguished by noAuth:
//   - noAuth=true: no Authorization header was present. resolveCaller wrote
//     nothing; the caller decides how to proceed (reject or continue
//     unauthenticated).
//   - noAuth=false: the header was present but malformed/invalid/expired.
//     resolveCaller has already written a 401 error response.
func resolveCaller(w http.ResponseWriter, r *http.Request) (ok bool, noAuth bool) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return false, true
	}

	// Extract token from "Bearer <token>" format
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		writeError(w, "Invalid authorization header format. Use 'Bearer <token>'", http.StatusUnauthorized)
		return false, false
	}

	token := parts[1]

	// Validate token, fall back to API key
	userID, userRole, tokenID, err := validateToken(token, getClientIP(r))
	if err != nil {
		var apiErr error
		userID, userRole, apiErr = validateAPIKey(token)
		if apiErr != nil {
			writeError(w, "Invalid or expired credentials", http.StatusUnauthorized)
			return false, false
		}
	} else {
		updateLastSeen(token)
		r.Header.Set("X-Session-ID", fmt.Sprintf("%d", tokenID))
	}

	// Store userID and role in request header for later use
	r.Header.Set("X-User-ID", fmt.Sprintf("%d", userID))
	r.Header.Set("X-User-Role", userRole)
	return true, false
}

// TokenMiddleware validates the token in the Authorization header
func TokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow OPTIONS requests to pass through for CORS preflight
		if r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}

		ok, noAuth := resolveCaller(w, r)
		if !ok {
			if noAuth {
				writeError(w, "Authorization header missing", http.StatusUnauthorized)
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}

// OptionalTokenMiddleware validates the token when present but allows
// unauthenticated requests through with an empty X-User-Role header.
// Handlers use the empty role to restrict responses to published data only.
// An invalid/expired token is still rejected with 401.
func OptionalTokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}
		ok, noAuth := resolveCaller(w, r)
		if !ok && !noAuth {
			// resolveCaller already wrote a 401 response
			return
		}
		next.ServeHTTP(w, r)
	})
}
