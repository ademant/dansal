package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"strconv"
)

// callerFromRequest extracts the authenticated caller's ID and role from the
// request headers set by TokenMiddleware. Returns (0, "") when the headers are
// absent or unparseable.
func callerFromRequest(r *http.Request) (id int, role string) {
	id, _ = strconv.Atoi(r.Header.Get("X-User-ID"))
	role = r.Header.Get("X-User-Role")
	return
}

// notifyUser sends msg to the user via Telegram when chatID is set, or by
// email when an SMTP host is configured. Errors are logged but do not
// propagate — callers that need async delivery must wrap in a goroutine.
func notifyUser(chatID, email, subject, body string) {
	if chatID != "" {
		if err := sendTelegramMessage(chatID, body); err != nil {
			log.Printf("notify: telegram to %s: %v", chatID, err)
		}
	} else if email != "" && (config.SMTP.Host != "" || config.SMTP.Sendmail != "") {
		if _, err := SendEmail(email, subject, body, true); err != nil {
			log.Printf("notify: email to %s: %v", email, err)
		}
	}
}

// userIDByEmail returns the database ID for the given email, or an error
// when no such user exists.
func userIDByEmail(email string) (int, error) {
	var id int
	if err := db.QueryRow("SELECT id FROM users WHERE email=?", email).Scan(&id); err != nil {
		return 0, fmt.Errorf("user not found: %s", email)
	}
	return id, nil
}

// generateToken returns a cryptographically random n-byte value encoded as
// base64 URL-safe string (no padding).
func generateToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
