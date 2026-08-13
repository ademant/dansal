package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// oidcFlowTTL is how long an authorization round-trip (redirect to the
// provider and back) has to complete before its state/nonce/PKCE verifier
// expire — generous enough for a slow login at the IdP, short enough that a
// stale flow row is not a long-lived attack surface.
const oidcFlowTTL = 10 * time.Minute

// OIDCProvider is a registered external identity provider (#1095).
// org_id NULL means instance-wide (shown on the main login page); a set
// org_id scopes it to that organization's own IdP (e.g. their WordPress
// site), offered on invites/logins for that org only.
type OIDCProvider struct {
	ID          int    `json:"id"`
	OrgID       *int   `json:"org_id,omitempty"`
	IssuerURL   string `json:"issuer_url"`
	ClientID    string `json:"client_id"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
}

// oidcDiscoveryCache memoizes *oidc.Provider by issuer URL for the process
// lifetime — discovery is an HTTP round-trip to the issuer's
// well-known/openid-configuration and its JWKS rarely change.
var (
	oidcDiscoveryMu    sync.Mutex
	oidcDiscoveryCache = map[string]*oidc.Provider{}
)

func oidcDiscover(ctx context.Context, issuerURL string) (*oidc.Provider, error) {
	oidcDiscoveryMu.Lock()
	if p, ok := oidcDiscoveryCache[issuerURL]; ok {
		oidcDiscoveryMu.Unlock()
		return p, nil
	}
	oidcDiscoveryMu.Unlock()

	p, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, err
	}
	oidcDiscoveryMu.Lock()
	oidcDiscoveryCache[issuerURL] = p
	oidcDiscoveryMu.Unlock()
	return p, nil
}

func loadOIDCProvider(id int) (OIDCProvider, string, error) {
	var p OIDCProvider
	var orgID sql.NullInt64
	var secret string
	var enabledInt int
	err := db.QueryRow(
		"SELECT id, org_id, issuer_url, client_id, client_secret, display_name, enabled FROM oidc_providers WHERE id=?", id,
	).Scan(&p.ID, &orgID, &p.IssuerURL, &p.ClientID, &secret, &p.DisplayName, &enabledInt)
	if err != nil {
		return OIDCProvider{}, "", err
	}
	if orgID.Valid {
		v := int(orgID.Int64)
		p.OrgID = &v
	}
	p.Enabled = enabledInt != 0
	return p, secret, nil
}

func oidcOAuth2Config(p OIDCProvider, secret string, provider *oidc.Provider, redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: secret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURI,
		Scopes:       []string{oidc.ScopeOpenID},
	}
}

// GET /api/v1/oidc/providers?org_id=N — public list of enabled providers for
// building "Continue with X" buttons. Never includes client_secret. With no
// org_id, returns instance-wide providers only; with org_id, returns
// instance-wide plus that org's own providers.
func getOIDCProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var orgID *int
	if s := r.URL.Query().Get("org_id"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			orgID = &v
		}
	}
	// Admins managing the provider registry (/admin/oidc-providers) need to
	// see disabled and org-scoped rows too; everyone else only ever gets the
	// enabled, applicable set used to render "Continue with X" buttons.
	_, role := callerFromRequest(r)
	isAdmin := role == RoleAdmin

	var rows *sql.Rows
	var err error
	switch {
	case isAdmin:
		rows, err = db.Query("SELECT id, org_id, issuer_url, client_id, display_name, enabled FROM oidc_providers ORDER BY display_name")
	case orgID != nil:
		rows, err = db.Query(
			"SELECT id, org_id, issuer_url, client_id, display_name, enabled FROM oidc_providers WHERE enabled=1 AND (org_id IS NULL OR org_id=?) ORDER BY display_name",
			*orgID,
		)
	default:
		rows, err = db.Query(
			"SELECT id, org_id, issuer_url, client_id, display_name, enabled FROM oidc_providers WHERE enabled=1 AND org_id IS NULL ORDER BY display_name",
		)
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	providers := []OIDCProvider{}
	for rows.Next() {
		var p OIDCProvider
		var oid sql.NullInt64
		var enabledInt int
		if err := rows.Scan(&p.ID, &oid, &p.IssuerURL, &p.ClientID, &p.DisplayName, &enabledInt); err != nil {
			writeInternalError(w, err)
			return
		}
		if oid.Valid {
			v := int(oid.Int64)
			p.OrgID = &v
		}
		p.Enabled = enabledInt != 0
		providers = append(providers, p)
	}
	json.NewEncoder(w).Encode(providers)
}

// POST /api/v1/oidc/providers — admin-only.
func createOIDCProvider(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, role := callerFromRequest(r)
	if role != RoleAdmin {
		writeError(w, "Forbidden: only admins may manage OIDC providers", http.StatusForbidden)
		return
	}
	var req struct {
		OrgID        *int   `json:"org_id"`
		IssuerURL    string `json:"issuer_url"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		DisplayName  string `json:"display_name"`
		Enabled      *bool  `json:"enabled"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.IssuerURL == "" || req.ClientID == "" || req.ClientSecret == "" || req.DisplayName == "" {
		writeError(w, "issuer_url, client_id, client_secret, and display_name are required", http.StatusBadRequest)
		return
	}
	// Fail fast on a bad issuer rather than accepting a provider that will
	// error on every login attempt.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := oidcDiscover(ctx, req.IssuerURL); err != nil {
		writeError(w, fmt.Sprintf("could not discover OIDC issuer: %v", err), http.StatusBadRequest)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	var orgVal any
	if req.OrgID != nil {
		orgVal = *req.OrgID
	}
	result, err := db.Exec(
		"INSERT INTO oidc_providers (org_id, issuer_url, client_id, client_secret, display_name, enabled) VALUES (?, ?, ?, ?, ?, ?)",
		orgVal, req.IssuerURL, req.ClientID, req.ClientSecret, req.DisplayName, enabled,
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	id, _ := result.LastInsertId()
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(OIDCProvider{
		ID: int(id), OrgID: req.OrgID, IssuerURL: req.IssuerURL, ClientID: req.ClientID,
		DisplayName: req.DisplayName, Enabled: enabled,
	})
}

// DELETE /api/v1/oidc/providers/{id} — admin-only.
func deleteOIDCProvider(w http.ResponseWriter, r *http.Request) {
	_, role := callerFromRequest(r)
	if role != RoleAdmin {
		writeError(w, "Forbidden: only admins may manage OIDC providers", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	if _, err := db.Exec("DELETE FROM oidc_providers WHERE id=?", id); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// oidcStartRequest is the body of POST /api/v1/oidc/start.
type oidcStartRequest struct {
	ProviderID  int    `json:"provider_id"`
	RedirectURI string `json:"redirect_uri"`
	InviteToken string `json:"invite_token,omitempty"`
}

// POST /api/v1/oidc/start — begins an authorization-code+PKCE flow. Called
// by dansal-web's /oidc/{id}/start route (the only side a browser can ever
// reach, since dansal's own API is loopback-only per web.yaml's dansal_url).
// Returns an authorize_url to redirect the browser to, and a flow_id the
// caller must round-trip back via POST /api/v1/oidc/callback.
func oidcStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	clientIP := getClientIP(r)
	if !loginRateLimiter.Allow(clientIP) {
		writeError(w, "Too many requests", http.StatusTooManyRequests)
		return
	}
	var req oidcStartRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.RedirectURI == "" {
		writeError(w, "redirect_uri is required", http.StatusBadRequest)
		return
	}
	p, secret, err := loadOIDCProvider(req.ProviderID)
	if err == sql.ErrNoRows {
		writeError(w, "Unknown OIDC provider", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !p.Enabled {
		writeError(w, "OIDC provider is disabled", http.StatusForbidden)
		return
	}
	// Fail fast, before ever redirecting the browser anywhere, if the invite
	// is already unusable.
	if req.InviteToken != "" {
		if _, err := loadValidInvite(req.InviteToken); err != nil {
			writeError(w, "Invalid or expired invite link", http.StatusGone)
			return
		}
	}

	discovered, err := oidcDiscover(r.Context(), p.IssuerURL)
	if err != nil {
		writeError(w, "Could not reach OIDC provider", http.StatusBadGateway)
		return
	}
	oauth2Cfg := oidcOAuth2Config(p, secret, discovered, req.RedirectURI)

	state, err := generateToken(24)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	nonce, err := generateToken(24)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	flowID, err := generateToken(24)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	var inviteVal any
	if req.InviteToken != "" {
		inviteVal = req.InviteToken
	}
	if _, err := db.Exec(
		"INSERT INTO oidc_flows (id, provider_id, state, nonce, pkce_verifier, redirect_uri, invite_token, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		flowID, p.ID, state, nonce, verifier, req.RedirectURI, inviteVal, time.Now().Add(oidcFlowTTL).Unix(),
	); err != nil {
		writeInternalError(w, err)
		return
	}

	authURL := oauth2Cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce))
	json.NewEncoder(w).Encode(map[string]string{"flow_id": flowID, "authorize_url": authURL})
}

type oidcFlow struct {
	ProviderID   int
	State        string
	Nonce        string
	PKCEVerifier string
	RedirectURI  string
	InviteToken  string
	ExpiresAt    int64
}

// loadAndConsumeOIDCFlow retrieves and immediately deletes a flow row
// (one-time use, mirrors loadWASession).
func loadAndConsumeOIDCFlow(flowID string) (oidcFlow, error) {
	var f oidcFlow
	var inviteToken sql.NullString
	err := db.QueryRow(
		"SELECT provider_id, state, nonce, pkce_verifier, redirect_uri, invite_token, expires_at FROM oidc_flows WHERE id=?", flowID,
	).Scan(&f.ProviderID, &f.State, &f.Nonce, &f.PKCEVerifier, &f.RedirectURI, &inviteToken, &f.ExpiresAt)
	if err == sql.ErrNoRows {
		return oidcFlow{}, fmt.Errorf("flow not found")
	}
	if err != nil {
		return oidcFlow{}, err
	}
	db.Exec("DELETE FROM oidc_flows WHERE id=?", flowID)
	if inviteToken.Valid {
		f.InviteToken = inviteToken.String
	}
	if time.Now().Unix() > f.ExpiresAt {
		return oidcFlow{}, fmt.Errorf("flow expired")
	}
	return f, nil
}

// oidcCallbackRequest is the body of POST /api/v1/oidc/callback.
type oidcCallbackRequest struct {
	FlowID string `json:"flow_id"`
	Code   string `json:"code"`
	State  string `json:"state"`
}

// POST /api/v1/oidc/callback — completes the authorization-code+PKCE
// exchange, verifies the ID token, and either redeems an invite (creating a
// zero-PII account: no email, no display name, just role+org from the
// invite and the external (issuer, subject) pair) or logs in an existing
// linked account. Response shape matches issueSessionResponse.
func oidcCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	clientIP := getClientIP(r)
	if !loginRateLimiter.Allow(clientIP) {
		writeError(w, "Too many requests", http.StatusTooManyRequests)
		return
	}
	var req oidcCallbackRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.FlowID == "" || req.Code == "" || req.State == "" {
		writeError(w, "flow_id, code, and state are required", http.StatusBadRequest)
		return
	}

	flow, err := loadAndConsumeOIDCFlow(req.FlowID)
	if err != nil {
		log.Printf("oidc callback from %s: %v", clientIP, err)
		writeError(w, "Login flow expired or invalid — please try again", http.StatusBadRequest)
		return
	}
	if req.State != flow.State {
		writeError(w, "State mismatch", http.StatusBadRequest)
		return
	}

	p, secret, err := loadOIDCProvider(flow.ProviderID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !p.Enabled {
		writeError(w, "OIDC provider is disabled", http.StatusForbidden)
		return
	}

	discovered, err := oidcDiscover(r.Context(), p.IssuerURL)
	if err != nil {
		writeError(w, "Could not reach OIDC provider", http.StatusBadGateway)
		return
	}
	oauth2Cfg := oidcOAuth2Config(p, secret, discovered, flow.RedirectURI)

	token, err := oauth2Cfg.Exchange(r.Context(), req.Code, oauth2.VerifierOption(flow.PKCEVerifier))
	if err != nil {
		log.Printf("oidc callback from %s: code exchange failed: %v", clientIP, err)
		writeError(w, "Could not complete login with provider", http.StatusBadGateway)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		writeError(w, "Provider did not return an ID token", http.StatusBadGateway)
		return
	}
	idToken, err := discovered.Verifier(&oidc.Config{ClientID: p.ClientID}).Verify(r.Context(), rawIDToken)
	if err != nil {
		log.Printf("oidc callback from %s: id_token verification failed: %v", clientIP, err)
		writeError(w, "Invalid ID token", http.StatusBadGateway)
		return
	}
	if idToken.Nonce != flow.Nonce {
		writeError(w, "Nonce mismatch", http.StatusBadRequest)
		return
	}

	issuer := idToken.Issuer
	subject := idToken.Subject
	if subject == "" {
		writeError(w, "Provider did not return a subject", http.StatusBadGateway)
		return
	}

	if flow.InviteToken != "" {
		oidcRedeemInvite(w, r, flow.InviteToken, issuer, subject)
		return
	}
	oidcLoginExisting(w, r, issuer, subject)
}

// oidcLoginExisting looks up an already-linked (issuer, subject) and issues
// a session if found — the "returning user" path, reached from the plain
// login page rather than an invite link.
func oidcLoginExisting(w http.ResponseWriter, r *http.Request, issuer, subject string) {
	var userID int
	var email, role string
	var disabled int
	err := db.QueryRow(
		`SELECT u.id, COALESCE(u.email,''), u.role, u.disabled
		 FROM user_identities i JOIN users u ON u.id = i.user_id
		 WHERE i.issuer_url=? AND i.subject=?`, issuer, subject,
	).Scan(&userID, &email, &role, &disabled)
	if err == sql.ErrNoRows {
		writeError(w, "No dansal account is linked to this identity — use an invite link first", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if disabled != 0 {
		writeError(w, "Account disabled", http.StatusForbidden)
		return
	}
	issueSessionResponse(w, r, http.StatusOK, userID, email, role, func() {
		writeError(w, "Internal server error", http.StatusInternalServerError)
	})
}

// oidcRedeemInvite creates a zero-PII account (no email, no display name —
// see useInvite's existing "passkey-only accounts are supported" precedent)
// bound to the given external identity, and consumes the invite. Mirrors
// webauthnInviteFinish's transaction shape.
func oidcRedeemInvite(w http.ResponseWriter, r *http.Request, inviteToken, issuer, subject string) {
	invite, ok := loadValidInviteOrError(w, inviteToken)
	if !ok {
		return
	}

	// An identity can only ever be linked to one account — if this
	// (issuer, subject) is already linked, this is a returning user who
	// followed a stale invite link, not a new signup.
	var existingUserID int
	err := db.QueryRow("SELECT user_id FROM user_identities WHERE issuer_url=? AND subject=?", issuer, subject).Scan(&existingUserID)
	if err == nil {
		writeError(w, "This identity is already linked to a dansal account — log in instead of using an invite", http.StatusConflict)
		return
	}
	if err != sql.ErrNoRows {
		writeInternalError(w, err)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		"INSERT INTO users (email, display_name, password_hash, role) VALUES (NULL, NULL, '', ?)",
		invite.Role,
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	userID, _ := result.LastInsertId()

	if _, err := tx.Exec(
		"INSERT INTO user_identities (user_id, issuer_url, subject) VALUES (?, ?, ?)",
		userID, issuer, subject,
	); err != nil {
		writeInternalError(w, err)
		return
	}

	if invite.OrgID.Valid {
		tx.Exec(
			"INSERT OR IGNORE INTO organization_members (organization_id, user_id) VALUES (?, ?)",
			invite.OrgID.Int64, userID,
		)
	}
	tx.Exec("UPDATE invite_links SET used_at=? WHERE id=?", time.Now().UTC().Unix(), invite.ID)

	if err := tx.Commit(); err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	log.Printf("oidc: new user id=%d (role=%s) registered via invite id=%d, issuer=%s", userID, invite.Role, invite.ID, issuer)

	issueSessionResponse(w, r, http.StatusCreated, int(userID), "", invite.Role, func() {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"status": "created"})
	})
}
