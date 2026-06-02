package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
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
	Fingerprint string `json:"fingerprint,omitempty"`
}

type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	User      User   `json:"user"`
}

type TokenError struct {
	Error string `json:"error"`
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

func generateSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// createTokenInDB stores the token with session metadata in the database.
func createTokenInDB(userID int, userAgent, ip, fingerprint string) (string, time.Time, error) {
	token, err := generateSessionToken()
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
		userID, token, expiresAt.Unix(), userAgent, ip, fpVal,
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

// validateToken checks if a token is valid and not expired.
// Results are cached for up to credCacheTTL to avoid a DB round-trip per request.
// Returns userID, role, tokenID (DB row id of the token).
func validateToken(token string) (int, string, int, error) {
	if userID, role, tokenID, ok := credentials.get(token); ok {
		return userID, role, tokenID, nil
	}

	var userID, tokenID int
	var userRole, expiresAt string

	err := db.QueryRow(
		"SELECT users.id, users.role, tokens.expires_at, tokens.id FROM tokens JOIN users ON tokens.user_id = users.id WHERE tokens.token = ? AND users.disabled = 0",
		token,
	).Scan(&userID, &userRole, &expiresAt, &tokenID)

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

	credentials.set(token, userID, userRole, tokenID, expTime)
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
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(TokenError{Error: "Invalid form data"})
			return
		}
		req.Email = r.FormValue("email")
		if req.Email == "" {
			req.Email = r.FormValue("username") // legacy webmin compat
		}
		req.Password = r.FormValue("password")
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			status := http.StatusBadRequest
			if errors.As(err, new(*http.MaxBytesError)) {
				status = http.StatusRequestEntityTooLarge
			}
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(TokenError{Error: "Invalid request body"})
			return
		}
	}

	// Accept email field; fall back to username field for backward compat.
	if req.Email == "" {
		req.Email = req.Username
	}

	// Validate input
	if req.Email == "" || req.Password == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(TokenError{Error: "Email and password are required"})
		return
	}

	clientIP := getClientIP(r)

	if !loginRateLimiter.Allow(clientIP) {
		log.Printf("auth failed from %s: login rate limit exceeded", clientIP)
		writeError(w, "Too many login attempts", http.StatusTooManyRequests)
		return
	}

	// Verify user credentials
	var user User
	var passwordHash string
	var userDisabled, failedCount int
	var failedSince string

	err := db.QueryRow(
		"SELECT id, COALESCE(email,''), COALESCE(display_name,''), role, created_at, password_hash, COALESCE(disabled,0), COALESCE(failed_login_count,0), COALESCE(failed_login_since,'') FROM users WHERE email = ?",
		req.Email,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.CreatedAt, &passwordHash, &userDisabled, &failedCount, &failedSince)

	if err == sql.ErrNoRows {
		// Fallback: try display_name (case-insensitive, only when exactly one user has it and has a password)
		rows, qerr := db.Query(
			"SELECT id, COALESCE(email,''), COALESCE(display_name,''), role, created_at, password_hash, COALESCE(disabled,0), COALESCE(failed_login_count,0), COALESCE(failed_login_since,'') FROM users WHERE display_name = ? COLLATE NOCASE AND password_hash IS NOT NULL AND password_hash != '' LIMIT 2",
			req.Email,
		)
		if qerr == nil {
			var count int
			for rows.Next() {
				count++
				if count == 1 {
					rows.Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.CreatedAt, &passwordHash, &userDisabled, &failedCount, &failedSince)
				}
			}
			rows.Close()
			if count == 1 {
				err = nil
			}
		}
	}

	if err == sql.ErrNoRows {
		log.Printf("auth failed from %s: invalid credentials", clientIP)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(TokenError{Error: "Invalid email or password"})
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(TokenError{Error: "Internal server error"})
		return
	}

	if userDisabled != 0 {
		log.Printf("auth failed from %s: user %q is disabled", clientIP, user.Email)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(TokenError{Error: "Invalid email or password"})
		return
	}

	// Reject empty password logins — user must use passkey or magic link.
	if passwordHash == "" {
		log.Printf("auth failed from %s: no password set for user %q", clientIP, user.Email)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(TokenError{Error: "No password set — use a passkey or magic link"})
		return
	}

	// Verify password; migrate legacy SHA-256 hashes to bcrypt on first successful login.
	ok, migrate := checkPassword(req.Password, passwordHash)
	if !ok {
		log.Printf("auth failed from %s: invalid credentials", clientIP)
		recordFailedLogin(user.ID, user.Email, clientIP, failedCount, failedSince)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(TokenError{Error: "Invalid email or password"})
		return
	}
	if migrate {
		db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", hashPassword(req.Password), user.ID)
	}
	db.Exec("UPDATE users SET failed_login_count=0, failed_login_since=NULL WHERE id=?", user.ID)

	// Generate token / session
	token, expiresAt, err := createTokenInDB(user.ID, r.UserAgent(), clientIP, req.Fingerprint)
	if err != nil {
		log.Printf("Error creating token: %v\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(TokenError{Error: "Failed to create token"})
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
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
		writeError(w, err.Error(), http.StatusInternalServerError)
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
		db.Exec("DELETE FROM tokens WHERE token = ?", token)
		credentials.invalidate(token)
	}
	w.WriteHeader(http.StatusNoContent)
}

// TokenMiddleware validates the token in the Authorization header
func TokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow OPTIONS requests to pass through for CORS preflight
		if r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(TokenError{Error: "Authorization header missing"})
			return
		}

		// Extract token from "Bearer <token>" format
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(TokenError{Error: "Invalid authorization header format. Use 'Bearer <token>'"})
			return
		}

		token := parts[1]

		// Validate token, fall back to API key
		userID, userRole, tokenID, err := validateToken(token)
		if err != nil {
			var apiErr error
			userID, userRole, apiErr = validateAPIKey(token)
			if apiErr != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(TokenError{Error: "Invalid or expired credentials"})
				return
			}
		} else {
			updateLastSeen(token)
			r.Header.Set("X-Session-ID", fmt.Sprintf("%d", tokenID))
		}

		// Store userID and role in request header for later use
		r.Header.Set("X-User-ID", fmt.Sprintf("%d", userID))
		r.Header.Set("X-User-Role", userRole)

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
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			next.ServeHTTP(w, r)
			return
		}
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(TokenError{Error: "Invalid authorization header format. Use 'Bearer <token>'"})
			return
		}
		token := parts[1]
		userID, userRole, tokenID, err := validateToken(token)
		if err != nil {
			var apiErr error
			userID, userRole, apiErr = validateAPIKey(token)
			if apiErr != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(TokenError{Error: "Invalid or expired credentials"})
				return
			}
		} else {
			updateLastSeen(token)
			r.Header.Set("X-Session-ID", fmt.Sprintf("%d", tokenID))
		}
		r.Header.Set("X-User-ID", fmt.Sprintf("%d", userID))
		r.Header.Set("X-User-Role", userRole)
		next.ServeHTTP(w, r)
	})
}
