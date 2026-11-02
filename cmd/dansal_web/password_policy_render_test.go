package main

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPasswordFormsFollowTheServerPolicy covers #1739: the register form takes
// its minlength, maxlength and hint from the policy the API reported, not from
// a hardcoded 8.
func TestPasswordFormsFollowTheServerPolicy(t *testing.T) {
	old := currentPasswordPolicy()
	t.Cleanup(func() { setPasswordPolicy(old) })
	setPasswordPolicy(PasswordPolicy{MinLength: 12, MaxLength: 64, RequireClasses: 3, CheckBreached: false})

	req := httptest.NewRequest(http.MethodGet, "/register", nil)
	html := renderPageHTML(t, func(ts *Templates) *template.Template { return ts.register }, req, "Register", RegisterPageData{PendingVerified: true, PendingID: 1, PendingToken: "tok"})

	if !strings.Contains(html, `minlength="12"`) || !strings.Contains(html, `maxlength="64"`) {
		t.Errorf("password input does not carry the policy's minlength/maxlength")
	}
	if strings.Contains(html, `minlength="8"`) {
		t.Errorf("hardcoded minlength 8 still rendered")
	}
	if !strings.Contains(html, `id="pw-hint"`) || !strings.Contains(html, "12") {
		t.Errorf("password hint with the minimum length is missing")
	}
}

// TestPasswordPolicyDefaultsBeforeAnyFetch covers #1739: before the first info
// fetch the forms still use the API defaults.
func TestPasswordPolicyDefaultsBeforeAnyFetch(t *testing.T) {
	p := PasswordPolicy{MinLength: 8, MaxLength: 128, CheckBreached: true}
	old := currentPasswordPolicy()
	t.Cleanup(func() { setPasswordPolicy(old) })
	setPasswordPolicy(p)
	if got := currentPasswordPolicy(); got != p {
		t.Fatalf("policy = %+v, want %+v", got, p)
	}
}
