package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// TestChangeOwnPasswordWithoutOld covers #1888: a session started by a recent
// non-password login may set a new password without the old one; a password
// session, an old non-password session and a session of another user may
// not. A TOTP code authorises the change in place of the old password.
func TestChangeOwnPasswordWithoutOld(t *testing.T) {
	setupOIDCTestDB(t)

	pwHash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec("INSERT INTO users (display_name, password_hash, role) VALUES ('PW', ?, 'user')", pwHash)
	if err != nil {
		t.Fatal(err)
	}
	uid64, _ := res.LastInsertId()
	userID := int(uid64)

	session := func(method string, age time.Duration) int {
		t.Helper()
		created := time.Now().UTC().Add(-age).Format("2006-01-02 15:04:05")
		res, err := db.Exec(
			"INSERT INTO tokens (user_id, token, expires_at, login_method, created_at) VALUES (?, ?, ?, ?, ?)",
			userID, "tok-"+method+"-"+age.String(), time.Now().Add(time.Hour).Unix(), method, created,
		)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return int(id)
	}
	change := func(sessionID int, body map[string]string) int {
		t.Helper()
		b, _ := json.Marshal(body)
		r := authedRequest(t, http.MethodPost, "/api/v1/me/password", b, userID, RoleUser)
		if sessionID != 0 {
			r.Header.Set("X-Session-ID", strconv.Itoa(sessionID))
		}
		w := httptest.NewRecorder()
		changeOwnPassword(w, r)
		return w.Code
	}
	newPw := map[string]string{"new_password": "a fresh and long passphrase 42"}

	for _, tc := range []struct {
		name      string
		sessionID int
		want      int
	}{
		{"no session (API key)", 0, http.StatusBadRequest},
		{"password session", session(loginMethodPassword, time.Minute), http.StatusBadRequest},
		{"legacy session without method", session("", time.Minute), http.StatusBadRequest},
		{"old magic session", session(loginMethodMagic, 20*time.Minute), http.StatusBadRequest},
		{"recent magic session", session(loginMethodMagic, time.Minute), http.StatusNoContent},
		{"recent passkey session", session(loginMethodPasskey, time.Minute), http.StatusNoContent},
		{"recent oidc session", session(loginMethodOIDC, time.Minute), http.StatusNoContent},
	} {
		if got := change(tc.sessionID, newPw); got != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, got, tc.want)
		}
	}

	// A session of another user does not count for this one.
	res, _ = db.Exec("INSERT INTO users (display_name, role) VALUES ('Other', 'user')")
	other64, _ := res.LastInsertId()
	res, _ = db.Exec("INSERT INTO tokens (user_id, token, expires_at, login_method) VALUES (?, 'tok-other', ?, ?)",
		other64, time.Now().Add(time.Hour).Unix(), loginMethodMagic)
	otherSession, _ := res.LastInsertId()
	if got := change(int(otherSession), newPw); got != http.StatusBadRequest {
		t.Errorf("other user's session: status %d, want 400", got)
	}

	// TOTP code in place of the old password.
	secret, err := totpNewSecret()
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("UPDATE users SET totp_secret=? WHERE id=?", secret, userID)
	if got := change(0, map[string]string{"new_password": newPw["new_password"], "totp_code": "000000"}); got != http.StatusUnauthorized {
		t.Errorf("wrong TOTP code: status %d, want 401", got)
	}
	code, err := totpGenerate(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := change(0, map[string]string{"new_password": newPw["new_password"], "totp_code": code}); got != http.StatusNoContent {
		t.Errorf("valid TOTP code: status %d, want 204", got)
	}
}
