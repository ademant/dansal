package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTimetableEntryStartEnd(t *testing.T) {
	eventStart := "2026-09-15T18:00:00+02:00"

	// Same-day entry, no entry_date of its own: falls back to the event's
	// own start date, inheriting its offset.
	e := TimetableEntry{StartTime: "19:00", EndTime: "20:30"}
	start, end, ok := timetableEntryStartEnd(e, eventStart)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got := start.Format("2006-01-02T15:04:05-07:00"); got != "2026-09-15T19:00:00+02:00" {
		t.Fatalf("start = %s", got)
	}
	if got := end.Format("2006-01-02T15:04:05-07:00"); got != "2026-09-15T20:30:00+02:00" {
		t.Fatalf("end = %s", got)
	}

	// Crosses midnight: end <= start means end is the next day.
	e2 := TimetableEntry{StartTime: "22:00", EndTime: "02:00"}
	start2, end2, ok2 := timetableEntryStartEnd(e2, eventStart)
	if !ok2 {
		t.Fatal("expected ok=true")
	}
	if !end2.After(start2) {
		t.Fatalf("expected end after start for a midnight-crossing entry, got start=%v end=%v", start2, end2)
	}
	if end2.Sub(start2).Hours() != 4 {
		t.Fatalf("expected a 4h span, got %v", end2.Sub(start2))
	}

	// Its own entry_date overrides the event's start date (multi-day festival).
	e3 := TimetableEntry{StartTime: "10:00", EndTime: "11:00", EntryDate: "2026-09-17"}
	start3, _, ok3 := timetableEntryStartEnd(e3, eventStart)
	if !ok3 {
		t.Fatal("expected ok=true")
	}
	if got := start3.Format("2006-01-02"); got != "2026-09-17" {
		t.Fatalf("expected entry_date to override the event's own date, got %s", got)
	}

	// Malformed clock values: not ok, not a crash.
	if _, _, ok := timetableEntryStartEnd(TimetableEntry{StartTime: "bad", EndTime: "11:00"}, eventStart); ok {
		t.Fatal("expected ok=false for a malformed start_time")
	}
}

func TestFilterTimetableEntries(t *testing.T) {
	entries := []TimetableEntry{{ID: 1}, {ID: 2}, {ID: 3}}

	if got := filterTimetableEntries(entries, ""); len(got) != 3 {
		t.Fatalf("empty filter should return everything unchanged, got %d", len(got))
	}
	got := filterTimetableEntries(entries, "1, 3, 99")
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("expected entries 1 and 3 (99 doesn't exist, ignored), got %+v", got)
	}
	if got := filterTimetableEntries(entries, "nonsense"); len(got) != 0 {
		t.Fatalf("expected no matches for unparseable ids, got %d", len(got))
	}
}

// TestFeedEventTimetableICSHandler exercises the actual HTTP handler
// end-to-end (#1177): unfiltered returns every entry's VEVENT, ?entries=
// filters down to just the starred selection, and an unpublished event 404s
// like the whole-event .ics export already does.
func TestFeedEventTimetableICSHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/events/1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Event{
			ID: 1, Title: "Festival", StartTime: "2026-09-15T18:00:00+02:00", IsPublished: true,
			Timetable: []TimetableEntry{
				{ID: 10, Title: "Opening bal", StartTime: "18:00", EndTime: "19:00"},
				{ID: 11, Title: "Workshop", StartTime: "19:30", EndTime: "20:30"},
			},
		})
	})
	mux.HandleFunc("/api/v1/events/2", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Event{ID: 2, Title: "Draft", IsPublished: false})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	cfg := &Config{Domain: "example.test"}
	h := feedEventTimetableICSHandler(cfg, client)

	render := func(id, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/events/"+id+"/timetable.ics?"+query, nil)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := render("1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unfiltered: status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Count(body, "BEGIN:VEVENT") != 2 {
		t.Fatalf("expected 2 VEVENTs unfiltered, body:\n%s", body)
	}

	rec = render("1", "entries=11")
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered: status=%d body=%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if strings.Count(body, "BEGIN:VEVENT") != 1 || !strings.Contains(body, "SUMMARY:Workshop") {
		t.Fatalf("expected exactly the starred entry's VEVENT, body:\n%s", body)
	}

	rec = render("2", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unpublished event: expected 404, got %d", rec.Code)
	}
}

// TestFeedRouterTimetableICSPrecedence guards the ordering feed.go's comment
// calls out: "/events/{id}/timetable.ics" and "/events/{id}.ics" both match
// strings.HasSuffix(p, ".ics"), so the more specific timetable case must be
// checked first in feedRouter's switch or every timetable.ics request would
// silently 404 (id would resolve to "123/timetable", not "123").
func TestFeedRouterTimetableICSPrecedence(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/events/1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Event{
			ID: 1, Title: "Festival", StartTime: "2026-09-15T18:00:00+02:00", IsPublished: true,
			Timetable: []TimetableEntry{{ID: 10, Title: "Opening bal", StartTime: "18:00", EndTime: "19:00"}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	cfg := &Config{Domain: "example.test"}

	dbConn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dbConn.Close()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("expected feedRouter to handle %s itself, fell through to next instead", r.URL.Path)
	})
	handler := feedRouter(cfg, dbConn, client)(next)

	req := httptest.NewRequest(http.MethodGet, "/events/1/timetable.ics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "SUMMARY:Opening bal") {
		t.Fatalf("expected the timetable VEVENT, got:\n%s", rec.Body.String())
	}
}
