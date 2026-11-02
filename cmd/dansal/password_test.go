package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// withPasswordPolicy sets the rules for one test and restores the config after.
func withPasswordPolicy(t *testing.T, p PasswordConfig) {
	t.Helper()
	old := config
	config = &Config{Password: p}
	t.Cleanup(func() { config = old })
}

// TestValidatePasswordRules covers #1739: length counts characters, max and
// class rules apply, and the breach check can be turned off for offline use.
func TestValidatePasswordRules(t *testing.T) {
	ctx := context.Background()
	noBreach := PasswordConfig{MinLength: 8, MaxLength: 16, RequireClasses: 0, CheckBreached: false}
	cases := []struct {
		name    string
		policy  PasswordConfig
		pw      string
		wantErr string // "" = accepted
	}{
		{"short ascii", noBreach, "abcdefg", "too_short"},
		{"eight ascii", noBreach, "abcdefgh", ""},
		{"umlauts count as characters, not bytes", noBreach, "äöüß", "too_short"},
		{"eight umlaut characters pass", noBreach, "äöüßäöüß", ""},
		{"too long", noBreach, strings.Repeat("a", 17), "too_long"},
		{"max 0 means no limit", PasswordConfig{MinLength: 8, MaxLength: 0}, strings.Repeat("a", 500), ""},
		{"classes missing", PasswordConfig{MinLength: 8, RequireClasses: 3}, "abcdefghij", "classes"},
		{"classes met", PasswordConfig{MinLength: 8, RequireClasses: 3}, "Abcdefg1", ""},
		{"symbol counts as a class", PasswordConfig{MinLength: 8, RequireClasses: 4}, "abcdef1!", "classes"},
		{"three classes meet a rule of three", PasswordConfig{MinLength: 8, RequireClasses: 3}, "abcdef1!", ""},
		{"spaces are not a symbol", PasswordConfig{MinLength: 8, RequireClasses: 3}, "abc def1", "classes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withPasswordPolicy(t, tc.policy)
			err := validatePassword(ctx, tc.pw)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validatePassword(%q) = %v, want accepted", tc.pw, err)
				}
				return
			}
			var pe *passwordRuleError
			if !errors.As(err, &pe) || pe.Code != tc.wantErr {
				t.Fatalf("validatePassword(%q) = %v, want code %s", tc.pw, err, tc.wantErr)
			}
		})
	}
}

// TestPasswordErrorIsSentWithItsCode covers #1739: clients get a stable
// error_code next to the message.
func TestPasswordErrorIsSentWithItsCode(t *testing.T) {
	withPasswordPolicy(t, PasswordConfig{MinLength: 8})
	err := validatePassword(context.Background(), "short")
	rec := httptest.NewRecorder()
	writePasswordError(rec, err, 422)
	if rec.Code != 422 {
		t.Fatalf("status %d, want 422", rec.Code)
	}
	var body map[string]string
	if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if body["error_code"] != "too_short" || !strings.Contains(body["error"], "at least 8") {
		t.Fatalf("body %v, want error_code too_short and the minimum in the message", body)
	}
}

// TestDefaultPasswordPolicyMatchesOldRules covers #1739: without a config file
// the rules stay as they were (8 characters, breach check on).
func TestDefaultPasswordPolicyMatchesOldRules(t *testing.T) {
	d := defaultPasswordConfig()
	if d.MinLength != 8 || d.MaxLength != 128 || d.RequireClasses != 0 || !d.CheckBreached {
		t.Fatalf("defaults = %+v", d)
	}
}
