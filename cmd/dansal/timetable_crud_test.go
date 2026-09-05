package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
)

// newTimetableEntryRequest builds a request against the per-entry endpoints
// (#1270), same X-User-ID/X-User-Role header convention as
// newTimetableRequest in timetable_history_test.go, plus the entry_id path
// value those routes also carry.
func newTimetableEntryRequest(method string, body []byte, eventID, entryID, callerID int, role string) *httptest.ResponseRecorder {
	path := "/api/v1/events/" + strconv.Itoa(eventID) + "/timetable/" + strconv.Itoa(entryID)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("X-User-ID", strconv.Itoa(callerID))
	req.Header.Set("X-User-Role", role)
	req.SetPathValue("id", strconv.Itoa(eventID))
	req.SetPathValue("entry_id", strconv.Itoa(entryID))
	rec := httptest.NewRecorder()
	switch method {
	case "PUT":
		updateTimetableEntry(rec, req)
	case "DELETE":
		deleteTimetableEntry(rec, req)
	}
	return rec
}

// seedTimetableCrudEvent takes a distinguishing title rather than a fixed
// one: two events with the same title and start_time would collide on
// dedup's Tier 4 (title + start_time ±3h, no location set) and silently
// merge into one row — exactly the trap that bit e2e/helpers/indexFixture.ts
// earlier (#1266) and would otherwise make any test needing two genuinely
// separate events here quietly test against just one.
func seedTimetableCrudEvent(t *testing.T, title string) int {
	t.Helper()
	id, _, _, err := insertEvent(db, EventInput{
		Title: title, StartTime: 2000000000, EndTime: 2000100000, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insertEvent: %v", err)
	}
	return id
}

// TestUpdateTimetableEntrySuccess covers #1270's core contract: a per-entry
// PUT changes only that row (unlike replaceTimetable's delete-and-reinsert-
// everything), bumps version, and stamps updated_at/updated_by — leaving a
// second, untouched entry exactly as it was.
func TestUpdateTimetableEntrySuccess(t *testing.T) {
	setupDedupTestDB(t)
	eventID := seedTimetableCrudEvent(t, "Festival Success")

	postBody, _ := json.Marshal([]TimetableEntryRequest{
		{StartTime: "10:00", EndTime: "11:00", Title: "Keynote", EntryType: "talk"},
		{StartTime: "12:00", EndTime: "13:00", Title: "Lunch", EntryType: "break"},
	})
	rec := newTimetableRequest("POST", "/api/v1/events/1/timetable", postBody, eventID, 1, RoleAdmin)
	if rec.Code != 201 {
		t.Fatalf("seed POST: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var seeded []TimetableEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &seeded); err != nil || len(seeded) != 2 {
		t.Fatalf("decode seed response: err=%v body=%s", err, rec.Body.String())
	}
	if seeded[0].Version != 1 {
		t.Fatalf("expected a freshly inserted entry to start at version 1, got %d", seeded[0].Version)
	}

	putBody, _ := json.Marshal(TimetableEntryUpdateRequest{
		TimetableEntryRequest: TimetableEntryRequest{StartTime: "10:00", EndTime: "10:45", Title: "Keynote (shortened)", EntryType: "talk"},
		Version:               1,
	})
	rec = newTimetableEntryRequest("PUT", putBody, eventID, seeded[0].ID, 1, RoleAdmin)
	if rec.Code != 200 {
		t.Fatalf("PUT entry: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated TimetableEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if updated.Title != "Keynote (shortened)" || updated.EndTime != "10:45" {
		t.Fatalf("expected fields to be updated, got %+v", updated)
	}
	if updated.Version != 2 {
		t.Fatalf("expected version to bump to 2, got %d", updated.Version)
	}
	if updated.UpdatedAt == "" || updated.UpdatedBy == "" {
		t.Fatalf("expected updated_at/updated_by to be stamped, got %+v", updated)
	}

	// The other entry must be completely untouched.
	full, err := fetchTimetable(db, eventID)
	if err != nil {
		t.Fatalf("fetchTimetable: %v", err)
	}
	if len(full) != 2 {
		t.Fatalf("expected 2 entries to remain, got %d", len(full))
	}
	var lunch *TimetableEntry
	for i := range full {
		if full[i].ID == seeded[1].ID {
			lunch = &full[i]
		}
	}
	if lunch == nil || lunch.Title != "Lunch" || lunch.Version != 1 {
		t.Fatalf("expected the untouched entry to still be at version 1 with its original title, got %+v", lunch)
	}

	// #1176: the granular edit still journals a full-timetable snapshot.
	rec = newTimetableRequest("GET", "/api/v1/events/1/timetable/history", nil, eventID, 1, RoleAdmin)
	var history []TimetableHistoryEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history) != 2 { // seed POST + this PUT
		t.Fatalf("expected 2 journal rows (seed + edit), got %d", len(history))
	}
	if len(history[0].Snapshot) != 2 {
		t.Fatalf("expected the newest journal row to hold the full 2-entry timetable, got %+v", history[0].Snapshot)
	}
}

// TestUpdateTimetableEntryStaleVersionConflict is #1270's whole point: two
// editors both reading version 1, one saves (bumping to version 2), the
// other's PUT still carries the version-1 they read — it must be rejected
// with 409 instead of silently clobbering the first save.
func TestUpdateTimetableEntryStaleVersionConflict(t *testing.T) {
	setupDedupTestDB(t)
	eventID := seedTimetableCrudEvent(t, "Festival Conflict")

	postBody, _ := json.Marshal(TimetableEntryRequest{StartTime: "10:00", EndTime: "11:00", Title: "Keynote", EntryType: "talk"})
	rec := newTimetableRequest("POST", "/api/v1/events/1/timetable", postBody, eventID, 1, RoleAdmin)
	var seeded []TimetableEntry
	json.Unmarshal(rec.Body.Bytes(), &seeded)
	entryID := seeded[0].ID

	firstEditorBody, _ := json.Marshal(TimetableEntryUpdateRequest{
		TimetableEntryRequest: TimetableEntryRequest{StartTime: "10:00", EndTime: "11:00", Title: "Keynote (editor A)", EntryType: "talk"},
		Version:               1,
	})
	rec = newTimetableEntryRequest("PUT", firstEditorBody, eventID, entryID, 1, RoleAdmin)
	if rec.Code != 200 {
		t.Fatalf("editor A's PUT: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Editor B read the entry before editor A saved, so still carries version 1.
	secondEditorBody, _ := json.Marshal(TimetableEntryUpdateRequest{
		TimetableEntryRequest: TimetableEntryRequest{StartTime: "10:00", EndTime: "11:00", Title: "Keynote (editor B)", EntryType: "talk"},
		Version:               1,
	})
	rec = newTimetableEntryRequest("PUT", secondEditorBody, eventID, entryID, 1, RoleAdmin)
	if rec.Code != 409 {
		t.Fatalf("editor B's stale-version PUT: expected 409, got status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Editor A's save must survive untouched.
	full, _ := fetchTimetable(db, eventID)
	if len(full) != 1 || full[0].Title != "Keynote (editor A)" || full[0].Version != 2 {
		t.Fatalf("expected editor A's save to survive the rejected conflicting PUT, got %+v", full)
	}
}

// TestUpdateTimetableEntryWrongEvent ensures an entry_id that exists but
// belongs to a *different* event reads as 404, not as if it belonged to the
// event in the URL — the same mismatch guard deleteTimetableEntry uses.
func TestUpdateTimetableEntryWrongEvent(t *testing.T) {
	setupDedupTestDB(t)
	eventA := seedTimetableCrudEvent(t, "Festival A")
	eventB := seedTimetableCrudEvent(t, "Festival B")

	postBody, _ := json.Marshal(TimetableEntryRequest{StartTime: "10:00", EndTime: "11:00", Title: "Keynote", EntryType: "talk"})
	rec := newTimetableRequest("POST", "/api/v1/events/1/timetable", postBody, eventA, 1, RoleAdmin)
	var seeded []TimetableEntry
	json.Unmarshal(rec.Body.Bytes(), &seeded)

	putBody, _ := json.Marshal(TimetableEntryUpdateRequest{
		TimetableEntryRequest: TimetableEntryRequest{StartTime: "10:00", EndTime: "11:00", Title: "Hijacked", EntryType: "talk"},
		Version:               1,
	})
	rec = newTimetableEntryRequest("PUT", putBody, eventB, seeded[0].ID, 1, RoleAdmin)
	if rec.Code != 404 {
		t.Fatalf("PUT with entry belonging to a different event: expected 404, got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestDeleteTimetableEntrySuccess covers per-entry delete: only the targeted
// row disappears, the rest of the timetable is untouched, and the deletion
// still journals a full-snapshot row (#1176).
func TestDeleteTimetableEntrySuccess(t *testing.T) {
	setupDedupTestDB(t)
	eventID := seedTimetableCrudEvent(t, "Festival Delete")

	postBody, _ := json.Marshal([]TimetableEntryRequest{
		{StartTime: "10:00", EndTime: "11:00", Title: "Keynote", EntryType: "talk"},
		{StartTime: "12:00", EndTime: "13:00", Title: "Lunch", EntryType: "break"},
	})
	rec := newTimetableRequest("POST", "/api/v1/events/1/timetable", postBody, eventID, 1, RoleAdmin)
	var seeded []TimetableEntry
	json.Unmarshal(rec.Body.Bytes(), &seeded)

	rec = newTimetableEntryRequest("DELETE", nil, eventID, seeded[0].ID, 1, RoleAdmin)
	if rec.Code != 204 {
		t.Fatalf("DELETE entry: status=%d body=%s", rec.Code, rec.Body.String())
	}

	full, err := fetchTimetable(db, eventID)
	if err != nil {
		t.Fatalf("fetchTimetable: %v", err)
	}
	if len(full) != 1 || full[0].Title != "Lunch" {
		t.Fatalf("expected only the untargeted entry to remain, got %+v", full)
	}

	rec = newTimetableRequest("GET", "/api/v1/events/1/timetable/history", nil, eventID, 1, RoleAdmin)
	var history []TimetableHistoryEntry
	json.Unmarshal(rec.Body.Bytes(), &history)
	if len(history) != 2 { // seed POST + this DELETE
		t.Fatalf("expected 2 journal rows (seed + delete), got %d", len(history))
	}
	if len(history[0].Snapshot) != 1 {
		t.Fatalf("expected the newest journal row to hold the 1 remaining entry, got %+v", history[0].Snapshot)
	}
}

// TestTimetableEntryPublisherRoleForbiddenOutsideOrg mirrors
// TestTimetablePublisherRoleForbiddenOutsideOrg for the new per-entry
// routes — they must go through the same timetableAuthCheck, not a
// hand-rolled copy that forgets the org-membership gate.
func TestTimetableEntryPublisherRoleForbiddenOutsideOrg(t *testing.T) {
	setupDedupTestDB(t)

	res, err := db.Exec("INSERT INTO organizations (name) VALUES ('Test Org')")
	if err != nil {
		t.Fatalf("insert org: %v", err)
	}
	orgID64, _ := res.LastInsertId()
	orgID := int(orgID64)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (3, 'outsider@example.test', 'Outsider', 'publisher')")

	eventID, _, _, err := insertEvent(db, EventInput{
		Title: "Festival", StartTime: 2000000000, EndTime: 2000100000, IsPublished: true, OrganizationID: &orgID,
	})
	if err != nil {
		t.Fatalf("insertEvent: %v", err)
	}
	postBody, _ := json.Marshal(TimetableEntryRequest{StartTime: "10:00", EndTime: "11:00", Title: "Keynote", EntryType: "talk"})
	rec := newTimetableRequest("POST", "/api/v1/events/1/timetable", postBody, eventID, 1, RoleAdmin)
	var seeded []TimetableEntry
	json.Unmarshal(rec.Body.Bytes(), &seeded)

	putBody, _ := json.Marshal(TimetableEntryUpdateRequest{
		TimetableEntryRequest: TimetableEntryRequest{StartTime: "10:00", EndTime: "11:00", Title: "Hijacked", EntryType: "talk"},
		Version:               1,
	})
	rec = newTimetableEntryRequest("PUT", putBody, eventID, seeded[0].ID, 3, RolePublisher)
	if rec.Code != 403 {
		t.Fatalf("PUT entry as non-member publisher: expected 403, got status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = newTimetableEntryRequest("DELETE", nil, eventID, seeded[0].ID, 3, RolePublisher)
	if rec.Code != 403 {
		t.Fatalf("DELETE entry as non-member publisher: expected 403, got status=%d body=%s", rec.Code, rec.Body.String())
	}
}
