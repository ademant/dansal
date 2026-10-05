package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// #1432: sender-key fetches are signed with the relay actor key, refused
// fetches (401/403) are cached for an hour, and a slow key server can't hold
// the inbox request past keyFetchTimeout.

func TestSignGETRequestVerifies(t *testing.T) {
	pub, priv, err := generateRSAKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://remote.example/users/alice", nil)
	if err := SignGETRequest(req, "https://us.example/relay#main-key", priv); err != nil {
		t.Fatal(err)
	}
	req.Host = "remote.example"
	if !strings.Contains(req.Header.Get("Signature"), `headers="(request-target) host date"`) {
		t.Fatalf("Signature = %q", req.Header.Get("Signature"))
	}
	if err := VerifyRequest(req, pub); err != nil {
		t.Fatalf("own signature doesn't verify: %v", err)
	}
}

func withKeyFetchSigner(t *testing.T) (pub string) {
	t.Helper()
	pub, priv, err := generateRSAKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	old := keyFetchSigner.Load()
	setKeyFetchSigner("https://us.example/relay#main-key", priv)
	t.Cleanup(func() { keyFetchSigner.Store(old) })
	return pub
}

func TestFetchActorPublicKeySignsTheFetch(t *testing.T) {
	ourPub := withKeyFetchSigner(t)
	theirPub, _, _ := generateRSAKeyPair()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Authorized fetch: refuse anything that isn't validly signed by us.
		if !strings.Contains(r.Header.Get("Signature"), `keyId="https://us.example/relay#main-key"`) || VerifyRequest(r, ourPub) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"publicKey": map[string]string{"owner": "https://remote/alice", "publicKeyPem": theirPub}})
	}))
	defer srv.Close()

	pem, owner, err := fetchActorPublicKey(context.Background(), srv.Client(), srv.URL+"/users/alice#main-key")
	if err != nil || pem != theirPub || owner != "https://remote/alice" {
		t.Fatalf("fetch = %q, %q, %v", pem, owner, err)
	}
}

func TestFetchActorPublicKeyCachesRefusal(t *testing.T) {
	withKeyFetchSigner(t)
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	keyID := srv.URL + "/users/suspended#main-key"
	for i := 0; i < 3; i++ {
		if _, _, err := fetchActorPublicKey(context.Background(), srv.Client(), keyID); err == nil {
			t.Fatal("expected an error for a refused fetch")
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("remote fetched %d times, want 1 (403 cached)", n)
	}
}

func TestFetchActorPublicKeyTimesOut(t *testing.T) {
	old := keyFetchTimeout
	keyFetchTimeout = 100 * time.Millisecond
	defer func() { keyFetchTimeout = old }()
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	if _, _, err := fetchActorPublicKey(context.Background(), srv.Client(), srv.URL+"/users/slow#main-key"); err == nil {
		t.Fatal("expected a timeout error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("fetch took %v — keyFetchTimeout not applied", d)
	}
}

func TestLegacyGancioInboxAccepts(t *testing.T) {
	rec := httptest.NewRecorder()
	legacyGancioInboxHandler(rec, httptest.NewRequest(http.MethodPost, "/federation/u/relay/inbox", strings.NewReader(`{"type":"Announce"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
}
