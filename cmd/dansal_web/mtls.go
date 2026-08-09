package main

import (
	"log"
	"net/http"
	"strings"
)

// certExtractCN parses the CN value from a DN string like "CN=alice,O=dansal"
func certExtractCN(dn string) string {
	for _, part := range strings.Split(dn, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToUpper(part), "CN=") {
			return part[3:]
		}
	}
	return ""
}

// certAuthMiddleware checks nginx-forwarded mTLS headers on /admin/ requests.
// If the cert is verified and the CN resolves to an admin user, a session is
// created transparently — the admin never sees the login page.
// dansal_web only listens on localhost (behind nginx), so these headers are trusted.
func certAuthMiddleware(client *DansalClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Client-Verified") != "SUCCESS" {
				next.ServeHTTP(w, r)
				return
			}
			cn := certExtractCN(r.Header.Get("X-Client-DN"))
			if cn == "" {
				next.ServeHTTP(w, r)
				return
			}

			lr, err := client.CertLogin(r.Context(), cn)
			if err != nil {
				log.Printf("cert-auth: CertLogin for %q failed: %v", cn, err)
				next.ServeHTTP(w, r)
				return
			}

			su := &SessionUser{
				ID:          lr.User.ID,
				Email:       lr.User.Email,
				DisplayName: lr.User.DisplayName,
				Role:        lr.User.Role,
			}
			establishSession(w, lr)
			next.ServeHTTP(w, withSessionUser(r, su))
		})
	}
}
