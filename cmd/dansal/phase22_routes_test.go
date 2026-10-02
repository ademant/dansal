package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// Phase-22 (#1379/#1380/#1381): canonical routes and their deprecated aliases.

func phase22Request(t *testing.T, h http.HandlerFunc, method, role string, callerID int, body any, pathValues map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, "/", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Role", role)
	req.Header.Set("X-User-ID", strconv.Itoa(callerID))
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func insertTestEvent(t *testing.T, title string, orgID sql.NullInt64) int {
	t.Helper()
	res, err := db.Exec("INSERT INTO events (title, description, start_time, end_time, organization_id) VALUES (?, '', 1900000000, 1900003600, ?)", title, orgID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

func eventOrg(t *testing.T, id int) sql.NullInt64 {
	t.Helper()
	org, err := eventOrgID(db, id)
	if err != nil {
		t.Fatal(err)
	}
	return org
}

func TestDeprecatedAliasHeaders(t *testing.T) {
	h := deprecatedAlias("/api/v1/locations/{id}/organizations/{org_id}", func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("org_id", "9") // set late, like the body-based aliases
		w.WriteHeader(http.StatusNoContent)
	})
	rec := phase22Request(t, h, http.MethodPost, RoleAdmin, 1, nil, map[string]string{"id": "4"})
	if got := rec.Header().Get("Deprecation"); got != apiDeprecatedSince {
		t.Errorf("Deprecation = %q", got)
	}
	if got := rec.Header().Get("Link"); got != `</api/v1/locations/4/organizations/9>; rel="successor-version"` {
		t.Errorf("Link = %q", got)
	}
}

func TestSyndicateEventTargets(t *testing.T) {
	_, _, orgA, _ := setupOrgClaimTestDB(t)
	ev := insertTestEvent(t, "Synd", sql.NullInt64{Int64: int64(orgA), Valid: true})
	pv := map[string]string{"id": strconv.Itoa(ev)}

	if rec := phase22Request(t, syndicateEvent, http.MethodPost, RoleAdmin, 1, EventSyndicateRequest{Target: "myspace"}, pv); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown target: %d, want 400", rec.Code)
	}
	for _, target := range []string{syndicationTargetEventbrite, syndicationTargetSocialDanceToday} {
		// Not configured on the org → 422, from the new route and the alias alike.
		rec := phase22Request(t, syndicateEvent, http.MethodPost, RoleAdmin, 1, EventSyndicateRequest{Target: target}, pv)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not configured") {
			t.Errorf("%s via /syndication: %d %s", target, rec.Code, rec.Body.String())
		}
		rec = phase22Request(t, syndicateEventLegacy(target), http.MethodPost, RoleAdmin, 1, nil, pv)
		if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("Deprecation") == "" {
			t.Errorf("%s via legacy route: %d, Deprecation=%q", target, rec.Code, rec.Header().Get("Deprecation"))
		}
	}
}

func TestBulkAssignEventOrg(t *testing.T) {
	userA, _, orgA, orgB := setupOrgClaimTestDB(t)
	orphan := insertTestEvent(t, "Orphan", sql.NullInt64{})
	own := insertTestEvent(t, "Own", sql.NullInt64{Int64: int64(orgA), Valid: true})
	foreign := insertTestEvent(t, "Foreign", sql.NullInt64{Int64: int64(orgB), Valid: true})
	ids := []int{orphan, own, foreign}

	// A user can't move events into an org they don't belong to, nor clear.
	if rec := phase22Request(t, bulkAssignEventOrg, http.MethodPost, RoleUser, userA, BulkAssignOrgRequest{IDs: ids, OrganizationID: &orgB}, nil); rec.Code != http.StatusForbidden {
		t.Errorf("non-member target: %d, want 403", rec.Code)
	}
	if rec := phase22Request(t, bulkAssignEventOrg, http.MethodPost, RoleUser, userA, BulkAssignOrgRequest{IDs: ids}, nil); rec.Code != http.StatusForbidden {
		t.Errorf("user clearing: %d, want 403", rec.Code)
	}

	// Into their own org: the orphan is claimed, the foreign event skipped.
	if rec := phase22Request(t, bulkAssignEventOrg, http.MethodPost, RoleUser, userA, BulkAssignOrgRequest{IDs: ids, OrganizationID: &orgA}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("bulk assign: %d %s", rec.Code, rec.Body.String())
	}
	if got := eventOrg(t, orphan); got.Int64 != int64(orgA) {
		t.Errorf("orphan org = %v, want %d", got, orgA)
	}
	if got := eventOrg(t, foreign); got.Int64 != int64(orgB) {
		t.Errorf("foreign event moved to %v", got)
	}

	// Admin may clear.
	if rec := phase22Request(t, bulkAssignEventOrg, http.MethodPost, RoleAdmin, 1, BulkAssignOrgRequest{IDs: []int{own}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("admin clear: %d", rec.Code)
	}
	if eventOrg(t, own).Valid {
		t.Error("admin clear left the organization set")
	}
}

// The deprecated POST /events/{id}/assign-org now runs through PUT
// /events/{id}/organization, including a publisher claiming an orphan.
func TestAssignEventOrgAlias(t *testing.T) {
	userA, _, orgA, orgB := setupOrgClaimTestDB(t)
	orphan := insertTestEvent(t, "Orphan", sql.NullInt64{})
	pv := map[string]string{"id": strconv.Itoa(orphan)}

	if rec := phase22Request(t, assignEventOrg, http.MethodPost, RolePublisher, userA, map[string]int{"org_id": orgB}, pv); rec.Code != http.StatusForbidden {
		t.Errorf("claim into non-member org: %d, want 403", rec.Code)
	}
	rec := phase22Request(t, assignEventOrg, http.MethodPost, RolePublisher, userA, map[string]int{"org_id": orgA}, pv)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("claim: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Link"), "/api/v1/events/"+strconv.Itoa(orphan)+"/organization") {
		t.Errorf("Link = %q", rec.Header().Get("Link"))
	}
	if got := eventOrg(t, orphan); got.Int64 != int64(orgA) {
		t.Errorf("org = %v, want %d", got, orgA)
	}
}

func TestLocationOrganizationRoutes(t *testing.T) {
	userA, _, orgA, orgB := setupOrgClaimTestDB(t)
	res, err := db.Exec("INSERT INTO locations (location) VALUES ('Hall')")
	if err != nil {
		t.Fatal(err)
	}
	locID64, _ := res.LastInsertId()
	loc := strconv.Itoa(int(locID64))
	linked := func(org int) bool {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM location_organizations WHERE location_id=? AND organization_id=?", loc, org).Scan(&n)
		return n > 0
	}

	pvA := map[string]string{"id": loc, "org_id": strconv.Itoa(orgA)}
	pvB := map[string]string{"id": loc, "org_id": strconv.Itoa(orgB)}
	if rec := phase22Request(t, addLocationOrganization, http.MethodPut, RoleUser, userA, nil, pvB); rec.Code != http.StatusForbidden {
		t.Errorf("link to non-member org: %d, want 403", rec.Code)
	}
	if rec := phase22Request(t, addLocationOrganization, http.MethodPut, RoleUser, userA, nil, pvA); rec.Code != http.StatusNoContent || !linked(orgA) {
		t.Fatalf("link: %d linked=%v", rec.Code, linked(orgA))
	}
	if rec := phase22Request(t, addLocationOrganization, http.MethodPut, RoleUser, userA, nil, pvA); rec.Code != http.StatusNoContent {
		t.Errorf("link again (idempotent): %d", rec.Code)
	}
	if rec := phase22Request(t, removeLocationOrganization, http.MethodDelete, RoleUser, userA, nil, pvA); rec.Code != http.StatusNoContent || linked(orgA) {
		t.Fatalf("unlink: %d linked=%v", rec.Code, linked(orgA))
	}

	// Deprecated aliases.
	if rec := phase22Request(t, assignLocationOrg, http.MethodPost, RoleAdmin, 1, map[string]int{"organization_id": orgB}, map[string]string{"id": loc}); rec.Code != http.StatusNoContent || !linked(orgB) {
		t.Fatalf("assign-org alias: %d linked=%v", rec.Code, linked(orgB))
	}
	rec := phase22Request(t, unassignLocationOrg, http.MethodPost, RoleAdmin, 1, map[string]int{"location_id": int(locID64), "organization_id": orgB}, nil)
	if rec.Code != http.StatusNoContent || linked(orgB) {
		t.Fatalf("unassign-org alias: %d linked=%v", rec.Code, linked(orgB))
	}
	if want := "</api/v1/locations/" + loc + "/organizations/" + strconv.Itoa(orgB) + ">"; !strings.HasPrefix(rec.Header().Get("Link"), want) {
		t.Errorf("Link = %q, want prefix %q", rec.Header().Get("Link"), want)
	}
}
