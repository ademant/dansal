package main

import (
	"context"
	"log"
	"net"
	"net/url"
	"time"
)

// validateURLDomain resolves the host of rawURL via DNS. DNS errors (timeouts,
// SERVFAIL) are treated as pass-through to avoid false positives on transient
// failures. Only a definitive NXDOMAIN (empty result, no error) is rejected.
// Returns nil when rawURL is empty or unparseable (other validators handle those).
func validateURLDomain(ctx context.Context, rawURL string) error {
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil
	}
	host := u.Hostname()

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		log.Printf("validateURLDomain: lookup %q: %v (allowing)", host, err)
		return nil
	}
	if len(addrs) == 0 {
		return &urlDomainError{host: host}
	}
	return nil
}

type urlDomainError struct{ host string }

func (e *urlDomainError) Error() string {
	return "domain does not exist: " + e.host
}
