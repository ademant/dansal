package main

import "testing"

// TestUpdateTemplate covers the ownership scoping added for #1086 (editing
// an already-saved template, previously impossible — only create/delete
// existed): the owner can update their own template, an unrelated user's
// update affects nothing (reported as sql.ErrNoRows) rather than silently
// editing someone else's row, and admin bypasses the ownership check.
func TestUpdateTemplate(t *testing.T) {
	db := initDB(":memory:")
	defer db.Close()

	id, err := saveTemplate(db, 1, nil, nil, nil, "Weekly bal", `{"url":"https://old.example"}`)
	if err != nil {
		t.Fatalf("saveTemplate: %v", err)
	}

	// Owner can update.
	orgID := 5
	if err := updateTemplate(db, int(id), 1, false, &orgID, "Weekly bal (updated)", `{"url":"https://new.example"}`); err != nil {
		t.Fatalf("updateTemplate as owner: %v", err)
	}
	got, err := getTemplate(db, int(id))
	if err != nil {
		t.Fatalf("getTemplate: %v", err)
	}
	if got.Name != "Weekly bal (updated)" || got.Data != `{"url":"https://new.example"}` {
		t.Fatalf("template not updated: name=%q data=%q", got.Name, got.Data)
	}
	if got.OrgID == nil || *got.OrgID != 5 {
		t.Fatalf("org_id not updated: %v", got.OrgID)
	}

	// A different, non-admin user cannot update someone else's template.
	if err := updateTemplate(db, int(id), 2, false, nil, "Hijacked", "{}"); err == nil {
		t.Fatalf("expected error when a non-owner updates another user's template")
	}
	got2, _ := getTemplate(db, int(id))
	if got2.Name != "Weekly bal (updated)" {
		t.Fatalf("template was modified by a non-owner: name=%q", got2.Name)
	}

	// Admin can update regardless of ownership.
	if err := updateTemplate(db, int(id), 2, true, nil, "Admin edit", "{}"); err != nil {
		t.Fatalf("updateTemplate as admin: %v", err)
	}
	got3, _ := getTemplate(db, int(id))
	if got3.Name != "Admin edit" {
		t.Fatalf("admin update did not apply: name=%q", got3.Name)
	}
}

// TestTemplateDataToEvent is the round-trip check for templateDataFromEvent:
// a template's stored JSON should rebuild an Event whose fields drive the
// same form sections used to create it (#1086's edit page prefill).
func TestTemplateDataToEvent(t *testing.T) {
	locByID := map[int]Location{
		42: {ID: 42, Location: "Village Hall", Town: "Testville"},
	}
	td := templateEventData{
		URL:                "https://example.com",
		BookingURL:         "https://tickets.example.com",
		HasBall:            true,
		WorkshopDifficulty: "beginner",
		OrgID:              7,
		LocID:              42,
		PricingType:        "single",
		PricingAmount:      12.5,
		PricingCurrency:    "EUR",
		Tags:               []string{"bal-folk"},
		Food:               "sold",
		Drink:              "alcohol",
		ContactName:        "Jo",
		ContactEmail:       "jo@example.com",
		TicketsTotal:       50,
		BookingEnabled:     true,
	}

	ev := templateDataToEvent(td, locByID)

	if ev.URL != td.URL || ev.BookingURL != td.BookingURL {
		t.Errorf("URL/BookingURL not carried over: %+v", ev)
	}
	if !ev.HasBall {
		t.Errorf("HasBall not carried over")
	}
	if ev.OrganizationID == nil || *ev.OrganizationID != 7 {
		t.Errorf("OrganizationID = %v, want 7", ev.OrganizationID)
	}
	if ev.LocationID == nil || *ev.LocationID != 42 {
		t.Errorf("LocationID = %v, want 42", ev.LocationID)
	}
	if ev.Location == nil || ev.Location.Location != "Village Hall" {
		t.Errorf("Location not resolved from locByID: %+v", ev.Location)
	}
	if ev.Pricing == nil || ev.Pricing.Type != "single" || ev.Pricing.Amount != 12.5 || ev.Pricing.Currency != "EUR" {
		t.Errorf("Pricing not carried over: %+v", ev.Pricing)
	}
	if len(ev.Tags) != 1 || ev.Tags[0] != "bal-folk" {
		t.Errorf("Tags not carried over: %v", ev.Tags)
	}
	if ev.ContactName != "Jo" || ev.ContactEmail != "jo@example.com" {
		t.Errorf("Contact fields not carried over: %+v", ev)
	}
	if ev.TicketsTotal != 50 || !ev.BookingEnabled {
		t.Errorf("Ticket fields not carried over: %+v", ev)
	}

	// No org/location/pricing set at all → nothing synthesized.
	empty := templateDataToEvent(templateEventData{}, locByID)
	if empty.OrganizationID != nil || empty.LocationID != nil || empty.Pricing != nil || empty.Location != nil {
		t.Errorf("expected all-nil optional fields for an empty template, got %+v", empty)
	}
}
