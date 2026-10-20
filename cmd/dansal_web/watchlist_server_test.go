package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	wlTestIP   = "192.0.2.1"
	wlUUID     = "11111111-2222-3333-4444-555555555555"
	wlOtherUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

// wlTestToken issues a form token old enough to pass the guard's minimum age.
func wlTestToken(t *testing.T) string {
	t.Helper()
	id := issueFormToken(wlTestIP)
	if id == "" {
		t.Fatal("could not issue a form token")
	}
	v, ok := oneTimeTokens.Load(id)
	if !ok {
		t.Fatal("token not stored")
	}
	tok := v.(oneTimeToken)
	tok.createdAt = time.Now().Add(-5 * time.Second)
	oneTimeTokens.Store(id, tok)
	return id
}

// wlTestEnv opens a fresh web DB and returns it with the save handler and a
// helper that posts a form to it. The create limit is raised so tests don't
// trip it; the create-limit test sets its own.
func wlTestEnv(t *testing.T) (*sql.DB, func(url.Values) *httptest.ResponseRecorder) {
	t.Helper()
	db := initDB(filepath.Join(t.TempDir(), "web.db"))
	watchlistCreateThrottle = newSubmissionThrottle(100, time.Hour)
	watchlistLoadThrottle = newSubmissionThrottle(100, time.Minute)
	save := watchlistSaveHandler(&Config{Domain: "example.test"}, db)
	post := func(form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/watchlist/save", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = wlTestIP + ":1234"
		rec := httptest.NewRecorder()
		save(rec, req)
		return rec
	}
	return db, post
}

func wlForm(tok, uuid, key, ids string) url.Values {
	v := url.Values{"_form_token": {tok}, "dansal_phone2": {""}, "uuid": {uuid}, "ids": {ids}}
	if key != "" {
		v.Set("key", key)
	}
	return v
}

func wlLoad(db *sql.DB, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/watchlist/load", nil)
	req.Header.Set("X-Watchlist-Key", key)
	req.RemoteAddr = wlTestIP + ":1234"
	rec := httptest.NewRecorder()
	watchlistLoadHandler(db)(rec, req)
	return rec
}

func wlKeyFrom(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("create reply not JSON: %v (%s)", err, rec.Body.String())
	}
	if !watchlistKeyRe.MatchString(out["key"]) {
		t.Fatalf("create returned key %q, want 32 lower-case hex chars", out["key"])
	}
	return out["key"]
}

func TestWatchlistServerCreateUpdateLoad(t *testing.T) {
	db, post := wlTestEnv(t)

	rec := post(wlForm(wlTestToken(t), wlUUID, "", "12,45"))
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}
	key := wlKeyFrom(t, rec)

	if rec := post(wlForm(wlTestToken(t), wlUUID, key, "12,45,78")); rec.Code != http.StatusOK {
		t.Fatalf("update: status %d body %s", rec.Code, rec.Body.String())
	}
	if lrec := wlLoad(db, key); lrec.Code != http.StatusOK || !strings.Contains(lrec.Body.String(), `"ids":[12,45,78]`) {
		t.Fatalf("load after update: status %d body %s", lrec.Code, lrec.Body.String())
	}
}

func TestWatchlistServerCreateReplacesOldKey(t *testing.T) {
	db, post := wlTestEnv(t)
	first := wlKeyFrom(t, post(wlForm(wlTestToken(t), wlUUID, "", "12")))
	second := wlKeyFrom(t, post(wlForm(wlTestToken(t), wlUUID, "", "99")))
	if first == second {
		t.Fatal("a new create must issue a new key")
	}
	if rec := wlLoad(db, first); rec.Code != http.StatusNotFound {
		t.Errorf("old key still loads: status %d", rec.Code)
	}
	if rec := wlLoad(db, second); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ids":[99]`) {
		t.Errorf("new key: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestWatchlistServerUpdateNeedsMatchingUUIDAndKey(t *testing.T) {
	_, post := wlTestEnv(t)
	key := wlKeyFrom(t, post(wlForm(wlTestToken(t), wlUUID, "", "12")))
	rec := post(wlForm(wlTestToken(t), wlOtherUID, key, "1"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("another browser's UUID with this key: status %d, want 404", rec.Code)
	}
}

func TestWatchlistServerRejectsMissingToken(t *testing.T) {
	_, post := wlTestEnv(t)
	rec := post(wlForm("not-a-token", wlUUID, "", "12"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("bad token: status %d, want 403", rec.Code)
	}
}

func TestWatchlistServerHoneypotStoresNothing(t *testing.T) {
	db, post := wlTestEnv(t)
	form := wlForm(wlTestToken(t), wlUUID, "", "12")
	form.Set("dansal_phone2", "bot")
	rec := post(form)
	if rec.Code != http.StatusOK {
		t.Fatalf("honeypot: status %d, want the same 200 as a real save", rec.Code)
	}
	if fake := wlKeyFrom(t, rec); fake == "" {
		t.Error("honeypot should return a key-shaped value")
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM watchlists").Scan(&n)
	if n != 0 {
		t.Errorf("honeypot stored %d row(s), want 0", n)
	}
}

func TestWatchlistServerCreateIsRateLimited(t *testing.T) {
	_, post := wlTestEnv(t)
	watchlistCreateThrottle = newSubmissionThrottle(2, time.Hour)
	for i := 0; i < 2; i++ {
		if rec := post(wlForm(wlTestToken(t), wlUUID, "", "12")); rec.Code != http.StatusOK {
			t.Fatalf("create %d: status %d", i+1, rec.Code)
		}
	}
	if rec := post(wlForm(wlTestToken(t), wlUUID, "", "12")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("third create: status %d, want 429", rec.Code)
	}
}

func TestWatchlistServerRejectsOversizedList(t *testing.T) {
	_, post := wlTestEnv(t)
	ids := make([]string, watchlistMaxIDs+1)
	for i := range ids {
		ids[i] = "1"
	}
	rec := post(wlForm(wlTestToken(t), wlUUID, "", strings.Join(ids, ",")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("%d ids: status %d, want 400", len(ids), rec.Code)
	}
}

func TestWatchlistRetentionSweep(t *testing.T) {
	db, _ := wlTestEnv(t)
	now := time.Now()
	insert := func(uuidHash, keyHash string, age time.Duration) {
		db.Exec(`INSERT INTO watchlists (uuid_hash, key_hash, ids_json, updated_at) VALUES (?, ?, '[1]', ?)`,
			uuidHash, keyHash, now.Add(-age).Unix())
	}
	insert("old", "oldkey", 400*24*time.Hour)    // over 12 months
	insert("fresh", "freshkey", 30*24*time.Hour) // a month old

	sweepWatchlists(db)

	var n int
	db.QueryRow("SELECT COUNT(*) FROM watchlists").Scan(&n)
	if n != 1 {
		t.Fatalf("after sweep: %d rows, want 1 (the fresh one)", n)
	}
	db.QueryRow("SELECT COUNT(*) FROM watchlists WHERE uuid_hash = 'fresh'").Scan(&n)
	if n != 1 {
		t.Error("the fresh list was removed")
	}
}
