package main

import (
	_ "embed"
	"fmt"
	"net/url"

	"gopkg.in/yaml.v2"
)

// Registration email texts (#1809). The table lives in email_i18n.yaml, one
// block per language; the applicant's language is stored on the pending
// registration (pending_registrations.lang).

//go:embed email_i18n.yaml
var emailI18nYAML []byte

var emailTexts = mustLoadEmailTexts()

// emailSiteDefault is used for a registration with no language stored (rows
// from before the lang column). It is the site default (cmd/dansal_web).
const emailSiteDefault = "de"

// emailFallback is used when the stored language is not one of the 12.
const emailFallback = "en"

func mustLoadEmailTexts() map[string]map[string]string {
	var out map[string]map[string]string
	if err := yaml.Unmarshal(emailI18nYAML, &out); err != nil {
		panic(fmt.Sprintf("email_i18n.yaml: %v", err))
	}
	return out
}

// emailText returns the text for key in lang, falling back to English. The
// result is a fmt format; callers pass the arguments the key documents.
func emailText(lang, key string) string {
	if lang == "" {
		lang = emailSiteDefault
	}
	if t, ok := emailTexts[lang][key]; ok {
		return t
	}
	return emailTexts[emailFallback][key]
}

// emailMessage returns the subject and the formatted body for a registration
// email. subjectKey and bodyKey are keys in email_i18n.yaml.
func emailMessage(lang, subjectKey, bodyKey string, args ...any) (string, string) {
	return emailText(lang, subjectKey), fmt.Sprintf(emailText(lang, bodyKey), args...)
}

// registerLang returns the language to store for a registration: "" (not
// stored, site default) when none was sent, the code itself when it is one
// of the 12, and en for anything else.
func registerLang(lang string) string {
	if lang == "" {
		return ""
	}
	if _, ok := emailTexts[lang]; ok {
		return lang
	}
	return emailFallback
}

// emailLoginURL is the sign-in link for an approved applicant. It carries the
// address only, to prefill the login form; no token.
func emailLoginURL(base, email string) string {
	return base + "/login?email=" + url.QueryEscape(email)
}
