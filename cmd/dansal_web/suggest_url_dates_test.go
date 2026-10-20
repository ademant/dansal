package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #1417: typed date vs. the date(s) a source URL names.

func TestStartDates(t *testing.T) {
	got := startDates([]PreviewEvent{
		{StartTime: "2026-10-03T10:00:00+02:00"},
		{StartTime: "2026-10-03T20:00:00+02:00"}, // same day: once
		{StartTime: "2026-09-12T19:30:00+02:00"},
		{StartTime: "not a date"},
	})
	if strings.Join(got, ",") != "2026-09-12,2026-10-03" {
		t.Errorf("startDates = %q", got)
	}
}

func TestSuggestURLDatesHandler(t *testing.T) {
	var gotURL string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events/suggest-preview" {
			http.NotFound(w, r)
			return
		}
		r.ParseMultipartForm(1 << 20)
		gotURL = r.FormValue("url")
		json.NewEncoder(w).Encode([]PreviewEvent{{Title: "Journée baroque", StartTime: "2026-10-03T10:00:00+02:00"}})
	}))
	defer api.Close()
	publicThrottle = newSubmissionThrottleForget(100, time.Minute, time.Hour)
	h := suggestURLDatesHandler(&Config{SMTPHost: "smtp.example.test"}, &DansalClient{BaseURL: api.URL, HTTP: api.Client()})

	get := func(q string) urlDatesResponse {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/events/suggest/url-dates?url="+q, nil))
		var res urlDatesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
		}
		return res
	}
	if res := get("https%3A%2F%2Fpretix.example%2Fbaroque%2F"); strings.Join(res.Dates, ",") != "2026-10-03" || gotURL != "https://pretix.example/baroque/" {
		t.Errorf("dates = %q, forwarded url = %q", res.Dates, gotURL)
	}
	// Non-http(s) input never reaches the API and yields an empty list.
	gotURL = ""
	if res := get("javascript%3Aalert(1)"); len(res.Dates) != 0 || gotURL != "" {
		t.Errorf("non-http url: dates = %q, forwarded = %q", res.Dates, gotURL)
	}
}

// The source date is captured only in import mode — on the manage link the
// prefill is the event's own stored date, not an independent source.
func TestSuggestSourceDateOnlyInImportMode(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)
	render := func(data SuggestPageData) string {
		rec := httptest.NewRecorder()
		renderTemplate(rec, loadTemplates().suggestEvent, tmplData(httptest.NewRequest(http.MethodGet, "/events/suggest", nil), &Config{Domain: "example.test"}, loadI18n(""), "test", data))
		body := rec.Body.String()
		checkInlineJS(t, body)
		return body
	}
	if b := render(SuggestPageData{IsImportMode: true}); !strings.Contains(b, "if (true && window.sgSetSourceDates)") || strings.Count(b, `class="sg-src-date-warn date-warn-note" hidden`) != 2 {
		t.Error("import mode: source-date capture or warning elements missing")
	}
	if b := render(SuggestPageData{ManageToken: "tok"}); !strings.Contains(b, "if (false && window.sgSetSourceDates)") {
		t.Error("manage link must not treat its stored date as a source date")
	}
}

// #1559: the website check reports why it found no dates, so the form can
// say what happened. Only no_events and unreachable show a note; the rest is
// silent. The API answers 422 for an unparseable body and 502 when it could
// not fetch the URL (suggestPreviewHandler).
func TestSuggestURLDatesStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
		dates  string
	}{
		{"events found", http.StatusOK, `[{"title":"Ball","start_time":"2026-10-03T20:00:00+02:00"}]`, urlDatesOK, "2026-10-03"},
		{"page without event data", http.StatusUnprocessableEntity, `{"error":"no machine-readable events"}`, urlDatesNoEvents, ""},
		{"empty event list", http.StatusOK, `[]`, urlDatesNoEvents, ""},
		{"fetch failed", http.StatusBadGateway, `{"error":"fetch failed"}`, urlDatesUnreachable, ""},
		{"rate limited by the API", http.StatusTooManyRequests, `rate limit exceeded`, urlDatesSkipped, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer api.Close()
			publicThrottle = newSubmissionThrottleForget(100, time.Minute, time.Hour)
			h := suggestURLDatesHandler(&Config{SMTPHost: "smtp.example.test"}, &DansalClient{BaseURL: api.URL, HTTP: api.Client()})
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest(http.MethodGet, "/events/suggest/url-dates?url=https%3A%2F%2Fbalfolk-aachen.de%2Fveranstaltungen%2F", nil))
			var res urlDatesResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			if res.Status != tc.want {
				t.Errorf("status = %q, want %q", res.Status, tc.want)
			}
			if strings.Join(res.Dates, ",") != tc.dates {
				t.Errorf("dates = %q, want %q", res.Dates, tc.dates)
			}
		})
	}
}
