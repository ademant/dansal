package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// #1614: the org actor document is cached per slug for apActorCacheTTL, and an
// organisation save clears it.
func TestAPActorCache(t *testing.T) {
	invalidateAPActorCache()
	t.Cleanup(invalidateAPActorCache)

	if _, ok := getAPActorBody("balfolk"); ok {
		t.Fatal("empty cache returned a body")
	}

	putAPActorBody("balfolk", []byte(`{"id":"x"}`))
	body, ok := getAPActorBody("balfolk")
	if !ok || string(body) != `{"id":"x"}` {
		t.Fatalf("cached body = %q, ok=%v", body, ok)
	}

	apActorCache.Store("stale", apActorEntry{body: []byte("old"), expires: time.Now().Add(-time.Second)})
	if _, ok := getAPActorBody("stale"); ok {
		t.Error("expired entry was served")
	}

	putAPActorBody("other", []byte("y"))
	invalidateAPActorCache()
	if _, ok := getAPActorBody("balfolk"); ok {
		t.Error("invalidate kept an entry")
	}
	if _, ok := getAPActorBody("other"); ok {
		t.Error("invalidate kept an entry")
	}
}

// A cached actor is written as ActivityPub JSON, as before the cache.
func TestWriteAPActorBodyContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAPActorBody(rec, []byte(`{"type":"Group"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/activity+json" {
		t.Errorf("Content-Type = %q", ct)
	}
}
