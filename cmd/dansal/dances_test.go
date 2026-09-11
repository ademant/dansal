package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// setupDancesTestDB swaps the package-level db for a fresh in-memory one with
// the full schema, restoring the original on cleanup.
func setupDancesTestDB(t *testing.T) {
	t.Helper()
	old := db
	t.Cleanup(func() { db = old })
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	db = conn
	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
}

// TestDanceDescriptionCRUD covers #1290: a dance's free-text description
// round-trips through create, list, and the new PUT update endpoint (which
// didn't exist before this issue — dances only supported create+delete).
func TestDanceDescriptionCRUD(t *testing.T) {
	setupDancesTestDB(t)

	adminReq := func(method, path string, body any) *http.Request {
		var buf bytes.Buffer
		if body != nil {
			json.NewEncoder(&buf).Encode(body)
		}
		r := httptest.NewRequest(method, path, &buf)
		r.Header.Set("X-User-ID", "1")
		r.Header.Set("X-User-Role", RoleAdmin)
		return r
	}

	// Create with a description.
	rec := httptest.NewRecorder()
	createDance(rec, adminReq(http.MethodPost, "/api/v1/dances", map[string]string{
		"name": "An Dro", "description": "A Breton chain dance in 8-count phrases.",
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created Dance
	json.NewDecoder(rec.Body).Decode(&created)
	if created.Description != "A Breton chain dance in 8-count phrases." {
		t.Fatalf("created dance description = %q", created.Description)
	}

	// List reflects the description.
	rec = httptest.NewRecorder()
	getDances(rec, httptest.NewRequest(http.MethodGet, "/api/v1/dances", nil))
	var listed []Dance
	json.NewDecoder(rec.Body).Decode(&listed)
	if len(listed) != 1 || listed[0].Description != created.Description {
		t.Fatalf("listed dances = %+v", listed)
	}

	// Update via the new PUT endpoint.
	req := adminReq(http.MethodPut, "/api/v1/dances/1", map[string]string{
		"name": "An Dro", "description": "Updated description.",
	})
	req.SetPathValue("id", "1")
	rec = httptest.NewRecorder()
	updateDance(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated Dance
	json.NewDecoder(rec.Body).Decode(&updated)
	if updated.Description != "Updated description." {
		t.Fatalf("updated dance description = %q, want %q", updated.Description, "Updated description.")
	}

	// Non-admin cannot update.
	nonAdminReq := adminReq(http.MethodPut, "/api/v1/dances/1", map[string]string{"name": "An Dro", "description": "x"})
	nonAdminReq.Header.Set("X-User-Role", "user")
	nonAdminReq.SetPathValue("id", "1")
	rec = httptest.NewRecorder()
	updateDance(rec, nonAdminReq)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin update status=%d, want 403", rec.Code)
	}

	// Updating a nonexistent dance 404s.
	req = adminReq(http.MethodPut, "/api/v1/dances/999", map[string]string{"name": "X", "description": "y"})
	req.SetPathValue("id", "999")
	rec = httptest.NewRecorder()
	updateDance(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("update nonexistent status=%d, want 404", rec.Code)
	}
}
