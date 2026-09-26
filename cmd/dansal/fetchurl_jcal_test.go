package main

// jCal import (#1377): dansal already emits jCal (icalTextToJCal, jcal.go)
// and already accepts a jCal POST body (jcalToICalText); this closes the
// loop by reading it back as a fetch-source type. Since there is no known
// external jCal producer to test against yet, these tests use dansal's own
// jCal output as the fixture — exactly the format an admin pointing one
// dansal instance's fetch sources at another dansal instance would receive.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestFetchTypeHeaders(t *testing.T) {
	if h := fetchTypeHeaders("jcal"); h["Accept"] != "application/calendar+json" {
		t.Fatalf("jcal headers = %v, want Accept: application/calendar+json", h)
	}
	for _, typ := range []string{"ical", "json", "rss", "kufer", "folkdance-json", "gancio-json", ""} {
		if h := fetchTypeHeaders(typ); h != nil {
			t.Errorf("fetchTypeHeaders(%q) = %v, want nil", typ, h)
		}
	}
}

func TestValidFetchTypeJcal(t *testing.T) {
	if !validFetchType("jcal") {
		t.Fatal("validFetchType(\"jcal\") = false, want true")
	}
}

func TestDetectFetchTypeJcal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/calendar+json; charset=utf-8")
	}))
	defer srv.Close()

	old := safeClient
	safeClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { safeClient = old })

	if got := detectFetchType(srv.URL); got != "jcal" {
		t.Fatalf("detectFetchType() = %q, want %q", got, "jcal")
	}
}

// jcalFixtureFromDansal builds a jCal document the exact way dansal's own
// GET /api/v1/events (Accept: application/calendar+json) would: through
// buildEventsCalendar + icalTextToJCal, per the user's suggestion to use
// dansal's own jCal as the reference producer rather than a hand-written one.
func jcalFixtureFromDansal(t *testing.T, events []Event) []byte {
	t.Helper()
	jcal, err := icalTextToJCal(buildEventsCalendar(events).Serialize())
	if err != nil {
		t.Fatalf("icalTextToJCal: %v", err)
	}
	return jcal
}

func TestParseBodyToRequestsJcal(t *testing.T) {
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	// Truncated to whole seconds: iCal's DTSTART/DTEND carry only second
	// precision, so an untruncated start/end would never round-trip equal.
	start := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	end := start.Add(2 * time.Hour)
	jcal := jcalFixtureFromDansal(t, []Event{{
		ID:        4242,
		Title:     "Fest Noz de Test",
		StartTime: start.Format(time.RFC3339),
		EndTime:   end.Format(time.RFC3339),
		Location:  &Location{Location: "Salle des fêtes"},
	}})

	reqs, err := parseBodyToRequests(jcal, FetchSource{Type: "jcal", URL: "https://example.org/events"})
	if err != nil {
		t.Fatalf("parseBodyToRequests: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Title != "Fest Noz de Test" {
		t.Errorf("Title = %q", req.Title)
	}
	if req.UID != "event-4242@go-calendar" {
		t.Errorf("UID = %q", req.UID)
	}
	if req.Location.Location != "Salle des fêtes" {
		t.Errorf("Location = %q", req.Location.Location)
	}
	gotStart, err := time.Parse(time.RFC3339, req.StartTime)
	if err != nil || !gotStart.Equal(start) {
		t.Errorf("StartTime = %q (parsed %v), want instant %v", req.StartTime, gotStart, start)
	}
}

// TestImportFromJcalSourceEndToEnd exercises the full fetch path: a "jcal"
// FetchSource is fetched with the Accept: application/calendar+json header
// (fetchTypeHeaders/getWithRetry), converted back to iCal text, and imported
// through the same dedup/merge machinery as an "ical" source. The fixture
// server behaves like a content-negotiated dansal instance — it only serves
// jCal when asked for it — so a missing Accept header would make the test
// fail on the JSON-decode step, proving the header actually gets sent.
// Running the import twice and asserting the row count stays at 1 covers the
// issue's "survives repeated adminFetchAll runs without duplicating"
// acceptance criterion (via the UID-based dedup tier).
func TestImportFromJcalSourceEndToEnd(t *testing.T) {
	setupDedupTestDB(t)

	start := time.Now().Add(45 * 24 * time.Hour).UTC()
	end := start.Add(3 * time.Hour)
	jcal := jcalFixtureFromDansal(t, []Event{{
		ID:        7,
		Title:     "Bal Folk Import Test",
		StartTime: start.Format(time.RFC3339),
		EndTime:   end.Format(time.RFC3339),
	}})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/calendar+json" {
			// What a content-negotiated dansal instance would send a caller
			// that didn't ask for jCal: its default JSON event-array shape,
			// not jCal — parsing this as jCal would fail.
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":7,"title":"wrong representation"}]`))
			return
		}
		w.Header().Set("Content-Type", "application/calendar+json")
		w.Write(jcal)
	}))
	defer srv.Close()

	old := safeClient
	safeClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { safeClient = old })

	src := FetchSource{Type: "jcal", URL: srv.URL}
	events, counts, err := importFromSource(context.Background(), src)
	if err != nil {
		t.Fatalf("importFromSource: %v", err)
	}
	if counts.New != 1 || len(events) != 1 {
		t.Fatalf("counts = %+v, events = %d, want 1 new event", counts, len(events))
	}
	if events[0].Title != "Bal Folk Import Test" {
		t.Errorf("Title = %q", events[0].Title)
	}

	// Re-fetch: the UID tier must recognise the same event and not duplicate it.
	if _, _, err := importFromSource(context.Background(), src); err != nil {
		t.Fatalf("second importFromSource: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM events WHERE title = ?", "Bal Folk Import Test").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("event count after re-import = %d, want 1 (dedup must have matched by UID)", n)
	}
}
