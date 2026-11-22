package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1876: a password or invite rejection names the rule that failed, with the
// instance's numbers, and keeps the API's status.
func TestAccountErrorMessageNamesTheRule(t *testing.T) {
	i18n := loadI18n("")
	req := httptest.NewRequest(http.MethodPost, "/invites/x/password", nil)
	req.Header.Set("Accept-Language", "en")
	old := currentPasswordPolicy()
	setPasswordPolicy(PasswordPolicy{MinLength: 12, MaxLength: 64, RequireClasses: 3})
	defer setPasswordPolicy(old)

	cases := []struct {
		code       string
		status     int
		wantSubstr string
	}{
		{"too_short", http.StatusUnprocessableEntity, "12"},
		{"too_long", http.StatusUnprocessableEntity, "64"},
		{"classes", http.StatusUnprocessableEntity, "3"},
		{"breached", http.StatusUnprocessableEntity, "breach"},
		{"display_name_taken", http.StatusConflict, "display name"},
		{"invite_used", http.StatusGone, "already been used"},
	}
	for _, c := range cases {
		err := &apiHTTPError{StatusCode: c.status, Message: "API English", ErrorCode: c.code}
		msg, status := accountErrorMessage(req, i18n, err)
		if status != c.status {
			t.Errorf("%s: status = %d, want %d", c.code, status, c.status)
		}
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(c.wantSubstr)) {
			t.Errorf("%s: message %q does not contain %q", c.code, msg, c.wantSubstr)
		}
		if strings.Contains(msg, "dansal API") || msg == "API English" {
			t.Errorf("%s: raw API text leaked: %q", c.code, msg)
		}
	}
}

// An error without a known code keeps the API's message; a failure that never
// reached the API gets the generic text and 502.
func TestAccountErrorMessageFallbacks(t *testing.T) {
	i18n := loadI18n("")
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	msg, status := accountErrorMessage(req, i18n, &apiHTTPError{StatusCode: 400, Message: "something else"})
	if msg != "something else" || status != 400 {
		t.Errorf("unknown code: got (%q, %d)", msg, status)
	}
	msg, status = accountErrorMessage(req, i18n, errors.New("connection refused"))
	if status != http.StatusBadGateway || msg == "" || strings.Contains(msg, "connection refused") {
		t.Errorf("transport error: got (%q, %d)", msg, status)
	}
}
