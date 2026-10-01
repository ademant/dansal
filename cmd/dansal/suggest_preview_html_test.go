package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSuggestPreviewParsesHTMLEventPage covers #1417: an event page with a
// JSON-LD Event (pretix & co.) submitted without a type hint used to be
// parsed as iCal and fail; it must now yield the event and its date.
func TestSuggestPreviewParsesHTMLEventPage(t *testing.T) {
	if instanceTimezone == nil {
		instanceTimezone, _ = time.LoadLocation("Europe/Berlin")
	}
	config = &Config{}
	initSuggestRateLimiters()

	page := `<!DOCTYPE html><html><head><title>Journée baroque</title>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"Event",
"name":"Wirkstatt Journée baroque","startDate":"2026-10-03T10:00:00+02:00","endDate":"2026-10-03T18:00:00+02:00",
"location":{"@type":"Place","name":"Karlsburg Durlach","address":{"@type":"PostalAddress","streetAddress":"Pfinztalstraße 9","postalCode":"76227","addressLocality":"Karlsruhe"}}}</script>
</head><body>…</body></html>`

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "page.html")
	fw.Write([]byte(page))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events/suggest-preview", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.RemoteAddr = "203.0.113.9:1234"
	rec := httptest.NewRecorder()
	suggestPreviewHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var events []EventCreateRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil || len(events) != 1 {
		t.Fatalf("events = %d (%v), body=%s", len(events), err, rec.Body.String())
	}
	if !strings.HasPrefix(events[0].StartTime, "2026-10-03") || events[0].Location.Location != "Karlsburg Durlach" {
		t.Errorf("parsed start=%q location=%q", events[0].StartTime, events[0].Location.Location)
	}
}

func TestLooksLikeHTML(t *testing.T) {
	cases := map[string]bool{
		"<!DOCTYPE html><html>":                    true,
		"\xef\xbb\xbf<!doctype HTML>":              true,
		"  <html lang=de>":                         true,
		"BEGIN:VCALENDAR\r\nVERSION:2.0":           false,
		`[{"title":"x"}]`:                          false,
		`<div><script type="application/ld+json">`: true,
	}
	for in, want := range cases {
		if got := looksLikeHTML([]byte(in)); got != want {
			t.Errorf("looksLikeHTML(%q) = %v, want %v", in, got, want)
		}
	}
}
