package main

import "testing"

// TestEventResponseIncludesSourceAttribution verifies scanEventRow/
// eventListSelect denormalize fetch_sources.attribution/licence/terms_url
// onto every event imported from that source (#1485, compliance G9
// phase-67), the same way series_cadence already does for event_series —
// see TestEventResponseIncludesSeriesCadence.
func TestEventResponseIncludesSourceAttribution(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")

	res, err := db.Exec(`INSERT INTO fetch_sources (url, type, attribution, licence, terms_url) VALUES ('https://example.test/feed.ics', 'ical', 'Example Verein', 'CC-BY-4.0', 'https://example.test/terms')`)
	if err != nil {
		t.Fatalf("insert fetch source: %v", err)
	}
	srcID64, _ := res.LastInsertId()
	srcID := int(srcID64)

	eventID, _, _, err := insertEvent(db, EventInput{
		Title: "Imported Ball", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
		FetchSourceID: srcID,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	event, err := fetchEventByID(db, eventID)
	if err != nil {
		t.Fatalf("fetchEventByID: %v", err)
	}
	if event.SourceAttribution != "Example Verein" {
		t.Errorf("SourceAttribution = %q, want %q", event.SourceAttribution, "Example Verein")
	}
	if event.SourceLicence != "CC-BY-4.0" {
		t.Errorf("SourceLicence = %q, want %q", event.SourceLicence, "CC-BY-4.0")
	}
	if event.SourceTermsURL != "https://example.test/terms" {
		t.Errorf("SourceTermsURL = %q, want %q", event.SourceTermsURL, "https://example.test/terms")
	}

	// A manually-created event (no fetch source) carries no source metadata.
	eventID2, _, _, err := insertEvent(db, EventInput{
		Title: "Manual Ball", StartTime: 2000010000, EndTime: 2000013600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert second event: %v", err)
	}
	event2, err := fetchEventByID(db, eventID2)
	if err != nil {
		t.Fatalf("fetchEventByID: %v", err)
	}
	if event2.SourceAttribution != "" || event2.SourceLicence != "" || event2.SourceTermsURL != "" {
		t.Errorf("manual event source fields = %q %q %q, want all empty", event2.SourceAttribution, event2.SourceLicence, event2.SourceTermsURL)
	}
}
