package main

import (
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestGetEventsRepeatedOrganizationIDFilter covers #1365: organization_id on
// GET /api/v1/events must accept either repeated params or a comma-separated
// list, matching any of the given orgs, capped at maxOrgIDFilter values.
func TestGetEventsRepeatedOrganizationIDFilter(t *testing.T) {
	setupDedupTestDB(t)

	db.Exec("INSERT INTO organizations (id, name) VALUES (1, 'Org 1'), (2, 'Org 2'), (3, 'Org 3')")

	org1, org2, org3 := 1, 2, 3
	for i, orgID := range []*int{&org1, &org2, &org3} {
		// Distinct titles/times: identical title+start_time with no location
		// would otherwise collide with dedup tier 4 (title+start_time) and
		// collapse all three into one event.
		start := int64(2000000000 + i*7200)
		if _, _, _, err := insertEvent(db, EventInput{
			Title: "Event " + strconv.Itoa(i), StartTime: start, EndTime: start + 3600,
			IsPublished: true, OrganizationID: orgID,
		}); err != nil {
			t.Fatalf("insert event for org %d: %v", *orgID, err)
		}
	}

	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"repeated params", "?organization_id=1&organization_id=2", 2},
		{"comma-separated", "?organization_id=1,3", 2},
		{"single value still works", "?organization_id=2", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/events"+tc.query, nil)
			w := httptest.NewRecorder()
			getEvents(w, req)
			if w.Code != 200 {
				t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
			}
			want := strconv.Itoa(tc.want)
			if got := w.Header().Get("X-Total-Count"); got != want {
				t.Errorf("X-Total-Count = %q, want %q (body=%s)", got, want, w.Body.String())
			}
		})
	}
}

// TestGetEventsAuthedListETag covers #1364: the authenticated (role/org-scoped)
// branch of GET /api/v1/events emits a private ETag and honors If-None-Match
// with a 304, distinct from the public branch's own whole-table fingerprint.
func TestGetEventsAuthedListETag(t *testing.T) {
	setupDedupTestDB(t)

	db.Exec("INSERT INTO organizations (id, name) VALUES (1, 'Org 1')")

	org1 := 1
	if _, _, _, err := insertEvent(db, EventInput{
		Title: "Event", StartTime: 2000000000, EndTime: 2000003600,
		IsPublished: true, OrganizationID: &org1,
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/events?organization_id=1", nil)
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	w := httptest.NewRecorder()
	getEvents(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag on authenticated list response")
	}
	if cc := w.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Errorf("Cache-Control = %q, want %q", cc, "private, no-cache")
	}

	req2 := httptest.NewRequest("GET", "/api/v1/events?organization_id=1", nil)
	req2.Header.Set("X-User-ID", "1")
	req2.Header.Set("X-User-Role", RoleAdmin)
	req2.Header.Set("If-None-Match", etag)
	w2 := httptest.NewRecorder()
	getEvents(w2, req2)
	if w2.Code != 304 {
		t.Fatalf("second request with matching If-None-Match: status = %d, want 304", w2.Code)
	}
}
