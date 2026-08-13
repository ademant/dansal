package main

import (
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	_ "github.com/mattn/go-sqlite3"
)

// fakeOIDCProvider is a minimal spec-enough OIDC IdP for testing dansal's
// client flow end-to-end: discovery, authorization redirect, code exchange,
// and ID token verification against a real JWKS. It signs real RS256 JWTs
// with golang-jwt (already a direct dependency, used elsewhere for invite
// JWTs) rather than pulling in a second JOSE library just for tests.
type fakeOIDCProvider struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	kid    string
	issuer string
}

func newFakeOIDCProvider(t *testing.T, clientID string) *fakeOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDCProvider{key: key, kid: "test-key-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.issuer,
			"authorization_endpoint":                f.issuer + "/authorize",
			"token_endpoint":                        f.issuer + "/token",
			"jwks_uri":                              f.issuer + "/jwks.json",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"kid": f.kid,
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(bigEndianBytes(key.PublicKey.E)),
			}},
		})
	})
	// /authorize: a real IdP would show a login page; ours immediately
	// "authenticates" and redirects back with a code. The nonce dansal sent
	// is threaded through as the code itself (opaque to everyone but our own
	// /token handler below) so the ID token we mint at exchange time can
	// embed the exact nonce this flow used, without any shared test state.
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		redirectURI := q.Get("redirect_uri")
		state := q.Get("state")
		nonce := q.Get("nonce")
		if q.Get("client_id") != clientID {
			http.Error(w, "unknown client", http.StatusBadRequest)
			return
		}
		dest, _ := url.Parse(redirectURI)
		v := dest.Query()
		v.Set("code", "nonce:"+nonce)
		v.Set("state", state)
		dest.RawQuery = v.Encode()
		http.Redirect(w, r, dest.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		code := r.FormValue("code")
		nonce := strings.TrimPrefix(code, "nonce:")

		now := time.Now()
		claims := jwt.MapClaims{
			"iss":   f.issuer,
			"sub":   "test-subject-42",
			"aud":   clientID,
			"exp":   now.Add(time.Hour).Unix(),
			"iat":   now.Unix(),
			"nonce": nonce,
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = f.kid
		signed, err := tok.SignedString(f.key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// oauth2's token-response parser branches on Content-Type: anything
		// other than application/x-www-form-urlencoded/text-plain is parsed
		// as JSON, so this must be explicit (the default sniffed type isn't).
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"id_token":     signed,
		})
	})

	f.srv = httptest.NewServer(mux)
	f.issuer = f.srv.URL
	return f
}

func (f *fakeOIDCProvider) Close() { f.srv.Close() }

// bigEndianBytes returns n's minimal big-endian byte representation, as
// needed for a JWK's "e" (public exponent) field.
func bigEndianBytes(n int) []byte {
	var b []byte
	for n > 0 {
		b = append([]byte{byte(n & 0xff)}, b...)
		n >>= 8
	}
	if len(b) == 0 {
		b = []byte{0}
	}
	return b
}

// doHandler invokes an http.HandlerFunc directly against a fresh request/recorder
// and decodes a JSON response body into out (if non-nil).
func doHandler(t *testing.T, h http.HandlerFunc, method, target string, body []byte, out any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	w := httptest.NewRecorder()
	h(w, r)
	if out != nil && w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("decode response %q: %v", w.Body.String(), err)
		}
	}
	return w
}

// setupOIDCTestDB points the package-level db at a fresh in-memory instance
// (restored after the test) with an admin-created invite ready to redeem,
// and returns the invite token.
func setupOIDCTestDB(t *testing.T) (inviteToken string) {
	t.Helper()
	old := db
	t.Cleanup(func() { db = old })

	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	db = conn
	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()

	prevConfig := config
	config = &Config{}
	config.Server.BaseURL = "https://dansal.example.com"
	config.Server.InviteSigningKeyPath = filepath.Join(t.TempDir(), "invite_signing_key.pem")
	config.Server.InviteExpiryHours = 48
	t.Cleanup(func() { config = prevConfig })
	if err := loadOrGenerateInviteSigningKey(); err != nil {
		t.Fatalf("loadOrGenerateInviteSigningKey: %v", err)
	}

	loginRateLimiter = NewRateLimiter(1000, time.Minute)

	res, err := db.Exec("INSERT INTO users (email, display_name, password_hash, role) VALUES ('admin@example.com','Admin','x','admin')")
	if err != nil {
		t.Fatal(err)
	}
	adminID, _ := res.LastInsertId()
	inv, err := createInviteRecord(int(adminID), RoleUser, "link", nil)
	if err != nil {
		t.Fatal(err)
	}
	return inv.Token
}

// TestOIDCInviteRedemptionEndToEnd drives the full client flow against a
// real (in-process, self-signed) fake IdP: discovery, PKCE authorization
// redirect, code exchange, JWKS-verified ID token, and zero-PII account
// creation via an invite (#1095).
func TestOIDCInviteRedemptionEndToEnd(t *testing.T) {
	inviteToken := setupOIDCTestDB(t)

	const clientID = "dansal-test-client"
	idp := newFakeOIDCProvider(t, clientID)
	defer idp.Close()

	res, err := db.Exec(
		"INSERT INTO oidc_providers (issuer_url, client_id, client_secret, display_name, enabled) VALUES (?, ?, 'secret', 'Test IdP', 1)",
		idp.issuer, clientID,
	)
	if err != nil {
		t.Fatal(err)
	}
	providerID, _ := res.LastInsertId()

	// 1. Start the flow.
	startBody, _ := json.Marshal(oidcStartRequest{
		ProviderID:  int(providerID),
		RedirectURI: "https://dansal.example/oidc/callback",
		InviteToken: inviteToken,
	})
	var startResp struct {
		FlowID       string `json:"flow_id"`
		AuthorizeURL string `json:"authorize_url"`
	}
	w := doHandler(t, oidcStart, http.MethodPost, "/api/v1/oidc/start", startBody, &startResp)
	if w.Code != http.StatusOK {
		t.Fatalf("oidcStart status = %d, body = %s", w.Code, w.Body.String())
	}
	if startResp.FlowID == "" || startResp.AuthorizeURL == "" {
		t.Fatalf("oidcStart returned empty flow_id/authorize_url: %+v", startResp)
	}

	// 2. Follow the authorize URL against the fake IdP without following the
	// final redirect, exactly like a browser handing control back to dansal.
	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	authResp, err := httpClient.Get(startResp.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	defer authResp.Body.Close()
	if authResp.StatusCode != http.StatusFound {
		t.Fatalf("fake IdP /authorize status = %d", authResp.StatusCode)
	}
	loc, err := url.Parse(authResp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := loc.Query().Get("code")
	state := loc.Query().Get("state")
	if code == "" || state == "" {
		t.Fatalf("redirect missing code/state: %s", loc)
	}

	// 3. Complete the callback.
	cbBody, _ := json.Marshal(oidcCallbackRequest{FlowID: startResp.FlowID, Code: code, State: state})
	var cbResp struct {
		Token  string `json:"token"`
		UserID int    `json:"user_id"`
		Role   string `json:"role"`
		Email  string `json:"email"`
	}
	w = doHandler(t, oidcCallback, http.MethodPost, "/api/v1/oidc/callback", cbBody, &cbResp)
	if w.Code != http.StatusCreated {
		t.Fatalf("oidcCallback status = %d, body = %s", w.Code, w.Body.String())
	}
	if cbResp.Token == "" || cbResp.UserID == 0 || cbResp.Role != RoleUser {
		t.Fatalf("unexpected callback response: %+v", cbResp)
	}
	if cbResp.Email != "" {
		t.Fatalf("expected zero-PII account (no email), got %q", cbResp.Email)
	}

	// The account should be a genuine zero-PII row and linked in user_identities.
	var email sql.NullString
	var displayName sql.NullString
	if err := db.QueryRow("SELECT email, display_name FROM users WHERE id=?", cbResp.UserID).Scan(&email, &displayName); err != nil {
		t.Fatal(err)
	}
	if email.Valid || displayName.Valid {
		t.Fatalf("expected NULL email/display_name, got email=%v display_name=%v", email, displayName)
	}
	var linkedUserID int
	if err := db.QueryRow("SELECT user_id FROM user_identities WHERE issuer_url=? AND subject=?", idp.issuer, "test-subject-42").Scan(&linkedUserID); err != nil {
		t.Fatal(err)
	}
	if linkedUserID != cbResp.UserID {
		t.Fatalf("user_identities.user_id = %d, want %d", linkedUserID, cbResp.UserID)
	}

	// The invite must now be consumed.
	if isInviteUsable(inviteToken) {
		t.Fatal("invite should be consumed after redemption")
	}

	// 4. Returning-user login: same identity, no invite this time.
	startBody2, _ := json.Marshal(oidcStartRequest{
		ProviderID:  int(providerID),
		RedirectURI: "https://dansal.example/oidc/callback",
	})
	var startResp2 struct {
		FlowID       string `json:"flow_id"`
		AuthorizeURL string `json:"authorize_url"`
	}
	w = doHandler(t, oidcStart, http.MethodPost, "/api/v1/oidc/start", startBody2, &startResp2)
	if w.Code != http.StatusOK {
		t.Fatalf("second oidcStart status = %d, body = %s", w.Code, w.Body.String())
	}
	authResp2, err := httpClient.Get(startResp2.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	defer authResp2.Body.Close()
	loc2, _ := url.Parse(authResp2.Header.Get("Location"))
	cbBody2, _ := json.Marshal(oidcCallbackRequest{
		FlowID: startResp2.FlowID,
		Code:   loc2.Query().Get("code"),
		State:  loc2.Query().Get("state"),
	})
	var cbResp2 struct {
		Token  string `json:"token"`
		UserID int    `json:"user_id"`
	}
	w = doHandler(t, oidcCallback, http.MethodPost, "/api/v1/oidc/callback", cbBody2, &cbResp2)
	if w.Code != http.StatusOK {
		t.Fatalf("returning-login oidcCallback status = %d, body = %s", w.Code, w.Body.String())
	}
	if cbResp2.UserID != cbResp.UserID {
		t.Fatalf("returning login resolved to user_id=%d, want %d", cbResp2.UserID, cbResp.UserID)
	}
}
