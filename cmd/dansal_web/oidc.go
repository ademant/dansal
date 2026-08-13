package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// oidcFlowCookie carries the flow_id issued by dansal's POST /api/v1/oidc/start
// across the redirect to the provider and back — short-lived, httpOnly, and
// scoped to the /oidc path so it's never sent anywhere else.
const oidcFlowCookie = "dsw_oidc_flow"

// oidcStartHandler begins an OIDC login or invite-redemption flow. All
// protocol state and secrets live on the dansal API (see cmd/dansal/oidc.go)
// — dansal-web only exists on this path because it's the only side a
// redirecting browser can ever reach (dansal_url is loopback-only).
// GET /oidc/{id}/start?invite=<token>
func oidcStartHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		invite := r.URL.Query().Get("invite")
		redirectURI := cfg.publicBaseURL() + "/oidc/" + strconv.Itoa(providerID) + "/callback"

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		flowID, authorizeURL, err := client.OIDCStart(ctx, providerID, redirectURI, invite)
		if err != nil {
			log.Printf("oidc start: %v", err)
			http.Redirect(w, r, oidcErrorRedirect(invite), http.StatusSeeOther)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     oidcFlowCookie,
			Value:    flowID,
			Path:     "/oidc",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   600,
		})
		http.Redirect(w, r, authorizeURL, http.StatusSeeOther)
	}
}

// oidcCallbackHandler receives the provider's redirect back to dansal (the
// standard OAuth2 "code"+"state" query params), forwards them to the dansal
// API for the actual exchange and ID-token verification, and establishes a
// session on success.
// GET /oidc/{id}/callback
func oidcCallbackHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		cookie, cookieErr := r.Cookie(oidcFlowCookie)
		// Clear the flow cookie unconditionally — it's one-shot either way.
		http.SetCookie(w, &http.Cookie{Name: oidcFlowCookie, Value: "", Path: "/oidc", MaxAge: -1})

		if cookieErr != nil || code == "" || state == "" {
			http.Redirect(w, r, "/login?error=oidc", http.StatusSeeOther)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		sess, err := client.OIDCCallback(ctx, cookie.Value, code, state)
		if err != nil {
			log.Printf("oidc callback: %v", err)
			http.Redirect(w, r, "/login?error=oidc", http.StatusSeeOther)
			return
		}
		setSession(w, sess.Token, SessionUser{
			ID:    sess.UserID,
			Email: sess.Email,
			Role:  sess.Role,
		}, parseSessionExpiry(sess.ExpiresAt))
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}
}

// oidcErrorRedirect sends the user back to wherever they started (the
// invite page if this was an invite redemption, otherwise /login) with an
// error flag, rather than stranding them on a bare error page.
func oidcErrorRedirect(inviteToken string) string {
	if inviteToken != "" {
		return "/invites/" + url.PathEscape(inviteToken) + "?error=oidc"
	}
	return "/login?error=oidc"
}

// oidcLinkFlowCookie carries the flow_id issued by dansal's
// POST /api/v1/oidc/link-start (#1096) — separate from oidcFlowCookie/path
// so a login flow and a link flow started in two tabs can never collide.
const oidcLinkFlowCookie = "dsw_oidc_link_flow"

// oidcLinkStartHandler begins a link flow for an already-authenticated user
// who wants to attach providerID to their existing account — reached from
// the "Link" button on /settings. GET /settings/oidc/{id}/link-start
func oidcLinkStartHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireLogin(w, r); !ok {
			return
		}
		providerID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		redirectURI := cfg.publicBaseURL() + "/settings/oidc/" + strconv.Itoa(providerID) + "/link-callback"
		token := getSessionToken(r)

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		flowID, authorizeURL, err := client.OIDCLinkStart(ctx, providerID, redirectURI, token)
		if err != nil {
			log.Printf("oidc link start: %v", err)
			http.Redirect(w, r, "/settings?linkerr=1", http.StatusSeeOther)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     oidcLinkFlowCookie,
			Value:    flowID,
			Path:     "/settings/oidc",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   600,
		})
		http.Redirect(w, r, authorizeURL, http.StatusSeeOther)
	}
}

// oidcLinkCallbackHandler receives the provider's redirect back for a link
// flow and reports success/failure back to /settings. No session is
// re-issued — the caller was already logged in throughout.
// GET /settings/oidc/{id}/link-callback
func oidcLinkCallbackHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireLogin(w, r); !ok {
			return
		}
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		cookie, cookieErr := r.Cookie(oidcLinkFlowCookie)
		http.SetCookie(w, &http.Cookie{Name: oidcLinkFlowCookie, Value: "", Path: "/settings/oidc", MaxAge: -1})

		if cookieErr != nil || code == "" || state == "" {
			http.Redirect(w, r, "/settings?linkerr=1", http.StatusSeeOther)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := client.OIDCLinkCallback(ctx, cookie.Value, code, state); err != nil {
			log.Printf("oidc link callback: %v", err)
			http.Redirect(w, r, "/settings?linkerr=1", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/settings?linked=1", http.StatusSeeOther)
	}
}
