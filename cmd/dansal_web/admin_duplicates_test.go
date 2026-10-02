package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// #1427: duplicate comparison page.

func dupTestSetup(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)
}

func TestDupSideBuildingAndRooms(t *testing.T) {
	building, room := 1, 3
	locs := []Location{
		{ID: 1, Location: "Kulturhaus", Town: "Freiburg"},
		{ID: 2, Location: "Großer Saal", ParentID: &building},
		{ID: 3, Location: "Kleiner Saal", ParentID: &building},
	}
	s := dupSide("a", Event{ID: 7, LocationID: &room, StartTime: "2026-10-06T20:00:00+02:00", EndTime: "2026-10-07T01:00:00+02:00", FetchSourceID: 4}, locs, "", "")
	if s.BuildingID != 1 || s.BuildingName != "Kulturhaus, Freiburg" || s.LocationID != 3 || len(s.Rooms) != 2 {
		t.Errorf("side = %+v", s)
	}
	if s.Date != "2026-10-06" || s.Start != "20:00" || s.End != "01:00" || s.EndDate != "2026-10-07" {
		t.Errorf("times = %s %s-%s %s", s.Date, s.Start, s.End, s.EndDate)
	}
	if s.SourceLabel != "feed #4" || s.SourceLink != "/admin/fetchurls/4/edit" {
		t.Errorf("source = %q %q", s.SourceLabel, s.SourceLink)
	}
	if got := shiftDate("2026-10-08", "2026-10-06", "2026-03-10"); got != "2026-03-12" {
		t.Errorf("shiftDate = %s", got)
	}
}

func TestAdminDuplicatePageRenders(t *testing.T) {
	dupTestSetup(t)
	building := 1
	locs := []Location{{ID: 1, Location: "Salle Testville", Town: "Testville"}, {ID: 2, Location: "Petite salle", ParentID: &building}}
	a := Event{ID: 11, Title: "Bal et Atelier", StartTime: "2026-10-06T20:00:00+02:00", LocationID: &building, Tags: []string{"bal-folk"}}
	b := Event{ID: 10, Title: "Bal de Testville", StartTime: "2026-10-06T20:30:00+02:00", LocationID: &building}
	req := withSessionUser(httptest.NewRequest(http.MethodGet, "/admin/duplicates/11", nil), &SessionUser{ID: 1, Role: "admin"})
	rec := httptest.NewRecorder()
	renderTemplate(rec, loadTemplates().adminDuplicate, tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), "test", AdminDuplicateData{
		A: dupSide("a", a, locs, "", ""), B: dupSide("b", b, locs, "", "Bal hebdo"), Time: true, Venue: true,
	}))
	body := rec.Body.String()
	checkInlineJS(t, body)
	for _, want := range []string{
		`id="dup-save" class="btn-primary" disabled`,
		`action="/admin/events/merge"`, `name="keep_id" value="11" form="dup-merge-form"`,
		`action="/admin/duplicates/11/accept"`, `action="/admin/events/11/delete"`, `action="/admin/events/10/delete"`,
		`name="a_loc"`, `<option value="2">Petite salle</option>`, `Bal hebdo`, `class="dup-reason"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %s", want)
		}
	}
	if strings.Contains(body, `"dup_`) || strings.Contains(body, ">dup_") {
		t.Error("untranslated dup_* key on page")
	}
}

// fakeAPI records requests and serves canned events for the save/merge tests.
type fakeAPI struct {
	mu     sync.Mutex
	calls  []string
	events map[string]Event
	check  DuplicateCheck
}

func (f *fakeAPI) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+string(body))
	f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/duplicate-check"):
		json.NewEncoder(w).Encode(f.check)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/events/"):
		json.NewEncoder(w).Encode(f.events[strings.TrimPrefix(r.URL.Path, "/api/v1/events/")])
	case r.Method == http.MethodPatch || r.Method == http.MethodPut:
		json.NewEncoder(w).Encode(Event{})
	case r.Method == http.MethodDelete || strings.HasSuffix(r.URL.Path, "/assign-events"):
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Write([]byte(`[]`))
	}
}

func (f *fakeAPI) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestAdminDuplicateSave(t *testing.T) {
	f := &fakeAPI{check: DuplicateCheck{Flagged: false}}
	api := httptest.NewServer(http.HandlerFunc(f.handler))
	defer api.Close()
	h := adminDuplicateSaveHandler(&DansalClient{BaseURL: api.URL, HTTP: api.Client()})
	form := url.Values{
		"a_id": {"11"}, "a_date": {"2026-03-10"}, "a_odate": {"2026-10-06"}, "a_start": {"20:00"}, "a_ostart": {"20:00"},
		"a_end": {"01:00"}, "a_oend": {"01:00"}, "a_oenddate": {"2026-10-07"}, "a_loc": {"1"}, "a_oloc": {"1"},
		"b_id": {"10"}, "b_date": {"2026-10-06"}, "b_odate": {"2026-10-06"}, "b_start": {"20:30"}, "b_ostart": {"20:30"},
		"b_end": {"23:30"}, "b_oend": {"23:30"}, "b_loc": {"2"}, "b_oloc": {"1"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/duplicates/11/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "11")
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
	rec := httptest.NewRecorder()
	h(rec, req)
	if loc := rec.Header().Get("Location"); loc != "/admin/events?flagged=1" {
		t.Errorf("redirect = %q (%d)", loc, rec.Code)
	}
	// A: new date, overnight end keeps its +1 day; B: moved into room 2.
	if !f.has(`PATCH /api/v1/events/11 {"end_time":"2026-03-11T01:00:00","start_time":"2026-03-10T20:00:00"}`) {
		t.Errorf("A times not patched as expected: %q", f.calls)
	}
	if !f.has(`PATCH /api/v1/events/10 {"location_id":2}`) || f.has(`PATCH /api/v1/events/10 {"end_time"`) {
		t.Errorf("B location/time patch wrong: %q", f.calls)
	}
}

func TestAdminMergeKeepsChosenEventAndSeries(t *testing.T) {
	series := 5
	f := &fakeAPI{events: map[string]Event{
		"10": {ID: 10, Title: "Bal (from feed)", ChangedBy: "fetch", ChangedAt: "200"},
		"11": {ID: 11, Title: "Bal hebdo", SeriesID: &series},
	}}
	api := httptest.NewServer(http.HandlerFunc(f.handler))
	defer api.Close()
	db, _ := sql.Open("sqlite3", ":memory:")
	defer db.Close()
	h := adminEventMergeHandler(&Config{}, db, &DansalClient{BaseURL: api.URL, HTTP: api.Client()})
	form := url.Values{"event_ids": {"11", "10"}, "keep_id": {"10"}, "flagged": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/events/merge", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
	h(httptest.NewRecorder(), req)
	if !f.has("PUT /api/v1/events/10") || !f.has("DELETE /api/v1/events/11") || f.has("DELETE /api/v1/events/10") {
		t.Errorf("keep_id not honoured: %q", f.calls)
	}
	if !f.has(`POST /api/v1/series/5/assign-events {"ids":[10]}`) {
		t.Errorf("merged event did not keep the series: %q", f.calls)
	}
}

func TestDuplicateEntryPointsRender(t *testing.T) {
	dupTestSetup(t)
	partner := 10
	req := withSessionUser(httptest.NewRequest(http.MethodGet, "/admin/events/11/edit", nil), &SessionUser{ID: 1, Role: "admin"})
	rec := httptest.NewRecorder()
	renderTemplate(rec, loadTemplates().adminEventForm, tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), "test", AdminEventFormData{
		Event: Event{ID: 11, NeedsDuplicateReview: true, DuplicateOfID: &partner},
	}))
	body := rec.Body.String()
	checkInlineJS(t, body)
	for _, want := range []string{`id="dup-note"`, `href="/admin/duplicates/11"`, `id="dup-check-btn"`, `id="dup-check-dialog"`} {
		if !strings.Contains(body, want) {
			t.Errorf("edit form missing %s", want)
		}
	}
	rec = httptest.NewRecorder()
	renderTemplate(rec, loadTemplates().adminEvents, tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), "test", AdminEventsData{
		Events: []Event{{ID: 11, Title: "Bal", NeedsDuplicateReview: true, DuplicateOfID: &partner, EmailVerified: true}},
	}))
	if !strings.Contains(rec.Body.String(), `href="/admin/duplicates/11" class="dup-solve-link"`) {
		t.Error("admin events list: Solve link missing")
	}
}
