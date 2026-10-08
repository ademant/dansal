package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// feedTestServer returns a DansalClient backed by a fake API that always
// answers /api/v1/events with the given events, regardless of query string.
func feedTestServer(t *testing.T, events []Event) *DansalClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	}))
	t.Cleanup(srv.Close)
	return &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
}

func feedICalRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/feed/events.ics?lang=en", nil)
	r.SetPathValue("format", "ics")
	return r
}

// feedVEventBlock extracts one event's VEVENT block from a serialized iCal
// body by its UID, so assertions about that event's own properties don't
// accidentally match a neighboring VEVENT.
func feedVEventBlock(t *testing.T, body string, eventID int) string {
	t.Helper()
	start := strings.Index(body, fmt.Sprintf("UID:event-%d@", eventID))
	if start < 0 {
		t.Fatalf("VEVENT for event %d not found:\n%s", eventID, body)
	}
	end := strings.Index(body[start:], "END:VEVENT")
	if end < 0 {
		t.Fatalf("END:VEVENT not found after event %d's UID:\n%s", eventID, body)
	}
	return body[start : start+end]
}

// #1475: VEVENT carries DTSTAMP/LAST-MODIFIED/SEQUENCE/STATUS, and a
// cancelled event reports STATUS:CANCELLED instead of silently disappearing
// or looking identical to a confirmed one.
func TestFeedICalEventChangeMetadata(t *testing.T) {
	events := []Event{
		{ID: 1, Title: "Confirmed Bal", StartTime: "2030-06-01T20:00:00Z", EndTime: "2030-06-01T23:00:00Z",
			CreatedAt: "2026-01-01 10:00:00", ChangedAt: "2026-01-01T10:00:00Z"},
		{ID: 2, Title: "Cancelled Bal", StartTime: "2030-06-02T20:00:00Z", EndTime: "2030-06-02T23:00:00Z",
			CreatedAt: "2026-01-01 10:00:00", ChangedAt: "2026-01-01T10:00:00Z", IsCancelled: true},
	}
	client := feedTestServer(t, events)
	h := feedMainHandler(&Config{Domain: "example.test"}, nil, client, loadI18n(""))

	rec := httptest.NewRecorder()
	h(rec, feedICalRequest())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{"DTSTAMP:", "LAST-MODIFIED:", "SEQUENCE:", "STATUS:CONFIRMED", "STATUS:CANCELLED"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in iCal body:\n%s", want, body)
		}
	}

	// The cancelled event's own VEVENT block must say CANCELLED, not just
	// the feed containing the word somewhere.
	cancelledBlock := feedVEventBlock(t, body, 2)
	if !strings.Contains(cancelledBlock, "STATUS:CANCELLED") {
		t.Errorf("cancelled event's own VEVENT missing STATUS:CANCELLED, block:\n%s", cancelledBlock)
	}
	confirmedBlock := feedVEventBlock(t, body, 1)
	if !strings.Contains(confirmedBlock, "STATUS:CONFIRMED") {
		t.Errorf("confirmed event's own VEVENT missing STATUS:CONFIRMED, block:\n%s", confirmedBlock)
	}
}

// #1475: a repeated request for an unchanged feed gets 304 via either
// If-None-Match or If-Modified-Since, so an unchanged poll costs a few
// hundred bytes instead of the full feed.
func TestFeedICalConditionalGet304(t *testing.T) {
	events := []Event{
		{ID: 1, Title: "Bal", StartTime: "2030-06-01T20:00:00Z", EndTime: "2030-06-01T23:00:00Z",
			CreatedAt: "2026-01-01 10:00:00", ChangedAt: "2026-01-01T10:00:00Z"},
	}
	client := feedTestServer(t, events)
	h := feedMainHandler(&Config{Domain: "example.test"}, nil, client, loadI18n(""))

	rec := httptest.NewRecorder()
	h(rec, feedICalRequest())
	etag := rec.Header().Get("ETag")
	lastMod := rec.Header().Get("Last-Modified")
	if etag == "" || lastMod == "" {
		t.Fatalf("missing ETag/Last-Modified on first response: etag=%q last-modified=%q", etag, lastMod)
	}

	t.Run("If-None-Match", func(t *testing.T) {
		r := feedICalRequest()
		r.Header.Set("If-None-Match", etag)
		rec2 := httptest.NewRecorder()
		h(rec2, r)
		if rec2.Code != http.StatusNotModified {
			t.Errorf("status = %d, want 304", rec2.Code)
		}
	})

	t.Run("If-Modified-Since", func(t *testing.T) {
		r := feedICalRequest()
		r.Header.Set("If-Modified-Since", lastMod)
		rec2 := httptest.NewRecorder()
		h(rec2, r)
		if rec2.Code != http.StatusNotModified {
			t.Errorf("status = %d, want 304", rec2.Code)
		}
	})
}

// #1475: editing an event (a newer changed_at) changes the feed's ETag, so
// a client that only revalidates via If-None-Match still sees the update.
func TestFeedICalETagChangesWithEvent(t *testing.T) {
	cfg := &Config{Domain: "example.test"}

	before := feedTestServer(t, []Event{
		{ID: 1, Title: "Bal", StartTime: "2030-06-01T20:00:00Z", EndTime: "2030-06-01T23:00:00Z",
			CreatedAt: "2026-01-01 10:00:00", ChangedAt: "2026-01-01T10:00:00Z"},
	})
	rec1 := httptest.NewRecorder()
	feedMainHandler(cfg, nil, before, loadI18n(""))(rec1, feedICalRequest())
	etag1 := rec1.Header().Get("ETag")

	after := feedTestServer(t, []Event{
		{ID: 1, Title: "Bal (time changed)", StartTime: "2030-06-01T21:00:00Z", EndTime: "2030-06-02T00:00:00Z",
			CreatedAt: "2026-01-01 10:00:00", ChangedAt: "2026-02-01T10:00:00Z"},
	})
	rec2 := httptest.NewRecorder()
	feedMainHandler(cfg, nil, after, loadI18n(""))(rec2, feedICalRequest())
	etag2 := rec2.Header().Get("ETag")

	if etag1 == "" || etag2 == "" {
		t.Fatalf("missing ETag: before=%q after=%q", etag1, etag2)
	}
	if etag1 == etag2 {
		t.Errorf("ETag unchanged after editing the event's changed_at: %q", etag1)
	}

	// The second request didn't send If-None-Match, so it must not 304.
	if rec2.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec2.Code)
	}
}

// #1475: a genuine edit (changed_at well after created_at) prepends a
// visible "Updated: <date>" marker to DESCRIPTION, since Webcal has no push
// notification for subscribers to otherwise notice the change.
func TestFeedICalUpdatedMarker(t *testing.T) {
	events := []Event{
		{ID: 1, Title: "Freshly created", Description: "original text", StartTime: "2030-06-01T20:00:00Z", EndTime: "2030-06-01T23:00:00Z",
			CreatedAt: "2026-01-01 10:00:00", ChangedAt: "2026-01-01T10:00:00Z"},
		{ID: 2, Title: "Edited later", Description: "original text", StartTime: "2030-06-02T20:00:00Z", EndTime: "2030-06-02T23:00:00Z",
			CreatedAt: "2026-01-01 10:00:00", ChangedAt: "2026-03-01T10:00:00Z"},
	}
	client := feedTestServer(t, events)
	h := feedMainHandler(&Config{Domain: "example.test"}, nil, client, loadI18n(""))

	rec := httptest.NewRecorder()
	h(rec, feedICalRequest())
	body := rec.Body.String()

	freshBlock := feedVEventBlock(t, body, 1)
	editedBlock := feedVEventBlock(t, body, 2)

	if strings.Contains(freshBlock, "DESCRIPTION:Last updated") {
		t.Errorf("freshly created event should not carry an update marker, block:\n%s", freshBlock)
	}
	if !strings.Contains(editedBlock, "Last updated") {
		t.Errorf("edited event missing update marker, block:\n%s", editedBlock)
	}
}
