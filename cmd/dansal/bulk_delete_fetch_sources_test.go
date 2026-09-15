package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBulkDeleteFetchSourcesRespectsOrgMembership covers #1316: batching the
// per-id org-membership check (bulkDeleteFetchSources used to run one SELECT
// per id) into a single WHERE id IN (...) lookup must not change which
// fetch sources a non-admin caller is allowed to delete — only ones
// belonging to an org the caller is a member of, plus a nonexistent id in
// the request is still silently ignored rather than erroring.
func TestBulkDeleteFetchSourcesRespectsOrgMembership(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (2, 'publisher@example.test', 'Publisher', 'user')")
	db.Exec("INSERT INTO organizations (id, name) VALUES (1, 'My Org'), (2, 'Other Org')")
	db.Exec("INSERT INTO organization_members (organization_id, user_id) VALUES (1, 2)")

	// tags must be a non-NULL string — scanFetchSource scans it into a plain
	// (non-nullable) string field.
	res, _ := db.Exec("INSERT INTO fetch_sources (url, organization_id, tags) VALUES ('https://example.test/mine.ics', 1, '')")
	mineID64, _ := res.LastInsertId()
	mineID := int(mineID64)
	res, _ = db.Exec("INSERT INTO fetch_sources (url, organization_id, tags) VALUES ('https://example.test/other.ics', 2, '')")
	otherID64, _ := res.LastInsertId()
	otherID := int(otherID64)

	const missingID = 999999
	body, _ := json.Marshal(map[string]any{"ids": []int{mineID, otherID, missingID}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fetchurl/bulk-delete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "2")
	req.Header.Set("X-User-Role", "user")
	w := httptest.NewRecorder()
	bulkDeleteFetchSources(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}

	var mineExists, otherExists int
	db.QueryRow("SELECT COUNT(*) FROM fetch_sources WHERE id=?", mineID).Scan(&mineExists)
	db.QueryRow("SELECT COUNT(*) FROM fetch_sources WHERE id=?", otherID).Scan(&otherExists)
	if mineExists != 0 {
		t.Error("fetch source belonging to the caller's own org was not deleted")
	}
	if otherExists != 1 {
		t.Error("fetch source belonging to a different org was deleted (or lost) — membership check was bypassed")
	}
}
