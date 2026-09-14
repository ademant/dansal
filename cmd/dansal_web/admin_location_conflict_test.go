package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestAdminLocationCreateConflictOffersResolutionActions covers #1302: when
// creating a new location hits a 409 (OSM-ID or, since #1302, geohash
// collision), the re-rendered form must offer the two real next steps —
// assign the org(s) the admin had checked to the existing location, or add
// the new location as a room under it — instead of just a dead-end "already
// exists" message. Both must carry the exact org ids that were checked on
// the abandoned submission.
func TestAdminLocationCreateConflictOffersResolutionActions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/locations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"error": "location already exists", "existing_id": 42})
	})
	mux.HandleFunc("GET /api/v1/locations/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "location": "Existing Hall"})
	})
	mux.HandleFunc("GET /api/v1/organizations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 5, "name": "Folk Club"},
			{"id": 9, "name": "Dance Society"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dbConn := initDB(":memory:")
	defer dbConn.Close()
	siteCfg = newSiteSettingsCache(dbConn)

	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test"}
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	handler := adminLocationCreateHandler(cfg, tmpls, client, i18n)

	form := strings.NewReader("location=New+Hall&organization_ids=5&organization_ids=9")
	req := httptest.NewRequest(http.MethodPost, "/admin/locations/new", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Existing Hall") {
		t.Errorf("rendered page does not name the conflicting location: %s", body)
	}
	if !strings.Contains(body, `action="/admin/locations/42/assign-orgs"`) {
		t.Errorf("missing the assign-orgs resolution form")
	}
	if !strings.Contains(body, `action="/admin/locations/42/rooms/new"`) {
		t.Errorf("missing the create-room resolution form")
	}
	if !strings.Contains(body, `name="organization_ids" value="5"`) || !strings.Contains(body, `name="organization_ids" value="9"`) {
		t.Errorf("resolution forms don't carry the checked org ids through: %s", body)
	}
	if strings.Contains(body, "loc_merge_confirm") || strings.Contains(body, `action="/admin/locations/merge"`) {
		t.Errorf("a fresh create-conflict (no existing Location.ID) should not offer the edit-only merge action")
	}
}

// TestAdminLocationConflictAssignOrgsHandler covers the "assign my org(s) to
// this location instead" resolution action: it must bulk-assign every
// checked org — not just the first — to the existing location.
func TestAdminLocationConflictAssignOrgsHandler(t *testing.T) {
	var assigned []int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/locations/bulk-assign-org", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs            []int `json:"ids"`
			OrganizationID int   `json:"organization_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.IDs) != 1 || body.IDs[0] != 42 {
			t.Errorf("bulk-assign-org ids = %v, want [42]", body.IDs)
		}
		assigned = append(assigned, body.OrganizationID)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &Config{Domain: "example.test"}
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	handler := adminLocationConflictAssignOrgsHandler(cfg, client)

	form := strings.NewReader("organization_ids=5&organization_ids=9")
	req := httptest.NewRequest(http.MethodPost, "/admin/locations/42/assign-orgs", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "42")
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/locations/42/edit" {
		t.Errorf("redirect target = %q, want /admin/locations/42/edit", loc)
	}
	if len(assigned) != 2 || assigned[0] != 5 || assigned[1] != 9 {
		t.Errorf("assigned org ids = %v, want [5 9]", assigned)
	}
}

// TestAdminLocationRoomCreateHandlerAssignsOrgs covers the "add as a room in
// this building instead" resolution action: after creating the child
// location, any organization_ids carried over from the abandoned
// new-location form must be assigned to the new room (a room otherwise has
// no org checkboxes of its own on the plain "add room" form).
func TestAdminLocationRoomCreateHandlerAssignsOrgs(t *testing.T) {
	var assignedTo, assignedOrg int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/locations/17/children", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": 99, "location": "Small Room"})
	})
	mux.HandleFunc("POST /api/v1/locations/bulk-assign-org", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs            []int `json:"ids"`
			OrganizationID int   `json:"organization_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.IDs) == 1 {
			assignedTo = body.IDs[0]
		}
		assignedOrg = body.OrganizationID
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &Config{Domain: "example.test"}
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	handler := adminLocationRoomCreateHandler(cfg, client)

	form := strings.NewReader("name=Small+Room&organization_ids=5")
	req := httptest.NewRequest(http.MethodPost, "/admin/locations/17/rooms/new", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "17")
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if assignedTo != 99 {
		t.Errorf("org was assigned to location %d, want the new room's id (99)", assignedTo)
	}
	if assignedOrg != 5 {
		t.Errorf("assigned org id = %d, want 5", assignedOrg)
	}
}

// TestAdminLocationMergeHandlerRedirect covers a real prod bug: merging from
// the edit page's duplicate-conflict banner used to redirect via the Referer
// header, whose value is the edit URL for the location merge just deleted —
// sending the admin straight back to "editing" a now-gone location. The
// conflict-banner merge form now submits a return field (carrying the edit
// page's own ?return=, same one admin_locations_maintenance.html links with)
// which must take priority; the two bulk-select-and-merge list pages don't
// submit one and must keep working exactly as before (Referer-based).
func TestAdminLocationMergeHandlerRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/locations/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": id})
	})
	mux.HandleFunc("PATCH /api/v1/locations/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
	})
	mux.HandleFunc("DELETE /api/v1/locations/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &Config{Domain: "example.test"}
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	handler := adminLocationMergeHandler(cfg, client)

	newReq := func(body string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/admin/locations/merge", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
	}

	t.Run("edit page's conflict-merge form: return field wins over Referer", func(t *testing.T) {
		req := newReq("loc_ids=257&loc_ids=42&return=%2Fadmin%2Flocations%2Fmaintenance")
		req.Header.Set("Referer", "https://example.test/admin/locations/257/edit?return=%2Fadmin%2Flocations%2Fmaintenance")

		rec := httptest.NewRecorder()
		handler(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status=%d, want 303; body=%s", rec.Code, rec.Body.String())
		}
		if loc := rec.Header().Get("Location"); loc != "/admin/locations/maintenance" {
			t.Errorf("redirect target = %q, want /admin/locations/maintenance (not the deleted location's own edit page)", loc)
		}
	})

	t.Run("bulk-select merge from a list page: no return field, falls back to Referer", func(t *testing.T) {
		req := newReq("loc_ids=257&loc_ids=42")
		req.Header.Set("Referer", "https://example.test/admin/locations")

		rec := httptest.NewRecorder()
		handler(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status=%d, want 303; body=%s", rec.Code, rec.Body.String())
		}
		if loc := rec.Header().Get("Location"); loc != "/admin/locations" {
			t.Errorf("redirect target = %q, want /admin/locations (existing Referer-based behavior)", loc)
		}
	})
}
