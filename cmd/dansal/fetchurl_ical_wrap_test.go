package main

// iCal feed misdetected as RSS when served as text/xml, and unparseable when
// wrapped in HTML (#1387). Found via a TYPO3 laks_calendar single-event
// export: HTTP 200, Content-Type: text/xml;charset=UTF-8, but the body is an
// HTML page with the VCALENDAR embedded partway through. The fixture below is
// a minimised version of the real response body from the issue.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// typo3WrappedICalFixture mirrors the real laks_calendar export: a full HTML
// document with BEGIN:VCALENDAR...END:VCALENDAR embedded in the body. DTSTART
// has no TZID/Z suffix (a floating local time), matching the real response.
const typo3WrappedICalFixture = `<!DOCTYPE html>
<html lang="de">
<head><title>Hessen Szene: Hessen Szene</title></head>
<body>
  <div class="download-hint">Your calendar file is being generated…</div>
    BEGIN:VCALENDAR
VERSION:2.0
CALSCALE:GREGORIAN
METHOD:PUBLISH
X-WR-CALNAME:Veranstaltungskalender
BEGIN:VEVENT
CLASS:PUBLIC
UID:veranstaltungskalender-133315
SUMMARY:Folkwards! Tanzworkshop
DTSTAMP:20260926T194910Z
DTSTART:20261101T150000
LOCATION:Kulturcafé-Saal\, Darmstädter Straße 31\, 64521 Groß-Gerau
PRIORITY:5
SEQUENCE:0
END:VEVENT
END:VCALENDAR
  </div>
</body></html>`

func TestExtractVCalendarBody(t *testing.T) {
	wellFormed := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n")
	if got := extractVCalendarBody(wellFormed); !bytes.Equal(got, wellFormed) {
		t.Errorf("well-formed body must be returned unchanged, got %q", got)
	}

	wrapped := []byte(typo3WrappedICalFixture)
	extracted := extractVCalendarBody(wrapped)
	if !bytes.HasPrefix(extracted, vcalendarBegin) {
		t.Fatalf("extracted body doesn't start with BEGIN:VCALENDAR: %q", extracted)
	}
	if !bytes.HasSuffix(bytes.TrimRight(extracted, "\r\n \t"), vcalendarEnd) {
		t.Fatalf("extracted body doesn't end with END:VCALENDAR: %q", extracted)
	}
	if bytes.Contains(extracted, []byte("<html")) {
		t.Errorf("extracted body still contains the HTML wrapper: %q", extracted)
	}

	// Leading whitespace/BOM before a genuinely well-formed body: still
	// returned unchanged (ics.ParseCalendar tolerates leading blank lines
	// itself; extraction must not interfere).
	withBOM := append([]byte("\ufeff"), wellFormed...)
	if got := extractVCalendarBody(withBOM); !bytes.Equal(got, withBOM) {
		t.Errorf("BOM-prefixed well-formed body must be returned unchanged, got %q", got)
	}

	noCalendar := []byte("<html><body>nothing here</body></html>")
	if got := extractVCalendarBody(noCalendar); !bytes.Equal(got, noCalendar) {
		t.Errorf("body with no VCALENDAR must be returned unchanged so the original parse error is preserved, got %q", got)
	}
}

func TestParseICalBodyToleratesHTMLWrapper(t *testing.T) {
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	entries, err := parseICalBody([]byte(typo3WrappedICalFixture), FetchSource{Type: "ical", URL: "https://www.hessen-szene.de/"})
	if err != nil {
		t.Fatalf("parseICalBody: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	req := entries[0].req
	if req.Title != "Folkwards! Tanzworkshop" {
		t.Errorf("Title = %q", req.Title)
	}
	if req.UID != "veranstaltungskalender-133315" {
		t.Errorf("UID = %q", req.UID)
	}
}

func TestDetectFetchTypeSniffsHTMLWrappedICalServedAsXML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
		w.Write([]byte(typo3WrappedICalFixture))
	}))
	defer srv.Close()

	old := safeClient
	safeClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { safeClient = old })

	if got := detectFetchType(srv.URL); got != "ical" {
		t.Fatalf("detectFetchType() = %q, want %q", got, "ical")
	}
}

// TestDetectFetchTypeGenuineXMLRSSStillDetectedAsRSS is the explicit
// regression check from the issue's acceptance criteria: a real RSS/Atom feed
// served with a generic XML content type must not be reclassified just
// because ambiguous types now get sniffed.
func TestDetectFetchTypeGenuineXMLRSSStillDetectedAsRSS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Events</title></channel></rss>`))
	}))
	defer srv.Close()

	old := safeClient
	safeClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { safeClient = old })

	if got := detectFetchType(srv.URL); got != "rss" {
		t.Fatalf("detectFetchType() = %q, want %q", got, "rss")
	}
}

// TestDetectFetchTypeICSURLHintSkipsBodyFetch checks that an .ics-ish URL
// hint short-circuits before the body-sniffing GET — a server that only
// answers HEAD (GET fails) still detects correctly, proving the hint is
// actually being used rather than the sniff happening to succeed anyway.
func TestDetectFetchTypeICSURLHintSkipsBodyFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		if r.Method == http.MethodGet {
			t.Error("GET should not have been made: the URL's \"ics\" hint must short-circuit the body sniff")
			http.Error(w, "unexpected GET", http.StatusInternalServerError)
			return
		}
	}))
	defer srv.Close()

	old := safeClient
	safeClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { safeClient = old })

	if got := detectFetchType(srv.URL + "/?action=ics"); got != "ical" {
		t.Fatalf("detectFetchType() = %q, want %q", got, "ical")
	}
}

func TestRSSMismatchHint(t *testing.T) {
	if h := rssMismatchHint([]byte(typo3WrappedICalFixture)); !strings.Contains(h, "VCALENDAR") {
		t.Errorf("expected a VCALENDAR hint, got %q", h)
	}
	if h := rssMismatchHint([]byte("<!DOCTYPE html><html><body>nope</body></html>")); !strings.Contains(h, "HTML") {
		t.Errorf("expected an HTML hint, got %q", h)
	}
	if h := rssMismatchHint([]byte("not xml at all")); h != "" {
		t.Errorf("expected no hint for unrecognised content, got %q", h)
	}
}

// TestParseRSSBodyErrorNamesActualFormat covers the issue's bug C directly:
// when a feed is (mis)routed to the RSS parser, the failure must say what was
// actually found rather than just repeating the wrong assumption.
func TestParseRSSBodyErrorNamesActualFormat(t *testing.T) {
	_, err := parseRSSBody([]byte(typo3WrappedICalFixture), FetchSource{Type: "rss"})
	if err == nil {
		t.Fatal("expected an error parsing an iCal body as RSS")
	}
	if !strings.Contains(err.Error(), "VCALENDAR") {
		t.Errorf("error should name the actual content, got: %v", err)
	}
}

// TestImportFromICalSourceEndToEndHTMLWrapped is the issue's first acceptance
// criterion end to end: the reproduction URL (a fixture standing in for it)
// imports successfully and yields the expected event.
func TestImportFromICalSourceEndToEndHTMLWrapped(t *testing.T) {
	setupDedupTestDB(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
		w.Header().Set("Content-Disposition", "attachment; filename=veranstaltungskalender-9afe492b.ics")
		w.Write([]byte(typo3WrappedICalFixture))
	}))
	defer srv.Close()

	old := safeClient
	safeClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { safeClient = old })

	typ := detectFetchType(srv.URL)
	if typ != "ical" {
		t.Fatalf("detectFetchType() = %q, want %q", typ, "ical")
	}

	events, counts, err := importFromSource(context.Background(), FetchSource{Type: typ, URL: srv.URL})
	if err != nil {
		t.Fatalf("importFromSource: %v", err)
	}
	if counts.New != 1 || len(events) != 1 {
		t.Fatalf("counts = %+v, events = %d, want 1 new event", counts, len(events))
	}
	if events[0].Title != "Folkwards! Tanzworkshop" {
		t.Errorf("Title = %q", events[0].Title)
	}
}
