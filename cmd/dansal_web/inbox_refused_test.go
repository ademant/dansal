package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// #1662: a self-Delete whose sender key fetch is refused (401/403/404) is
// acknowledged with 202 but not processed; anything else stays 401, and the
// 410 gone-actor path is unchanged.

func refusingKeyServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	old := fedHTTPClient
	fedHTTPClient = srv.Client()
	t.Cleanup(func() { fedHTTPClient = old })
	return srv
}

func signedInboxPost(t *testing.T, actor string, activity map[string]any) *http.Request {
	t.Helper()
	_, priv, err := generateRSAKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(activity)
	req := httptest.NewRequest(http.MethodPost, "https://us.example/inbox", bytes.NewReader(body))
	req.Host = "us.example"
	if err := SignRequest(req, actor+"#main-key", priv, body); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestReadInboxActivityRefusedKey(t *testing.T) {
	for i, tc := range []struct {
		name       string
		status     int
		actType    string
		self       bool
		wantStatus int
		wantOK     bool
	}{
		{"403 self-Delete acked", http.StatusForbidden, "Delete", true, http.StatusAccepted, false},
		{"401 self-Delete acked", http.StatusUnauthorized, "Delete", true, http.StatusAccepted, false},
		{"404 self-Delete acked", http.StatusNotFound, "Delete", true, http.StatusAccepted, false},
		{"403 Delete of other object", http.StatusForbidden, "Delete", false, http.StatusUnauthorized, false},
		{"403 Create", http.StatusForbidden, "Create", true, http.StatusUnauthorized, false},
		{"410 self-Delete processed", http.StatusGone, "Delete", true, http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := refusingKeyServer(t, tc.status)
			actor := srv.URL + "/users/u" + strconv.Itoa(i)
			object := actor
			if !tc.self {
				object = srv.URL + "/statuses/1"
			}
			req := signedInboxPost(t, actor, map[string]any{"type": tc.actType, "actor": actor, "object": object})
			rec := httptest.NewRecorder()
			raw, ok := readInboxActivity(rec, req)
			if ok != tc.wantOK || rec.Code != tc.wantStatus {
				t.Fatalf("ok=%v status=%d, want ok=%v status=%d (body %s)", ok, rec.Code, tc.wantOK, tc.wantStatus, rec.Body)
			}
			if tc.wantOK && raw["type"] != "Delete" {
				t.Errorf("raw = %v", raw)
			}
		})
	}
}

func TestFetchActorPublicKeyRefusedIsTyped(t *testing.T) {
	withKeyFetchSigner(t)
	srv := refusingKeyServer(t, http.StatusForbidden)
	keyID := srv.URL + "/users/typed#main-key"
	for _, wantCached := range []bool{false, true} {
		_, _, err := fetchActorPublicKey(t.Context(), srv.Client(), keyID)
		var refused errKeyRefused
		if !errors.As(err, &refused) || refused.cached != wantCached {
			t.Fatalf("err = %#v, want errKeyRefused{cached:%v}", err, wantCached)
		}
	}
}
