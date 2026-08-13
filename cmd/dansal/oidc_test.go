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
	"strconv"
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

// authedRequest builds a request carrying the X-User-ID/X-User-Role headers
// TokenMiddleware would normally set, for calling authenticated handlers
// directly in tests (mirrors doHandler but with caller identity attached).
func authedRequest(t *testing.T, method, target string, body []byte, userID int, role string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("X-User-ID", strconv.Itoa(userID))
	r.Header.Set("X-User-Role", role)
	return r
}

// TestOIDCLinkExistingAccount drives the link flow (#1096) for an
// already-authenticated user, and the unlink + self-lockout guards that go
// with it.
func TestOIDCLinkExistingAccount(t *testing.T) {
	setupOIDCTestDB(t)

	const clientID = "dansal-link-client"
	idp := newFakeOIDCProvider(t, clientID)
	defer idp.Close()

	res, err := db.Exec(
		"INSERT INTO oidc_providers (issuer_url, client_id, client_secret, display_name, enabled) VALUES (?, ?, 'secret', 'Link IdP', 1)",
		idp.issuer, clientID,
	)
	if err != nil {
		t.Fatal(err)
	}
	providerID, _ := res.LastInsertId()

	// A password-holding user wants to add SSO as a second login method.
	res, err = db.Exec("INSERT INTO users (email, display_name, password_hash, role) VALUES ('existing@example.com','Existing','somehash','user')")
	if err != nil {
		t.Fatal(err)
	}
	userIDVal, _ := res.LastInsertId()
	userID := int(userIDVal)

	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}

	runLinkFlow := func(t *testing.T, forUserID int) *httptest.ResponseRecorder {
		startBody, _ := json.Marshal(oidcLinkStartRequest{
			ProviderID:  int(providerID),
			RedirectURI: "https://dansal.example/settings/oidc/callback",
		})
		var startResp struct {
			FlowID       string `json:"flow_id"`
			AuthorizeURL string `json:"authorize_url"`
		}
		w := httptest.NewRecorder()
		oidcLinkStart(w, authedRequest(t, http.MethodPost, "/api/v1/oidc/link-start", startBody, forUserID, RoleUser))
		if w.Code != http.StatusOK {
			t.Fatalf("oidcLinkStart status = %d, body = %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &startResp); err != nil {
			t.Fatal(err)
		}

		authResp, err := httpClient.Get(startResp.AuthorizeURL)
		if err != nil {
			t.Fatal(err)
		}
		defer authResp.Body.Close()
		loc, err := url.Parse(authResp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}

		cbBody, _ := json.Marshal(oidcCallbackRequest{
			FlowID: startResp.FlowID,
			Code:   loc.Query().Get("code"),
			State:  loc.Query().Get("state"),
		})
		return doHandler(t, oidcCallback, http.MethodPost, "/api/v1/oidc/callback", cbBody, nil)
	}

	// 1. Link succeeds and the identity is attached to the caller's own account.
	w := runLinkFlow(t, userID)
	if w.Code != http.StatusCreated {
		t.Fatalf("link callback status = %d, body = %s", w.Code, w.Body.String())
	}
	var linkedUserID int
	if err := db.QueryRow("SELECT user_id FROM user_identities WHERE issuer_url=? AND subject=?", idp.issuer, "test-subject-42").Scan(&linkedUserID); err != nil {
		t.Fatal(err)
	}
	if linkedUserID != userID {
		t.Fatalf("linked to user_id=%d, want %d", linkedUserID, userID)
	}

	// 2. Listing shows the identity.
	w = httptest.NewRecorder()
	listUserOIDCIdentities(w, authedRequest(t, http.MethodGet, "/api/v1/user/oidc-identities", nil, userID, RoleUser))
	var identities []UserIdentityInfo
	if err := json.Unmarshal(w.Body.Bytes(), &identities); err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 {
		t.Fatalf("expected 1 linked identity, got %d", len(identities))
	}

	// 3. A second account cannot link the very same (issuer, subject).
	res, err = db.Exec("INSERT INTO users (email, display_name, password_hash, role) VALUES ('other@example.com','Other','otherhash','user')")
	if err != nil {
		t.Fatal(err)
	}
	otherUserIDVal, _ := res.LastInsertId()
	w = runLinkFlow(t, int(otherUserIDVal))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 linking an already-linked identity to a different account, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Unlink succeeds: the user still has a password afterwards.
	w = httptest.NewRecorder()
	r := authedRequest(t, http.MethodDelete, "/api/v1/user/oidc-identities/"+strconv.Itoa(int(identities[0].ID)), nil, userID, RoleUser)
	r.SetPathValue("id", strconv.Itoa(int(identities[0].ID)))
	deleteUserOIDCIdentity(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("unlink status = %d, body = %s", w.Code, w.Body.String())
	}

	// 5. Self-lockout guard: link again, then strip the password so the
	// identity is the account's only login method — unlink must now be
	// refused.
	w = runLinkFlow(t, userID)
	if w.Code != http.StatusCreated {
		t.Fatalf("re-link status = %d, body = %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec("UPDATE users SET password_hash='' WHERE id=?", userID); err != nil {
		t.Fatal(err)
	}
	var rowID int64
	if err := db.QueryRow("SELECT rowid FROM user_identities WHERE user_id=?", userID).Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r = authedRequest(t, http.MethodDelete, "/api/v1/user/oidc-identities/"+strconv.Itoa(int(rowID)), nil, userID, RoleUser)
	r.SetPathValue("id", strconv.Itoa(int(rowID)))
	deleteUserOIDCIdentity(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 unlinking last login method, got %d: %s", w.Code, w.Body.String())
	}
}

// TestChangeOwnPasswordRemoval covers the "clear my password" path (#1096):
// allowed once another login method exists, refused when it would leave the
// account with none.
func TestChangeOwnPasswordRemoval(t *testing.T) {
	setupOIDCTestDB(t)

	pwHash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec("INSERT INTO users (email, display_name, password_hash, role) VALUES ('pw@example.com','PW','" + pwHash + "','user')")
	if err != nil {
		t.Fatal(err)
	}
	userIDVal, _ := res.LastInsertId()
	userID := int(userIDVal)

	// No other login method yet — clearing the password must be refused.
	body, _ := json.Marshal(map[string]string{"old_password": "correct horse battery staple", "new_password": ""})
	w := httptest.NewRecorder()
	changeOwnPassword(w, authedRequest(t, http.MethodPost, "/api/v1/user/password", body, userID, RoleUser))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 clearing password with no other login method, got %d: %s", w.Code, w.Body.String())
	}

	// Add a passkey credential directly (register ceremony is out of scope
	// here — only its presence matters for the guard).
	if _, err := db.Exec(
		"INSERT INTO webauthn_credentials (user_id, credential_id, public_key, sign_count, aaguid, flags) VALUES (?, 'cred1', X'00', 0, X'00', 0)",
		userID,
	); err != nil {
		t.Fatal(err)
	}

	w = httptest.NewRecorder()
	changeOwnPassword(w, authedRequest(t, http.MethodPost, "/api/v1/user/password", body, userID, RoleUser))
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 clearing password with a passkey present, got %d: %s", w.Code, w.Body.String())
	}
	var hash string
	if err := db.QueryRow("SELECT password_hash FROM users WHERE id=?", userID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		t.Fatalf("password_hash = %q, want empty after removal", hash)
	}
}

// fakeMastodonInstance is a minimal stand-in for a Mastodon instance's
// plain-OAuth2 API (#1097): /api/v1/instance for the reachability check,
// /oauth/authorize + /oauth/token for the code exchange, and
// /api/v1/accounts/verify_credentials for identity resolution — deliberately
// with no discovery document or ID token, unlike fakeOIDCProvider.
type fakeMastodonInstance struct {
	srv     *httptest.Server
	baseURL string
	acctID  string
	// appsMode controls how POST /api/v1/apps (#1098 self-service
	// auto-registration) responds: "" (default)/"multi" always succeeds
	// regardless of how many redirect_uris are sent; "single" rejects a
	// request carrying more than one; "fail" always rejects. Read at
	// request time, so it can be changed after the server starts.
	appsMode string
}

func newFakeMastodonInstance(t *testing.T, clientID string) *fakeMastodonInstance {
	t.Helper()
	f := &fakeMastodonInstance{acctID: "mastodon-acct-99"}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/instance", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"uri": f.baseURL})
	})
	mux.HandleFunc("/api/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		redirectURIs := r.FormValue("redirect_uris")
		switch f.appsMode {
		case "fail":
			http.Error(w, "registration disabled", http.StatusForbidden)
			return
		case "single":
			if strings.Contains(redirectURIs, "\n") {
				http.Error(w, "only one redirect_uri supported", http.StatusUnprocessableEntity)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"client_id":     "auto-client-id",
			"client_secret": "auto-client-secret",
		})
	})
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != clientID {
			http.Error(w, "unknown client", http.StatusBadRequest)
			return
		}
		dest, _ := url.Parse(q.Get("redirect_uri"))
		v := dest.Query()
		v.Set("code", "test-mastodon-code")
		v.Set("state", q.Get("state"))
		dest.RawQuery = v.Encode()
		http.Redirect(w, r, dest.String(), http.StatusFound)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-mastodon-access-token",
			"token_type":   "Bearer",
		})
	})
	mux.HandleFunc("/api/v1/accounts/verify_credentials", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-mastodon-access-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": f.acctID, "username": "testuser"})
	})

	f.srv = httptest.NewServer(mux)
	f.baseURL = f.srv.URL
	return f
}

func (f *fakeMastodonInstance) Close() { f.srv.Close() }

// TestMastodonOAuth2LoginAndLink drives the Mastodon-kind provider (#1097)
// through the same three paths its OIDC counterpart already covers: invite
// redemption, returning login, and linking to an existing account — proving
// oidcCallback's kind branch produces the same (issuer, subject) contract
// downstream regardless of which protocol produced it.
func TestMastodonOAuth2LoginAndLink(t *testing.T) {
	inviteToken := setupOIDCTestDB(t)

	const clientID = "dansal-mastodon-client"
	instance := newFakeMastodonInstance(t, clientID)
	defer instance.Close()

	res, err := db.Exec(
		"INSERT INTO oidc_providers (kind, issuer_url, client_id, client_secret, display_name, enabled) VALUES ('mastodon', ?, ?, 'secret', 'Test Mastodon', 1)",
		instance.baseURL, clientID,
	)
	if err != nil {
		t.Fatal(err)
	}
	providerID, _ := res.LastInsertId()

	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	runFlow := func(t *testing.T, r *http.Request) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		oidcStart(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("oidcStart status = %d, body = %s", w.Code, w.Body.String())
		}
		var startResp struct {
			FlowID       string `json:"flow_id"`
			AuthorizeURL string `json:"authorize_url"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &startResp); err != nil {
			t.Fatal(err)
		}
		authResp, err := httpClient.Get(startResp.AuthorizeURL)
		if err != nil {
			t.Fatal(err)
		}
		defer authResp.Body.Close()
		loc, err := url.Parse(authResp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		cbBody, _ := json.Marshal(oidcCallbackRequest{
			FlowID: startResp.FlowID,
			Code:   loc.Query().Get("code"),
			State:  loc.Query().Get("state"),
		})
		return doHandler(t, oidcCallback, http.MethodPost, "/api/v1/oidc/callback", cbBody, nil)
	}

	// 1. Invite redemption creates a zero-PII account linked via Mastodon.
	startBody, _ := json.Marshal(oidcStartRequest{
		ProviderID:  int(providerID),
		RedirectURI: "https://dansal.example/oidc/callback",
		InviteToken: inviteToken,
	})
	w := runFlow(t, httptest.NewRequest(http.MethodPost, "/api/v1/oidc/start", strings.NewReader(string(startBody))))
	if w.Code != http.StatusCreated {
		t.Fatalf("mastodon invite redemption status = %d, body = %s", w.Code, w.Body.String())
	}
	var cbResp struct {
		UserID int    `json:"user_id"`
		Email  string `json:"email"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cbResp); err != nil {
		t.Fatal(err)
	}
	if cbResp.Email != "" {
		t.Fatalf("expected zero-PII account, got email %q", cbResp.Email)
	}
	var linkedUserID int
	if err := db.QueryRow("SELECT user_id FROM user_identities WHERE issuer_url=? AND subject=?", instance.baseURL, instance.acctID).Scan(&linkedUserID); err != nil {
		t.Fatal(err)
	}
	if linkedUserID != cbResp.UserID {
		t.Fatalf("linked user_id = %d, want %d", linkedUserID, cbResp.UserID)
	}

	// 2. Returning login resolves to the same account, no invite needed.
	startBody2, _ := json.Marshal(oidcStartRequest{
		ProviderID:  int(providerID),
		RedirectURI: "https://dansal.example/oidc/callback",
	})
	w = runFlow(t, httptest.NewRequest(http.MethodPost, "/api/v1/oidc/start", strings.NewReader(string(startBody2))))
	if w.Code != http.StatusOK {
		t.Fatalf("mastodon returning login status = %d, body = %s", w.Code, w.Body.String())
	}
	var cbResp2 struct {
		UserID int `json:"user_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cbResp2); err != nil {
		t.Fatal(err)
	}
	if cbResp2.UserID != cbResp.UserID {
		t.Fatalf("returning login resolved to user_id=%d, want %d", cbResp2.UserID, cbResp.UserID)
	}
}

// TestMastodonAutoRegisterSuccess drives the #1098 self-service flow when
// the instance accepts both redirect_uris in one call: the provider is
// created with real credentials and link_enabled stays true.
func TestMastodonAutoRegisterSuccess(t *testing.T) {
	setupOIDCTestDB(t)
	instance := newFakeMastodonInstance(t, "")
	defer instance.Close()

	res, err := db.Exec("INSERT INTO users (email, display_name, password_hash, role) VALUES ('admin2@example.com','Admin2','x','admin')")
	if err != nil {
		t.Fatal(err)
	}
	adminID, _ := res.LastInsertId()

	body, _ := json.Marshal(map[string]any{
		"kind":         "mastodon",
		"issuer_url":   instance.baseURL,
		"display_name": "Auto Mastodon",
	})
	w := httptest.NewRecorder()
	createOIDCProvider(w, authedRequest(t, http.MethodPost, "/api/v1/oidc/providers", body, int(adminID), RoleAdmin))
	if w.Code != http.StatusCreated {
		t.Fatalf("createOIDCProvider status = %d, body = %s", w.Code, w.Body.String())
	}
	var p OIDCProvider
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.ClientID != "auto-client-id" {
		t.Fatalf("ClientID = %q, want auto-client-id", p.ClientID)
	}
	if !p.LinkEnabled {
		t.Fatal("expected LinkEnabled=true when both redirect_uris are accepted")
	}
	var storedSecret string
	if err := db.QueryRow("SELECT client_secret FROM oidc_providers WHERE id=?", p.ID).Scan(&storedSecret); err != nil {
		t.Fatal(err)
	}
	if storedSecret != "auto-client-secret" {
		t.Fatalf("stored client_secret = %q, want auto-client-secret", storedSecret)
	}
}

// TestMastodonAutoRegisterSingleURIFallback drives the case where the
// instance rejects multiple redirect_uris: dansal retries with just the
// login/invite URL and marks the provider link_enabled=false.
func TestMastodonAutoRegisterSingleURIFallback(t *testing.T) {
	setupOIDCTestDB(t)
	instance := newFakeMastodonInstance(t, "")
	instance.appsMode = "single"
	defer instance.Close()

	res, err := db.Exec("INSERT INTO users (email, display_name, password_hash, role) VALUES ('admin3@example.com','Admin3','x','admin')")
	if err != nil {
		t.Fatal(err)
	}
	adminID, _ := res.LastInsertId()

	body, _ := json.Marshal(map[string]any{
		"kind":         "mastodon",
		"issuer_url":   instance.baseURL,
		"display_name": "Single URI Mastodon",
	})
	w := httptest.NewRecorder()
	createOIDCProvider(w, authedRequest(t, http.MethodPost, "/api/v1/oidc/providers", body, int(adminID), RoleAdmin))
	if w.Code != http.StatusCreated {
		t.Fatalf("createOIDCProvider status = %d, body = %s", w.Code, w.Body.String())
	}
	var p OIDCProvider
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.LinkEnabled {
		t.Fatal("expected LinkEnabled=false after single-URI fallback")
	}
	if p.ClientID != "auto-client-id" {
		t.Fatalf("ClientID = %q, want auto-client-id", p.ClientID)
	}
}

// TestMastodonAutoRegisterTotalFailure drives the case where the instance
// refuses self-service registration outright: the reserved provider row is
// rolled back so retrying (or falling back to manual credentials) doesn't
// leave orphaned rows with incrementing ids behind.
func TestMastodonAutoRegisterTotalFailure(t *testing.T) {
	setupOIDCTestDB(t)
	instance := newFakeMastodonInstance(t, "")
	instance.appsMode = "fail"
	defer instance.Close()

	res, err := db.Exec("INSERT INTO users (email, display_name, password_hash, role) VALUES ('admin4@example.com','Admin4','x','admin')")
	if err != nil {
		t.Fatal(err)
	}
	adminID, _ := res.LastInsertId()

	body, _ := json.Marshal(map[string]any{
		"kind":         "mastodon",
		"issuer_url":   instance.baseURL,
		"display_name": "Failing Mastodon",
	})
	w := httptest.NewRecorder()
	createOIDCProvider(w, authedRequest(t, http.MethodPost, "/api/v1/oidc/providers", body, int(adminID), RoleAdmin))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("createOIDCProvider status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM oidc_providers WHERE display_name='Failing Mastodon'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected reserved row to be rolled back, found %d rows", count)
	}
}
