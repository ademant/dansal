package main

import (
	"strings"
	"testing"
)

// #1809: the registration emails follow the applicant's language, with the
// site default for rows without one and English for an unknown one.
func TestRegistrationEmailLanguage(t *testing.T) {
	if got := registerLang(""); got != "" {
		t.Errorf("registerLang(\"\") = %q, want \"\" (site default)", got)
	}
	if got := registerLang("fr"); got != "fr" {
		t.Errorf("registerLang(fr) = %q, want fr", got)
	}
	if got := registerLang("xx"); got != "en" {
		t.Errorf("registerLang(xx) = %q, want en", got)
	}

	subjFR, _ := emailMessage("fr", "subject_verify", "verify", "https://x/v")
	if subjFR != "Confirmez votre inscription" {
		t.Errorf("fr subject = %q", subjFR)
	}
	subjDefault, _ := emailMessage("", "subject_verify", "verify", "https://x/v")
	if subjDefault != "Bestätige deine Registrierung" {
		t.Errorf("empty language should use the site default de, got %q", subjDefault)
	}
	subjUnknown, _ := emailMessage("xx", "subject_verify", "verify", "https://x/v")
	if subjUnknown != "Confirm your registration" {
		t.Errorf("unknown language should fall back to en, got %q", subjUnknown)
	}
}

// Every language has every key, so no email is ever sent with a raw key.
func TestEmailTextsCoverAllLanguagesAndKeys(t *testing.T) {
	keys := []string{"subject_verify", "verify", "subject_approved", "approved_password",
		"approved_passkey", "approved_invite", "subject_rejected", "rejected", "rejected_reason"}
	if len(emailTexts) != 12 {
		t.Fatalf("got %d languages, want 12", len(emailTexts))
	}
	for lang, texts := range emailTexts {
		for _, k := range keys {
			if texts[k] == "" {
				t.Errorf("%s is missing %s", lang, k)
			}
		}
	}
}

func TestApprovalMailCarriesSignInLink(t *testing.T) {
	link := emailLoginURL("https://balfolk.example", "anna+x@example.test")
	if link != "https://balfolk.example/login?email=anna%2Bx%40example.test" {
		t.Fatalf("login URL = %q", link)
	}
	_, body := emailMessage("de", "subject_approved", "approved_password", link)
	if !strings.Contains(body, link) {
		t.Errorf("approval body lacks the sign-in link: %q", body)
	}
	_, invite := emailMessage("en", "subject_approved", "approved_invite", "https://x/invites/t", 48)
	if !strings.Contains(invite, "https://x/invites/t") || !strings.Contains(invite, "48 hours") {
		t.Errorf("invite body = %q", invite)
	}
}

func TestRejectionMailShowsReasonOnlyWhenGiven(t *testing.T) {
	_, withReason := emailMessage("en", "subject_rejected", "rejected_reason", "duplicate account")
	if !strings.Contains(withReason, "Reason: duplicate account") {
		t.Errorf("reason missing: %q", withReason)
	}
	_, without := emailMessage("en", "subject_rejected", "rejected")
	if strings.Contains(without, "Reason:") {
		t.Errorf("reason line without a reason: %q", without)
	}
}
