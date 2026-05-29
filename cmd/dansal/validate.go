package main

import (
	"context"
	"fmt"
	"log"
	"net"
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

// isValidMatrixID checks that s is a fully-qualified Matrix user ID: @localpart:server
func isValidMatrixID(s string) bool {
	if !strings.HasPrefix(s, "@") {
		return false
	}
	colon := strings.IndexByte(s, ':')
	return colon > 1 && colon < len(s)-1
}
