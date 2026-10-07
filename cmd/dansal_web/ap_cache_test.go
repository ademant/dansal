package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// #1471: concurrent AP requests for the same object share one API fetch
// (singleflight), and the result is cached briefly so a fan-out burst
// costs ~1 fetch per object per TTL window instead of one per request.

// resetAPCaches clears the package-level caches so one test's entries
// can't leak into another's assertions (slug/ID choices might otherwise
// collide across tests sharing the same binary run).
func resetAPCaches(t *testing.T) {
	t.Helper()
	apEventCache.Clear()
	apOutboxCache.Clear()
}

func apEventRequest(id int) *http.Request {
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/events/%d", id), nil)
	req.Header.Set("Accept", "application/activity+json")
	req.SetPathValue("id", strconv.Itoa(id))
	return req
}

func TestAPEventCacheCoalescesConcurrentRequests(t *testing.T) {
	resetAPCaches(t)
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release // hold every concurrent caller here until they've all arrived
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Event{ID: 101, Title: "Burst Event", StartTime: "2030-01-01T20:00:00Z", ChangedAt: "1700000000"})
	}))
	defer srv.Close()
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	h := eventHandler(&Config{Domain: "example.test"}, loadTemplates(), client, loadI18n(""))

	const n = 20
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h(rec, apEventRequest(101))
			codes[i] = rec.Code
		}(i)
	}
	// Give every goroutine a chance to reach the handler and start (or join)
	// the singleflight call before releasing the mock server's one response.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200", i, code)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream GetEvent called %d times for %d concurrent requests, want 1", got, n)
	}
}

func TestAPEventCacheServesTombstoneAndCachesIt(t *testing.T) {
	resetAPCaches(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	h := eventHandler(&Config{Domain: "example.test"}, loadTemplates(), client, loadI18n(""))

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h(rec, apEventRequest(202))
		if rec.Code != http.StatusGone {
			t.Fatalf("request %d: status = %d, want 410 (body=%s)", i, rec.Code, rec.Body.String())
		}
		var tomb APTombstone
		if err := json.Unmarshal(rec.Body.Bytes(), &tomb); err != nil {
			t.Fatalf("decode tombstone: %v", err)
		}
		if tomb.Type != "Tombstone" {
			t.Errorf("type = %q, want Tombstone", tomb.Type)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream called %d times for 3 sequential requests, want 1 (cached)", got)
	}
}

func TestAPEventCacheRefreshesAfterTTL(t *testing.T) {
	resetAPCaches(t)
	var changedAt atomic.Value
	changedAt.Store("1700000000")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Event{ID: 303, Title: "Edited Event", StartTime: "2030-01-01T20:00:00Z", ChangedAt: changedAt.Load().(string)})
	}))
	defer srv.Close()
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	h := eventHandler(&Config{Domain: "example.test"}, loadTemplates(), client, loadI18n(""))

	rec := httptest.NewRecorder()
	h(rec, apEventRequest(303))
	// changed_at surfaces as the ETag (`"id-changed_at"`), not in the Note
	// body itself.
	if !strings.Contains(rec.Header().Get("ETag"), "1700000000") {
		t.Fatalf("first fetch's ETag missing original changed_at: %q", rec.Header().Get("ETag"))
	}

	// Simulate the edit (a new changed_at) and force the cache entry to
	// have already expired, instead of sleeping past the real TTL.
	changedAt.Store("1800000000")
	if v, ok := apEventCache.Load(303); ok {
		entry := v.(apResponseEntry)
		entry.expiresAt = time.Now().Add(-time.Second)
		apEventCache.Store(303, entry)
	} else {
		t.Fatal("expected a cache entry after the first fetch")
	}

	rec2 := httptest.NewRecorder()
	h(rec2, apEventRequest(303))
	if !strings.Contains(rec2.Header().Get("ETag"), "1800000000") {
		t.Errorf("expired entry wasn't refetched, ETag: %q", rec2.Header().Get("ETag"))
	}
}

func TestAPEventCacheConditionalGetStill304s(t *testing.T) {
	resetAPCaches(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Event{ID: 404, Title: "Cond Event", StartTime: "2030-01-01T20:00:00Z", ChangedAt: "1700000000"})
	}))
	defer srv.Close()
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	h := eventHandler(&Config{Domain: "example.test"}, loadTemplates(), client, loadI18n(""))

	rec := httptest.NewRecorder()
	h(rec, apEventRequest(404))
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag on first response")
	}

	req2 := apEventRequest(404)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: status = %d, want 304", rec2.Code)
	}
}

func TestInvalidateAPEventCache(t *testing.T) {
	resetAPCaches(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Event{ID: 505, Title: "Invalidate Me", StartTime: "2030-01-01T20:00:00Z", ChangedAt: "1700000000"})
	}))
	defer srv.Close()
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	h := eventHandler(&Config{Domain: "example.test"}, loadTemplates(), client, loadI18n(""))

	h(httptest.NewRecorder(), apEventRequest(505))
	h(httptest.NewRecorder(), apEventRequest(505))
	if got := hits.Load(); got != 1 {
		t.Fatalf("precondition: want 1 hit before invalidation, got %d", got)
	}

	invalidateAPEventCache(505)
	h(httptest.NewRecorder(), apEventRequest(505))
	if got := hits.Load(); got != 2 {
		t.Errorf("after invalidation: want a fresh fetch (2 total), got %d", got)
	}
}

// ── Outbox ────────────────────────────────────────────────────────────────

func TestAPOutboxCacheCoalescesConcurrentRequests(t *testing.T) {
	resetAPCaches(t)
	// file::memory:?cache=shared, not plain ":memory:" -- the 20 concurrent
	// requests below each pull a connection from the pool, and a plain
	// ":memory:" DSN gives every physical connection its own separate,
	// empty database (#1466).
	db, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE actors (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		org_id INTEGER UNIQUE NOT NULL,
		org_slug TEXT UNIQUE NOT NULL,
		public_key_pem TEXT NOT NULL,
		private_key_pem TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO actors (org_id, org_slug, public_key_pem, private_key_pem) VALUES (0, 'relay', 'pub', 'priv')`); err != nil {
		t.Fatal(err)
	}

	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		w.Header().Set("X-Total-Count", "0")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Event{})
	}))
	defer srv.Close()
	cfg := &Config{Domain: "example.test", RelayActorName: "relay"}
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	h := outboxHandler(cfg, db, client)

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/org/relay/outbox", nil)
			req.SetPathValue("name", "relay")
			req.Header.Set("Accept", "application/activity+json")
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := hits.Load(); got != 1 {
		t.Errorf("upstream called %d times for %d concurrent outbox requests, want 1", got, n)
	}
}
