package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// insertTestPendingSuggestion inserts a pending_fetch_suggestions row for
// approval/rejection tests, returning its id.
func insertTestPendingSuggestion(t *testing.T, orgID *int, orgName, locationMappingsJSON string) int {
	t.Helper()
	if locationMappingsJSON == "" {
		locationMappingsJSON = "[]"
	}
	var orgIDArg any
	if orgID != nil {
		orgIDArg = *orgID
	}
	token, err := generateToken(16)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(
		`INSERT INTO pending_fetch_suggestions
		 (token, email, feed_url, feed_type, event_count, org_id, org_name, location_mappings)
		 VALUES (?, 'suggester@example.com', 'https://example.com/calendar.ics', 'ical', 3, ?, ?, ?)`,
		token, orgIDArg, orgName, locationMappingsJSON,
	)
	if err != nil {
		t.Fatalf("insert pending suggestion: %v", err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

func reviewRequest(method, path string, callerID int, role string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-User-ID", strconv.Itoa(callerID))
	req.Header.Set("X-User-Role", role)
	return req
}

// TestApproveFetchSuggestionExistingOrg guards the common path: an existing
// org, one matched location and one brand-new one. Approval must alias both
// (so future fetches of this feed auto-resolve), create the fetch_sources
// row, and mark the suggestion approved — even though the actual network
// fetch of the fake URL will fail in this test environment (#1333 phase 2).
func TestApproveFetchSuggestionExistingOrg(t *testing.T) {
	setupFetchSuggestTestDB(t)

	var orgID int64
	if err := db.QueryRow("INSERT INTO organizations (name) VALUES ('Existing Org') RETURNING id").Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	var existingLocID int64
	if err := db.QueryRow("INSERT INTO locations (location, aliases) VALUES ('Salle Testville', '[]') RETURNING id").Scan(&existingLocID); err != nil {
		t.Fatal(err)
	}
	callerID := 1
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (?, 'member@example.test', 'Member', 'user')", callerID)
	if _, err := db.Exec("INSERT INTO organization_members (organization_id, user_id) VALUES (?, ?)", orgID, callerID); err != nil {
		t.Fatal(err)
	}

	oid := int(orgID)
	mappings := `[{"feed_name":"Salle des Fêtes Testville","matched_location_id":` + strconv.Itoa(int(existingLocID)) + `},` +
		`{"feed_name":"Some New Hall","new_location":{"location":"Some New Hall","town":"Testville","country":"France"}}]`
	id := insertTestPendingSuggestion(t, &oid, "", mappings)

	req := reviewRequest(http.MethodPost, "/api/v1/fetchurl-suggestions/"+strconv.Itoa(id)+"/approve", callerID, RoleUser)
	req.SetPathValue("id", strconv.Itoa(id))
	rec := httptest.NewRecorder()
	approveFetchSuggestionHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var status string
	db.QueryRow("SELECT status FROM pending_fetch_suggestions WHERE id=?", id).Scan(&status)
	if status != "approved" {
		t.Fatalf("status=%q, want approved", status)
	}

	var fetchSourceCount int
	db.QueryRow("SELECT COUNT(*) FROM fetch_sources WHERE url='https://example.com/calendar.ics' AND organization_id=?", orgID).Scan(&fetchSourceCount)
	if fetchSourceCount != 1 {
		t.Fatalf("expected exactly one fetch_sources row, got %d", fetchSourceCount)
	}

	var aliasCount int
	db.QueryRow("SELECT COUNT(*) FROM location_aliases WHERE location_id=? AND alias='Salle des Fêtes Testville'", existingLocID).Scan(&aliasCount)
	if aliasCount != 1 {
		t.Fatalf("expected matched-location alias, got %d", aliasCount)
	}

	var newLocID int
	var newLocErr = db.QueryRow("SELECT id FROM locations WHERE location='Some New Hall'").Scan(&newLocID)
	if newLocErr != nil {
		t.Fatalf("new location was not created: %v", newLocErr)
	}
	db.QueryRow("SELECT COUNT(*) FROM location_aliases WHERE location_id=? AND alias='Some New Hall'", newLocID).Scan(&aliasCount)
	if aliasCount != 1 {
		t.Fatalf("expected new-location alias, got %d", aliasCount)
	}
}

// TestApproveFetchSuggestionNewOrg guards the new-org-proposal path: approval
// must create the organization itself before creating the fetch source.
func TestApproveFetchSuggestionNewOrg(t *testing.T) {
	setupFetchSuggestTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")

	id := insertTestPendingSuggestion(t, nil, "Brand New Org", "")

	req := reviewRequest(http.MethodPost, "/api/v1/fetchurl-suggestions/"+strconv.Itoa(id)+"/approve", 1, RoleAdmin)
	req.SetPathValue("id", strconv.Itoa(id))
	rec := httptest.NewRecorder()
	approveFetchSuggestionHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var orgCount int
	db.QueryRow("SELECT COUNT(*) FROM organizations WHERE name='Brand New Org'").Scan(&orgCount)
	if orgCount != 1 {
		t.Fatalf("expected the new organization to be created, got %d rows", orgCount)
	}
}

// TestApproveFetchSuggestionForbidsNonMember guards the authorization rule:
// a non-admin who isn't a member of the target org cannot approve or reject
// it, and a new-org proposal (no org to check membership against) is
// admin-only regardless of caller.
func TestApproveFetchSuggestionForbidsNonMember(t *testing.T) {
	setupFetchSuggestTestDB(t)

	var orgID int64
	db.QueryRow("INSERT INTO organizations (name) VALUES ('Someone Elses Org') RETURNING id").Scan(&orgID)
	oid := int(orgID)
	id := insertTestPendingSuggestion(t, &oid, "", "")

	nonMemberID := 42
	req := reviewRequest(http.MethodPost, "/api/v1/fetchurl-suggestions/"+strconv.Itoa(id)+"/reject", nonMemberID, RoleUser)
	req.SetPathValue("id", strconv.Itoa(id))
	rec := httptest.NewRecorder()
	rejectFetchSuggestionHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-member reject: status=%d body=%s", rec.Code, rec.Body.String())
	}

	newOrgID := insertTestPendingSuggestion(t, nil, "Another New Org", "")
	req2 := reviewRequest(http.MethodPost, "/api/v1/fetchurl-suggestions/"+strconv.Itoa(newOrgID)+"/reject", nonMemberID, RoleUser)
	req2.SetPathValue("id", strconv.Itoa(newOrgID))
	rec2 := httptest.NewRecorder()
	rejectFetchSuggestionHandler(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("non-admin reject of new-org proposal: status=%d body=%s", rec2.Code, rec2.Body.String())
	}
}

// TestRejectFetchSuggestion guards the plain reject path.
func TestRejectFetchSuggestion(t *testing.T) {
	setupFetchSuggestTestDB(t)

	id := insertTestPendingSuggestion(t, nil, "Org To Reject", "")
	req := reviewRequest(http.MethodPost, "/api/v1/fetchurl-suggestions/"+strconv.Itoa(id)+"/reject", 1, RoleAdmin)
	req.SetPathValue("id", strconv.Itoa(id))
	rec := httptest.NewRecorder()
	rejectFetchSuggestionHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var status string
	db.QueryRow("SELECT status FROM pending_fetch_suggestions WHERE id=?", id).Scan(&status)
	if status != "rejected" {
		t.Fatalf("status=%q, want rejected", status)
	}

	// A second review of an already-rejected row must be refused.
	rec2 := httptest.NewRecorder()
	rejectFetchSuggestionHandler(rec2, req)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("re-reject: status=%d body=%s", rec2.Code, rec2.Body.String())
	}
}
