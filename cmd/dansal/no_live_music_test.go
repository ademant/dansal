package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1612: "no live music" is a per-event flag. It is stored, returned in the
// event list, and refused together with listed musicians.

func TestNoLiveMusicFlagIsStoredAndListed(t *testing.T) {
	setupDedupTestDB(t)

	recorded, _, _, err := insertEvent(db, EventInput{
		Title: "Recorded evening", StartTime: 2000000000, EndTime: 2000003600,
		IsPublished: true, NoLiveMusic: true,
	})
	if err != nil {
		t.Fatalf("insert recorded: %v", err)
	}
	live, _, _, err := insertEvent(db, EventInput{
		Title: "Live evening", StartTime: 2000086400, EndTime: 2000090000,
		IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert live: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/events", nil)
	w := httptest.NewRecorder()
	getEvents(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var events []struct {
		ID          int  `json:"id"`
		NoLiveMusic bool `json:"no_live_music"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	got := map[int]bool{}
	for _, e := range events {
		got[e.ID] = e.NoLiveMusic
	}
	if !got[recorded] {
		t.Errorf("event %d: no_live_music = false, want true", recorded)
	}
	if got[live] {
		t.Errorf("event %d: no_live_music = true, want false", live)
	}
}

// The suggest endpoint refuses "no live music" together with musicians before it
// touches the database, so a stale form or an API client cannot store the
// contradiction.
func TestSuggestRefusesNoLiveMusicWithMusicians(t *testing.T) {
	initSuggestRateLimiters()
	body := `{"title":"Session","no_live_music":true,"musicians":["Some Band"]}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/suggestions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	suggestHandler(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), errNoLiveMusicWithMusicians.Error()) {
		t.Errorf("body %q does not explain the refusal", w.Body.String())
	}
}
