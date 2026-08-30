package main

import (
	"encoding/json"
	"testing"
)

// TestTimetablePublisherRoleAllowed covers #1192: a publisher-role caller who
// is a member of the event's organization must be able to write the
// timetable, same as RoleAdmin/RoleUser — the role gate ahead of
// timetableAuthCheck must not hard-reject RolePublisher before org
// membership is even considered.
func TestTimetablePublisherRoleAllowed(t *testing.T) {
	setupDedupTestDB(t)

	res, err := db.Exec("INSERT INTO organizations (name) VALUES ('Test Org')")
	if err != nil {
		t.Fatalf("insert org: %v", err)
	}
	orgID64, _ := res.LastInsertId()
	orgID := int(orgID64)

	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (2, 'pub@example.test', 'Publisher', 'publisher')")
	if _, err := db.Exec("INSERT INTO organization_members (organization_id, user_id) VALUES (?, ?)", orgID, 2); err != nil {
		t.Fatalf("insert org member: %v", err)
	}

	id, _, _, err := insertEvent(db, EventInput{
		Title: "Festival", StartTime: 2000000000, EndTime: 2000100000, IsPublished: true, OrganizationID: &orgID,
	})
	if err != nil {
		t.Fatalf("insertEvent: %v", err)
	}

	postBody, _ := json.Marshal(TimetableEntryRequest{StartTime: "12:00", EndTime: "13:00", Title: "Lunch", EntryType: "break"})
	rec := newTimetableRequest("POST", "/api/v1/events/1/timetable", postBody, id, 2, RolePublisher)
	if rec.Code != 201 {
		t.Fatalf("POST timetable as publisher: status=%d body=%s", rec.Code, rec.Body.String())
	}

	putBody, _ := json.Marshal([]TimetableEntryRequest{
		{StartTime: "10:00", EndTime: "11:00", Title: "Keynote", EntryType: "keynote"},
	})
	rec = newTimetableRequest("PUT", "/api/v1/events/1/timetable", putBody, id, 2, RolePublisher)
	if rec.Code != 200 {
		t.Fatalf("PUT timetable as publisher: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = newTimetableRequest("DELETE", "/api/v1/events/1/timetable", nil, id, 2, RolePublisher)
	if rec.Code != 204 {
		t.Fatalf("DELETE timetable as publisher: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestTimetablePublisherRoleForbiddenOutsideOrg ensures the fix doesn't
// over-widen: a publisher who is NOT a member of the event's org must still
// be rejected, same as before.
func TestTimetablePublisherRoleForbiddenOutsideOrg(t *testing.T) {
	setupDedupTestDB(t)

	res, err := db.Exec("INSERT INTO organizations (name) VALUES ('Test Org')")
	if err != nil {
		t.Fatalf("insert org: %v", err)
	}
	orgID64, _ := res.LastInsertId()
	orgID := int(orgID64)

	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (3, 'outsider@example.test', 'Outsider', 'publisher')")

	id, _, _, err := insertEvent(db, EventInput{
		Title: "Festival", StartTime: 2000000000, EndTime: 2000100000, IsPublished: true, OrganizationID: &orgID,
	})
	if err != nil {
		t.Fatalf("insertEvent: %v", err)
	}

	postBody, _ := json.Marshal(TimetableEntryRequest{StartTime: "12:00", EndTime: "13:00", Title: "Lunch", EntryType: "break"})
	rec := newTimetableRequest("POST", "/api/v1/events/1/timetable", postBody, id, 3, RolePublisher)
	if rec.Code != 403 {
		t.Fatalf("POST timetable as non-member publisher: status=%d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}
