package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"time"
)

// isValidEmail checks basic email syntax: exactly one "@" not at the edges,
// non-empty local part (≤64 chars), domain with at least one ".", no whitespace,
// total length ≤254 (RFC 5321).
func isValidEmail(s string) bool {
	if len(s) > 254 || len(s) == 0 {
		return false
	}
	at := strings.Index(s, "@")
	if at <= 0 || at == len(s)-1 || strings.Count(s, "@") != 1 {
		return false
	}
	local, domain := s[:at], s[at+1:]
	if len(local) > 64 {
		return false
	}
	if strings.ContainsAny(s, " \t\n\r") {
		return false
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	return true
}

// validateEmailDomain checks that the email's domain has a mail server via MX
// lookup, with an RFC 5321 §5.1 fallback to A/AAAA. DNS errors (SERVFAIL,
// timeout) are logged and treated as pass-through to avoid false positives.
func validateEmailDomain(ctx context.Context, email string) error {
	domain := email[strings.Index(email, "@")+1:]

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	r := &net.Resolver{}
	mxs, mxErr := r.LookupMX(ctx, domain)
	if mxErr == nil && len(mxs) > 0 {
		return nil
	}
	if mxErr != nil {
		log.Printf("validateEmailDomain: MX lookup %q: %v (allowing)", domain, mxErr)
		return nil
	}

	// MX returned cleanly empty — try A/AAAA fallback.
	addrs, aErr := r.LookupHost(ctx, domain)
	if aErr != nil {
		log.Printf("validateEmailDomain: A lookup %q: %v (allowing)", domain, aErr)
		return nil
	}
	if len(addrs) == 0 {
		return fmt.Errorf("domain %q has no mail server", domain)
	}
	return nil
}

// looksLikeGmailDotSpam flags Gmail/Googlemail addresses with an unusually
// choppy dot pattern in the local part (e.g. "u.d.i.ja.x.i.r.u17@gmail.com").
// Gmail and Googlemail ignore dots in the local part, so this is a common
// trick to mint many "unique" addresses that all land in one inbox, used to
// dodge per-email duplicate checks. Legitimate addresses rarely have more
// than 2-3 dot-separated segments (e.g. "j.r.tolkien@gmail.com"), so the bar
// here — 5+ segments, most of them 1-2 chars — is set well above that.
func looksLikeGmailDotSpam(email string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	local, domain := email[:at], strings.ToLower(email[at+1:])
	if domain != "gmail.com" && domain != "googlemail.com" {
		return false
	}
	parts := strings.Split(local, ".")
	if len(parts) < 5 {
		return false
	}
	short := 0
	for _, p := range parts {
		if len(p) <= 2 {
			short++
		}
	}
	return short*10 >= len(parts)*6 // ≥60% of segments are ≤2 chars
}

var (
	reTelegramUsername = regexp.MustCompile(`^@?[A-Za-z0-9_]{5,32}$`)
	reTelegramLink     = regexp.MustCompile(`(?i)^https?://(t\.me|telegram\.me)/[A-Za-z0-9_]{5,32}$`)
	reTelegramPhone    = regexp.MustCompile(`^\+?\d{5,15}$`)
)

// isValidTelegramContact checks that s is a plausible Telegram identifier:
// a bare username (@optional, 5–32 alphanumeric+underscore chars), a t.me/
// telegram.me link, or a phone number (optional +, 5–15 digits).
func isValidTelegramContact(s string) bool {
	s = strings.TrimSpace(s)
	return reTelegramUsername.MatchString(s) || reTelegramLink.MatchString(s) || reTelegramPhone.MatchString(s)
}

// isValidMatrixID checks that s is a fully-qualified Matrix user ID: @localpart:server
func isValidMatrixID(s string) bool {
	if !strings.HasPrefix(s, "@") {
		return false
	}
	colon := strings.IndexByte(s, ':')
	return colon > 1 && colon < len(s)-1
}
