package main

import (
	"encoding/json"
	"testing"
)

// templateEventDataJSON is the canonical JSON shape written by the web process
// (cmd/dansal_web templateEventData). Any new field added to templateEventData
// must also appear here so the import side can parse it.
const templateEventDataJSON = `{
	"url": "https://example.com/event",
	"booking_url": "https://book.example.com",
	"start_time": "18:00",
	"end_time": "22:00",
	"has_ball": true,
	"has_workshop": true,
	"has_festival": false,
	"workshop_difficulty": "intermediate",
	"org_id": 5,
	"loc_id": 3,
	"pricing_type": "single",
	"pricing_amount": 12.5,
	"pricing_currency": "EUR",
	"pricing_lines": [{"label": "adult", "amount": 12.5}],
	"tags": ["bal-folk", "dance-workshop"],
	"dance_ids": [1, 2],
	"food": "soup",
	"drink": "water",
	"floor_condition": "wood",
	"attributes": {"wheelchair": true},
	"contact_name": "Alice",
	"contact_email": "alice@example.com",
	"tickets_total": 100,
	"booking_enabled": true,
	"timetable": [
		{"start_time": "16:00", "end_time": "17:00", "title": "Workshop", "description": "Intro", "room": "Main Hall"}
	]
}`

// TestTemplateRoundTrip verifies that JSON written by templateEventData (web
// process) is correctly parsed by templateImportData (API importer).
// Fields exclusive to one side (e.g. start_time/end_time, floor_condition,
// attributes, contact_*) are tolerated silently; shared fields must match.
func TestTemplateRoundTrip(t *testing.T) {
	var td templateImportData
	if err := json.Unmarshal([]byte(templateEventDataJSON), &td); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	check := func(name string, got, want any) {
		t.Helper()
		if got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}

	check("URL", td.URL, "https://example.com/event")
	check("BookingURL", td.BookingURL, "https://book.example.com")
	check("HasBall", td.HasBall, true)
	check("HasWorkshop", td.HasWorkshop, true)
	check("HasFestival", td.HasFestival, false)
	check("WorkshopDiff", td.WorkshopDiff, "intermediate") // field name differs, JSON key must match
	check("OrgID", td.OrgID, 5)
	check("LocID", td.LocID, 3)
	check("PricingType", td.PricingType, "single")
	check("PricingAmount", td.PricingAmount, 12.5)
	check("PricingCurrency", td.PricingCurrency, "EUR")
	check("Food", td.Food, "soup")
	check("Drink", td.Drink, "water")
	check("TicketsTotal", td.TicketsTotal, 100)
	check("BookingEnabled", td.BookingEnabled, true)

	if len(td.Tags) != 2 || td.Tags[0] != "bal-folk" {
		t.Errorf("Tags: got %v", td.Tags)
	}
	if len(td.DanceIDs) != 2 || td.DanceIDs[0] != 1 {
		t.Errorf("DanceIDs: got %v", td.DanceIDs)
	}
	if len(td.PricingLines) != 1 || td.PricingLines[0].Label != "adult" {
		t.Errorf("PricingLines: got %v", td.PricingLines)
	}
	if len(td.Timetable) != 1 {
		t.Fatalf("Timetable len: got %d", len(td.Timetable))
	}
	tt := td.Timetable[0]
	check("Timetable[0].StartTime", tt.StartTime, "16:00")
	check("Timetable[0].EndTime", tt.EndTime, "17:00")
	check("Timetable[0].Title", tt.Title, "Workshop")
	check("Timetable[0].Description", tt.Description, "Intro")
	check("Timetable[0].Room", tt.Room, "Main Hall")
}

// TestTemplateImportDataRoundTrip marshals and re-parses templateImportData
// to ensure no data is lost in a JSON round-trip.
func TestTemplateImportDataRoundTrip(t *testing.T) {
	orig := templateImportData{
		URL:             "https://example.com",
		BookingURL:      "https://book.example.com",
		HasBall:         true,
		HasWorkshop:     false,
		HasFestival:     true,
		WorkshopDiff:    "advanced",
		OrgID:           7,
		LocID:           9,
		LocName:         "should not be serialised",
		PricingType:     "multiple",
		PricingAmount:   0,
		PricingCurrency: "GBP",
		PricingLines:    []Price{{Label: "adult", Amount: 15}, {Label: "child", Amount: 8}},
		Tags:            []string{"bal-folk"},
		DanceIDs:        []int{3},
		Food:            "yes",
		Drink:           "yes",
		TicketsTotal:    50,
		BookingEnabled:  true,
		Timetable: []templateTimetableEntry{
			{StartTime: "20:00", EndTime: "23:00", Title: "Ball", Room: "Main"},
		},
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got templateImportData
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.LocName != "" {
		t.Errorf("LocName should not survive JSON round-trip, got %q", got.LocName)
	}
	if got.OrgID != orig.OrgID {
		t.Errorf("OrgID: got %d want %d", got.OrgID, orig.OrgID)
	}
	if got.WorkshopDiff != orig.WorkshopDiff {
		t.Errorf("WorkshopDiff: got %q want %q", got.WorkshopDiff, orig.WorkshopDiff)
	}
	if len(got.PricingLines) != 2 || got.PricingLines[0].Label != "adult" {
		t.Errorf("PricingLines: got %v", got.PricingLines)
	}
	if len(got.Timetable) != 1 || got.Timetable[0].StartTime != "20:00" {
		t.Errorf("Timetable: got %v", got.Timetable)
	}
}
