package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func phase18DB(t *testing.T) {
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

	prev := config
	config = &Config{}
	t.Cleanup(func() { config = prev })
}

// seedRenewToken stores a hashed renew token and returns the raw value.
func seedRenewToken(t *testing.T, email string, ttl time.Duration) string {
	t.Helper()
	raw := "renew-raw-value-abc123"
	_, err := db.Exec(
		`INSERT INTO verified_email_session_renew_tokens (token_hash, email, expires_at) VALUES (?,?,?)`,
		sha256Hex(raw), email, time.Now().Unix()+int64(ttl.Seconds()),
	)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func countBoardSessions(t *testing.T, email string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM verified_email_sessions WHERE email=?`, email).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// pathTokenRequest builds a request that carries a wildcard path value. The
// handlers are tested directly rather than through a ServeMux, so the value
// that routing would normally populate has to be set by hand.
func pathTokenRequest(target, name, value string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target+"/"+value, nil)
	r.SetPathValue(name, value)
	return r
}

func headerTokenRequest(method, target, auth, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r.Header.Set("Authorization", auth)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func jsonTokenRequest(method, target, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// The renew token is the entire authorization, so all three ways of
// presenting it must mint a session identically.
func TestBoardSessionRenewAcceptsHeaderAndPath(t *testing.T) {
	const raw = "renew-raw-value-abc123"

	cases := []struct {
		name  string
		build func() *http.Request
	}{
		{"path", func() *http.Request {
			return pathTokenRequest("/api/v1/board-sessions/renew", "token", raw)
		}},
		{"bearer header", func() *http.Request {
			return headerTokenRequest(http.MethodPost, "/api/v1/board-sessions/renew", "Bearer "+raw, "")
		}},
		{"json body", func() *http.Request {
			return jsonTokenRequest(http.MethodPost, "/api/v1/board-sessions/renew", `{"token":"`+raw+`"}`)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phase18DB(t)
			seedRenewToken(t, "board@example.com", time.Hour)

			rec := httptest.NewRecorder()
			useBoardSessionRenewHandler(rec, c.build())

			if rec.Code != http.StatusOK {
				t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
			}
			var out map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out["token"] == "" {
				t.Error("no session token returned")
			}
			if n := countBoardSessions(t, "board@example.com"); n != 1 {
				t.Errorf("%d sessions created, want 1", n)
			}
		})
	}
}

// A token stays single-use across forms: spending it on one route must
// invalidate it for the others, or a leaked log entry is replayable.
func TestBoardSessionRenewTokenIsSingleUseAcrossForms(t *testing.T) {
	phase18DB(t)
	raw := seedRenewToken(t, "board@example.com", time.Hour)

	rec1 := httptest.NewRecorder()
	useBoardSessionRenewHandler(rec1, pathTokenRequest("/api/v1/board-sessions/renew", "token", raw))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first use: got %d, want 200", rec1.Code)
	}

	for _, r := range []*http.Request{
		headerTokenRequest(http.MethodPost, "/api/v1/board-sessions/renew", "Bearer "+raw, ""),
		jsonTokenRequest(http.MethodPost, "/api/v1/board-sessions/renew", `{"token":"`+raw+`"}`),
		pathTokenRequest("/api/v1/board-sessions/renew", "token", raw),
	} {
		rec := httptest.NewRecorder()
		useBoardSessionRenewHandler(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("replay via %s: got %d, want 401", r.Method, rec.Code)
		}
	}

	if n := countBoardSessions(t, "board@example.com"); n != 1 {
		t.Errorf("%d sessions after 4 attempts, want 1", n)
	}
}

// Session tokens and consumed-token responses must not be cached, and the
// token must not be echoed onward in Referer. nginx only guarantees
// strict-origin-when-cross-origin, which still leaks a same-origin path.
func TestTokenResponsesAreNoStoreAndNoReferrer(t *testing.T) {
	phase18DB(t)
	raw := seedRenewToken(t, "board@example.com", time.Hour)

	cases := []struct {
		name string
		req  *http.Request
		h    http.HandlerFunc
	}{
		{"renew path", pathTokenRequest("/api/v1/board-sessions/renew", "token", raw), useBoardSessionRenewHandler},
		{"renew header", headerTokenRequest(http.MethodPost, "/api/v1/board-sessions/renew", "Bearer "+raw, ""), useBoardSessionRenewHandler},
		{"renew json", jsonTokenRequest(http.MethodPost, "/api/v1/board-sessions/renew", `{"token":"`+raw+`"}`), useBoardSessionRenewHandler},
		{"booking verify", jsonTokenRequest(http.MethodPost, "/api/v1/bookings/verify", `{"token":"nope"}`), verifyBooking},
		{"booking checkin", httptest.NewRequest(http.MethodGet, "/api/v1/bookings/checkin/qr", nil), checkinBooking},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c.h(rec, c.req)
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control: got %q, want no-store", got)
			}
			if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy: got %q, want no-referrer", got)
			}
		})
	}
}

func TestRenewWithoutTokenIsBadRequest(t *testing.T) {
	phase18DB(t)
	rec := httptest.NewRecorder()
	useBoardSessionRenewHandler(rec, jsonTokenRequest(http.MethodPost, "/api/v1/board-sessions/renew", `{}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

// A malformed Authorization header must not be treated as "no credential
// supplied" in a way that silently falls through to a different path.
func TestBearerFromHeader(t *testing.T) {
	cases := []struct{ header, want string }{
		{"", ""},
		{"Bearer abc", "abc"},
		{"Basic abc", ""},
		{"Bearer", ""},
		{"Bearer a b", ""},
		{"bearer abc", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/x", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := bearerFromHeader(r); got != c.want {
			t.Errorf("bearerFromHeader(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}
