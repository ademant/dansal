package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

type sessionContextKey int

const ctxSessionUser sessionContextKey = 1

const (
	cookieToken = "dsw_token"
	cookieUser  = "dsw_user"
)

// sessionHMACKey is generated once at startup and used to sign the dsw_user
// cookie so its fields (especially Role) can't be forged by a client.
var sessionHMACKey []byte

func init() {
	sessionHMACKey = make([]byte, 32)
	if _, err := rand.Read(sessionHMACKey); err != nil {
		log.Fatal("session: generate HMAC key: ", err)
	}
}

func sessionMAC(payload []byte) string {
	mac := hmac.New(sha256.New, sessionHMACKey)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

type SessionUser struct {
	ID          int    `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role"`
}

func withSessionUser(r *http.Request, u *SessionUser) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxSessionUser, u))
}

func getSessionUser(r *http.Request) *SessionUser {
	// Check request context first (set by cert-auth middleware)
	if u, ok := r.Context().Value(ctxSessionUser).(*SessionUser); ok && u != nil {
		return u
	}
	c, err := r.Cookie(cookieUser)
	if err != nil {
		return nil
	}
	payload, mac, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(sessionMAC([]byte(payload)))) {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	var u SessionUser
	if json.Unmarshal(decoded, &u) != nil {
		return nil
	}
	return &u
}

func getSessionToken(r *http.Request) string {
	c, err := r.Cookie(cookieToken)
	if err != nil {
		return ""
	}
	return c.Value
}

func setSession(w http.ResponseWriter, token string, user SessionUser, expiresAt time.Time) {
	userJSON, _ := json.Marshal(user)
	payload := base64.StdEncoding.EncodeToString(userJSON)
	userEncoded := payload + "." + sessionMAC([]byte(payload))
	http.SetCookie(w, &http.Cookie{
		Name:     cookieToken,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cookieUser,
		Value:    userEncoded,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSession(w http.ResponseWriter) {
	for _, name := range []string{cookieToken, cookieUser} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
			HttpOnly: true,
			Secure:   true,
		})
	}
}
