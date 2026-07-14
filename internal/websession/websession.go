// Package websession provides HMAC-signed session cookies shared by
// dansal-web and dansal-webmin. Each binary instantiates its own Cookies
// value with its own cookie names and signing key.
package websession

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Cookies holds the cookie names and signing key for a binary's session
// cookies. The token cookie carries an opaque session token (verified by
// the dansal API); the user cookie carries an HMAC-signed JSON blob
// describing the logged-in user so its fields (especially Role) can't be
// forged by a client.
type Cookies struct {
	Token string // cookie name for the session token
	User  string // cookie name for the encoded user JSON
	key   []byte
}

// New returns a Cookies value with a freshly generated signing key. Each
// binary (and each process restart) gets its own key — cookies don't need
// to survive restarts beyond a normal re-login.
func New(tokenCookie, userCookie string) Cookies {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("websession: generate signing key: " + err.Error())
	}
	return Cookies{Token: tokenCookie, User: userCookie, key: key}
}

func (c Cookies) mac(payload []byte) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// GetUser decodes and verifies the signed user cookie into dst (a pointer),
// returning true on success. Returns false if the cookie is missing, its
// signature doesn't match, or it doesn't decode into dst.
func (c Cookies) GetUser(r *http.Request, dst any) bool {
	ck, err := r.Cookie(c.User)
	if err != nil {
		return false
	}
	payload, mac, ok := strings.Cut(ck.Value, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(c.mac([]byte(payload)))) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	return json.Unmarshal(decoded, dst) == nil
}

// GetToken returns the session token cookie's value, or "" if not set.
func (c Cookies) GetToken(r *http.Request) string {
	ck, err := r.Cookie(c.Token)
	if err != nil {
		return ""
	}
	return ck.Value
}

// Set sets the token and signed-user cookies, both expiring at expiresAt.
func (c Cookies) Set(w http.ResponseWriter, token string, user any, expiresAt time.Time) {
	userJSON, _ := json.Marshal(user)
	payload := base64.StdEncoding.EncodeToString(userJSON)
	userEncoded := payload + "." + c.mac([]byte(payload))
	http.SetCookie(w, &http.Cookie{
		Name:     c.Token,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     c.User,
		Value:    userEncoded,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// SetUser re-signs and overwrites only the user cookie, leaving the token
// cookie untouched. Used to refresh the HMAC-signed user blob after a process
// restart without disturbing the raw session token stored in the browser.
func (c Cookies) SetUser(w http.ResponseWriter, user any, expiresAt time.Time) {
	userJSON, _ := json.Marshal(user)
	payload := base64.StdEncoding.EncodeToString(userJSON)
	http.SetCookie(w, &http.Cookie{
		Name:     c.User,
		Value:    payload + "." + c.mac([]byte(payload)),
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Clear deletes both session cookies.
func (c Cookies) Clear(w http.ResponseWriter) {
	for _, name := range []string{c.Token, c.User} {
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
