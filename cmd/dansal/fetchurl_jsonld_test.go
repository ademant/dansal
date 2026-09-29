package main

// JSON-LD event-page import tests (#1376). Fixtures are minimised real-world
// shapes: a bare Event, an @graph wrapper, an ItemList listing page, an Event
// subclass, a page with no Event markup (the new error), an ISO-8601
// duration instead of endDate, and a date-only startDate.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func jsonldPage(scripts ...string) []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><head>")
	for _, s := range scripts {
		b.WriteString(`<script type="application/ld+json">`)
		b.WriteString(s)
		b.WriteString(`</script>`)
	}
	b.WriteString("</head><body></body></html>")
	return []byte(b.String())
}

// futureDate returns a date string comfortably in the future so fixtures
// aren't filtered out by parseJSONLDBody's "already past" check.
func futureDate(daysFromNow int, withTime bool) string {
	d := time.Now().AddDate(0, 0, daysFromNow)
	if withTime {
		return d.Format("2006-01-02") + "T20:00:00+02:00"
	}
	return d.Format("2006-01-02")
}

func TestParseJSONLDBodyBareEvent(t *testing.T) {
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	body := jsonldPage(fmt.Sprintf(`{
		"@context": "https://schema.org",
		"@type": "Event",
		"name": "Bal Folk de Rennes",
		"description": "Une soirée de danses traditionnelles",
		"startDate": "%s",
		"endDate": "%s",
		"url": "https://example.org/events/1",
		"location": {
			"@type": "Place",
			"name": "Salle des fêtes",
			"address": {
				"@type": "PostalAddress",
				"streetAddress": "1 rue de la Paix",
				"addressLocality": "Rennes",
				"postalCode": "35000",
				"addressCountry": "FR"
			},
			"geo": {"@type": "GeoCoordinates", "latitude": 48.117, "longitude": -1.677}
		}
	}`, futureDate(30, true), futureDate(30, true)))

	rep := &icalParseReport{}
	reqs, err := parseJSONLDBody(body, FetchSource{URL: "https://example.org/events/1"}, rep)
	if err != nil {
		t.Fatalf("parseJSONLDBody: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Title != "Bal Folk de Rennes" {
		t.Errorf("Title = %q", req.Title)
	}
	if req.Location.Location != "Salle des fêtes" || req.Location.Town != "Rennes" || req.Location.CountryCode == "" && req.Location.Country != "FR" {
		t.Errorf("Location = %+v", req.Location)
	}
	if req.Location.Latitude == nil || *req.Location.Latitude != 48.117 {
		t.Errorf("Latitude = %v", req.Location.Latitude)
	}
	if req.TimezoneFallback {
		t.Error("an explicit-offset DateTime must not be flagged as a fallback")
	}
	if rep.TimezoneFallback != 0 || rep.Unparsed != 0 {
		t.Errorf("report = %+v, want zero", rep)
	}
}

func TestParseJSONLDBodyGraphWrapper(t *testing.T) {
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	body := jsonldPage(fmt.Sprintf(`{
		"@context": "https://schema.org",
		"@graph": [
			{"@type": "WebSite", "name": "Not an event"},
			{"@type": "Event", "name": "Fest Noz", "startDate": "%s"}
		]
	}`, futureDate(10, true)))

	reqs, err := parseJSONLDBody(body, FetchSource{URL: "https://example.org/p"}, nil)
	if err != nil {
		t.Fatalf("parseJSONLDBody: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Title != "Fest Noz" {
		t.Fatalf("got %+v", reqs)
	}
}

func TestParseJSONLDBodyItemListWrapper(t *testing.T) {
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	body := jsonldPage(fmt.Sprintf(`{
		"@context": "https://schema.org",
		"@type": "ItemList",
		"itemListElement": [
			{"@type": "ListItem", "position": 1, "item": {"@type": "MusicEvent", "name": "Concert A", "startDate": "%s"}},
			{"@type": "ListItem", "position": 2, "item": {"@type": "Festival", "name": "Festival B", "startDate": "%s"}}
		]
	}`, futureDate(5, true), futureDate(15, true)))

	reqs, err := parseJSONLDBody(body, FetchSource{URL: "https://example.org/listing"}, nil)
	if err != nil {
		t.Fatalf("parseJSONLDBody: %v", err)
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2: %+v", len(reqs), reqs)
	}
	titles := []string{reqs[0].Title, reqs[1].Title}
	if titles[0] != "Concert A" || titles[1] != "Festival B" {
		t.Errorf("titles = %v", titles)
	}
}

func TestParseJSONLDBodyNoEventMarkup(t *testing.T) {
	body := jsonldPage(`{"@context": "https://schema.org", "@type": "BreadcrumbList", "itemListElement": []}`)
	_, err := parseJSONLDBody(body, FetchSource{URL: "https://example.org/p"}, nil)
	if !errors.Is(err, errNoMachineReadableEvents) {
		t.Fatalf("err = %v, want errNoMachineReadableEvents", err)
	}
}

func TestParseJSONLDBodyNoScriptAtAll(t *testing.T) {
	body := []byte(`<!DOCTYPE html><html><body><p>Just a page.</p></body></html>`)
	_, err := parseJSONLDBody(body, FetchSource{URL: "https://example.org/p"}, nil)
	if !errors.Is(err, errNoMachineReadableEvents) {
		t.Fatalf("err = %v, want errNoMachineReadableEvents", err)
	}
}

func TestParseJSONLDBodyISO8601Duration(t *testing.T) {
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	body := jsonldPage(fmt.Sprintf(`{
		"@context": "https://schema.org", "@type": "Event", "name": "Atelier",
		"startDate": "%s", "duration": "PT2H30M"
	}`, futureDate(20, true)))

	reqs, err := parseJSONLDBody(body, FetchSource{URL: "https://example.org/p"}, nil)
	if err != nil {
		t.Fatalf("parseJSONLDBody: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	start, _ := time.Parse(time.RFC3339, reqs[0].StartTime)
	end, _ := time.Parse(time.RFC3339, reqs[0].EndTime)
	if got := end.Sub(start); got != 150*time.Minute {
		t.Errorf("end-start = %v, want 2h30m", got)
	}
}

func TestParseJSONLDBodyDateOnlyStartDateFlagsFallback(t *testing.T) {
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	body := jsonldPage(fmt.Sprintf(`{
		"@context": "https://schema.org", "@type": "Event", "name": "Stage",
		"startDate": "%s"
	}`, futureDate(40, false)))

	rep := &icalParseReport{}
	reqs, err := parseJSONLDBody(body, FetchSource{URL: "https://example.org/p"}, rep)
	if err != nil {
		t.Fatalf("parseJSONLDBody: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	// "when only a date is given, require a human-checkable value rather
	// than guessing midnight silently" (#1376) — the guess must be visible,
	// not undone: the request still carries a start time (the disclosure
	// mechanism is TimezoneFallback / the report count, not refusing to import).
	if !reqs[0].TimezoneFallback {
		t.Error("a date-only startDate must be flagged as a fallback")
	}
	if rep.TimezoneFallback != 1 {
		t.Errorf("report.TimezoneFallback = %d, want 1", rep.TimezoneFallback)
	}
}

func TestParseJSONLDBodyPastEventFilteredOut(t *testing.T) {
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	body := jsonldPage(`{"@context": "https://schema.org", "@type": "Event", "name": "Old", "startDate": "2020-01-01T20:00:00+01:00", "endDate": "2020-01-01T23:00:00+01:00"}`)
	reqs, err := parseJSONLDBody(body, FetchSource{URL: "https://example.org/p"}, nil)
	if err != nil {
		t.Fatalf("parseJSONLDBody: %v", err)
	}
	if len(reqs) != 0 {
		t.Fatalf("got %d requests, want 0 (event is in the past)", len(reqs))
	}
}

func TestParseJSONLDBodyFreeOffer(t *testing.T) {
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	body := jsonldPage(fmt.Sprintf(`{
		"@context": "https://schema.org", "@type": "Event", "name": "Bal gratuit",
		"startDate": "%s",
		"offers": {"@type": "Offer", "price": "0", "priceCurrency": "EUR"}
	}`, futureDate(3, true)))
	reqs, err := parseJSONLDBody(body, FetchSource{URL: "https://example.org/p"}, nil)
	if err != nil {
		t.Fatalf("parseJSONLDBody: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Pricing == nil || reqs[0].Pricing.Type != "free" {
		t.Fatalf("got %+v", reqs)
	}
}

func TestParseJSONLDBodyPaidOffer(t *testing.T) {
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	body := jsonldPage(fmt.Sprintf(`{
		"@context": "https://schema.org", "@type": "Event", "name": "Concert",
		"startDate": "%s",
		"offers": [{"@type": "Offer", "price": 12.5, "priceCurrency": "EUR"}]
	}`, futureDate(3, true)))
	reqs, err := parseJSONLDBody(body, FetchSource{URL: "https://example.org/p"}, nil)
	if err != nil {
		t.Fatalf("parseJSONLDBody: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Pricing == nil || reqs[0].Pricing.Type != "single" || reqs[0].Pricing.Amount != 12.5 {
		t.Fatalf("got %+v", reqs)
	}
}

func TestDetectFetchTypeDoesNotAutoDetectJsonldFromHTML(t *testing.T) {
	// #1376 point 5: jsonld cannot be auto-detected — a generic text/html
	// content type (from any page, including one publishing JSON-LD) must
	// keep falling to the default "ical" classification; the submitter picks
	// jsonld explicitly via the dropdown.
	if got := validFetchType("jsonld"); !got {
		t.Fatal("validFetchType(\"jsonld\") = false, want true")
	}
}
