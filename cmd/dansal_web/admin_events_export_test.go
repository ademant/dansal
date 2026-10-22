package main

import (
	"bytes"
	"encoding/csv"
	"net/url"
	"strings"
	"testing"
)

// #1584: the events CSV export. Covers the file layout (BOM, '|' delimiter,
// header), cell sanitising, and the feed-matching uid and ISO dates.
func TestWriteEventsCSVLayout(t *testing.T) {
	org := 7
	events := []Event{{
		ID:             42,
		Title:          "Ball | Night\nsecond line",
		Description:    "=HYPERLINK(\"x\")",
		StartTime:      "2026-10-13T18:00:00+02:00",
		EndTime:        "2026-10-13T23:30:00+02:00",
		ShortCode:      "BN42",
		OrganizationID: &org,
		Location:       &Location{Location: "Saal", Town: "Berlin", Address: "Str. 1", Zipcode: "10115", Country: "Germany"},
		Tags:           []string{"bal-folk", "beginners"},
		IsPublished:    true,
		TicketsTotal:   120,
	}}
	orgMap := map[int]string{7: "Tanzkreis"}

	var buf bytes.Buffer
	if err := writeEventsCSV(&buf, events, orgMap, "example.test"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\uFEFF") {
		t.Fatalf("export does not start with a UTF-8 BOM: %q", out[:min(len(out), 8)])
	}

	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(out, "\uFEFF")))
	r.Comma = '|'
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("export is not readable as '|'-separated CSV: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want header + 1", len(rows))
	}
	header, row := rows[0], rows[1]
	if len(row) != len(header) {
		t.Fatalf("row has %d cells, header %d", len(row), len(header))
	}
	cell := func(name string) string {
		for i, h := range header {
			if h == name {
				return row[i]
			}
		}
		t.Fatalf("no column %q", name)
		return ""
	}

	if got := cell("uid"); got != "event-42@example.test" {
		t.Errorf("uid = %q", got)
	}
	if got := cell("title"); got != "Ball / Night second line" {
		t.Errorf("title = %q: '|' must become '/' and the newline a space", got)
	}
	if got := cell("description"); got != "'=HYPERLINK(\"x\")" {
		t.Errorf("description = %q: formula-like value must be neutralised", got)
	}
	if got := cell("date_from"); got != "2026-10-13" {
		t.Errorf("date_from = %q", got)
	}
	if got := cell("start_time"); got != "18:00" {
		t.Errorf("start_time = %q", got)
	}
	if got := cell("end_time"); got != "23:30" {
		t.Errorf("end_time = %q", got)
	}
	if got := cell("organization"); got != "Tanzkreis" {
		t.Errorf("organization = %q", got)
	}
	if got := cell("tags"); got != "bal-folk, beginners" {
		t.Errorf("tags = %q", got)
	}
	if got := cell("canonical_url"); got != "https://example.test/events/42" {
		t.Errorf("canonical_url = %q", got)
	}
	if got := cell("tickets_total"); got != "120" {
		t.Errorf("tickets_total = %q", got)
	}
	if got := cell("city"); got != "Berlin" {
		t.Errorf("city = %q", got)
	}
}

// The export uses the same filter query as the page, so each maintenance filter
// must reach the API as its server-side parameter. Musician and instructor
// values are repeated, one per id; junk values are dropped.
func TestMaintenanceFilterAPIParams(t *testing.T) {
	q, _ := url.ParseQuery("org_id=3&city=Berlin&has_source=1&location_id=5&musician_id=1,2&musician_id=3&musician_id=abc&musician_id=0&instructor_id=4&date_from=2026-10-01")
	f := parseMaintenanceFilter(q)
	p := f.apiParams()

	checks := map[string]string{
		"organization_id": "3",
		"town":            "Berlin",
		"has_source":      "1",
		"location_id":     "5",
	}
	for k, want := range checks {
		if got := p.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if got := p["event_musician_id"]; len(got) != 3 || got[0] != "1" || got[1] != "2" || got[2] != "3" {
		t.Errorf("event_musician_id = %v, want [1 2 3]", got)
	}
	if got := p["event_instructor_id"]; len(got) != 1 || got[0] != "4" {
		t.Errorf("event_instructor_id = %v, want [4]", got)
	}
	if p.Has("musician_id") {
		t.Error("the legacy musician_id must not be sent: it also matches timetable performers")
	}
	if p.Get("start_time_after") == "" {
		t.Error("date_from must become start_time_after")
	}
	if !f.active() {
		t.Error("filter with values must be active")
	}
}
