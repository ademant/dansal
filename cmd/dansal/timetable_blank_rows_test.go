package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Blank timetable rows (#1558): a row with no content at all is dropped, so an
// event type alone is enough for a simple event. A row with some content but no
// title is still rejected, because it's a real mistake.

func TestDropBlankTimetableRows(t *testing.T) {
	reqs := []TimetableEntryRequest{
		{},
		{StartTime: "19:00", EndTime: "23:00", Title: "Ball"},
		{Title: ""},
		{Room: "Saal"},
	}
	got := dropBlankTimetableRows(reqs)
	if len(got) != 2 {
		t.Fatalf("kept %d rows, want 2: %+v", len(got), got)
	}
	if got[0].Title != "Ball" || got[1].Room != "Saal" {
		t.Errorf("kept the wrong rows: %+v", got)
	}
}

func TestPartialTimetableRowStillRejected(t *testing.T) {
	reqs := dropBlankTimetableRows([]TimetableEntryRequest{{StartTime: "19:00"}})
	if err := validateTimetableRequests(reqs); err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Fatalf("want 'title is required' for a row with a time but no title, got %v", err)
	}
}

func TestReadTimetableBodyAcceptsBlankFirstRow(t *testing.T) {
	body := `[{},{"title":"Talk","start_time":"10:00","end_time":"11:00"}]`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/events/1/timetable", strings.NewReader(body))
	w := httptest.NewRecorder()
	reqs, ok := readTimetableBody(w, r)
	if !ok {
		t.Fatalf("array with a blank first row was rejected: %d %s", w.Code, w.Body.String())
	}
	if len(reqs) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(reqs))
	}
	if kept := dropBlankTimetableRows(reqs); len(kept) != 1 || kept[0].Title != "Talk" {
		t.Errorf("after dropping blanks: %+v", kept)
	}
}

func TestReadTimetableBodyAcceptsSingleObject(t *testing.T) {
	body := `{"title":"Ball","start_time":"19:00","end_time":"23:00"}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/events/1/timetable", strings.NewReader(body))
	w := httptest.NewRecorder()
	reqs, ok := readTimetableBody(w, r)
	if !ok || len(reqs) != 1 || reqs[0].Title != "Ball" {
		t.Fatalf("single object: ok=%v reqs=%+v (%d %s)", ok, reqs, w.Code, w.Body.String())
	}
}

func TestReadTimetableBodyRejectsEmptyArray(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/events/1/timetable", strings.NewReader(`[]`))
	w := httptest.NewRecorder()
	if _, ok := readTimetableBody(w, r); ok || w.Code != http.StatusBadRequest {
		t.Fatalf("empty array should stay a 400, got ok=%v code=%d", ok, w.Code)
	}
}
