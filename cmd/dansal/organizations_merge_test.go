package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupOrgMergeDB gives a fresh test DB, a temp banner directory, two
// organisations (keep = 1, merge = 2) and two users.
func setupOrgMergeDB(t *testing.T) {
	t.Helper()
	setupDedupTestDB(t)
	dir := t.TempDir()
	oldDir := orgImagesDir
	orgImagesDir = dir
	t.Cleanup(func() { orgImagesDir = oldDir })
	oldCfg := config
	config = &Config{}
	t.Cleanup(func() { config = oldCfg })
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'a@example.test', 'A', 'admin')")
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (2, 'b@example.test', 'B', 'user')")
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (3, 'c@example.test', 'C', 'user')")
}

func insertOrg(t *testing.T, id int, name, website, desc string, updated int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO organizations (id, name, website, description, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, name, website, desc, updated)
	if err != nil {
		t.Fatalf("insert org %d: %v", id, err)
	}
}

func orgMergeAdminRequest(t *testing.T, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.Header.Set("X-User-ID", "1")
	r.Header.Set("X-User-Role", RoleAdmin)
	r.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	if method == http.MethodGet {
		getOrgMergePreview(w, r)
	} else {
		mergeOrganizations(w, r)
	}
	return w
}

func mergeBody(t *testing.T, keep, merge int, choices map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"keep_id": keep, "merge_id": merge, "choices": choices})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestOrgMergeNewerIsLaterUpdate covers #1644: "newer" is the later
// updated_at, and the preview marks that side.
func TestOrgMergeNewerIsLaterUpdate(t *testing.T) {
	setupOrgMergeDB(t)
	insertOrg(t, 1, "Erlanger Tanzhaus", "https://tanzhaus.example", "", 100)
	insertOrg(t, 2, "Erlangener Tanzhaus", "https://tanzhaus.example", "", 200)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/organizations/1/merge-preview?merge_id=2", nil)
	r.Header.Set("X-User-Role", RoleAdmin)
	r.SetPathValue("id", "1")
	getOrgMergePreview(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Fields []orgMergeField `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var name *orgMergeField
	for i := range got.Fields {
		if got.Fields[i].Field == "name" {
			name = &got.Fields[i]
		}
	}
	if name == nil {
		t.Fatalf("name not compared: %+v", got.Fields)
	}
	if name.Newer != "merge" {
		t.Fatalf("newer = %q, want merge (updated_at 200 > 100)", name.Newer)
	}
	if name.Choice != "" {
		t.Fatalf("name is a conflict, choice must be empty, got %q", name.Choice)
	}
}

// TestOrgMergeNeedsChoiceForConflicts covers the popup contract: a conflicting
// field without a choice is refused, and nothing is changed.
func TestOrgMergeNeedsChoiceForConflicts(t *testing.T) {
	setupOrgMergeDB(t)
	insertOrg(t, 1, "Erlanger Tanzhaus", "https://tanzhaus.example", "", 100)
	insertOrg(t, 2, "Erlangener Tanzhaus", "https://tanzhaus.example", "", 200)

	w := orgMergeAdminRequest(t, http.MethodPost, "/api/v1/organizations/merge", mergeBody(t, 1, 2, map[string]string{}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"name"`) {
		t.Fatalf("missing list should name the conflicting field: %s", w.Body.String())
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM organizations").Scan(&n)
	if n != 2 {
		t.Fatalf("organisations = %d, want 2 (refused merge must not delete)", n)
	}
}

// TestOrgMergeMovesReferencesAndFields covers the merge itself: events,
// members and linked locations move to the survivor, members are the union,
// the chosen values are applied, and the dropped organisation is gone.
func TestOrgMergeMovesReferencesAndFields(t *testing.T) {
	setupOrgMergeDB(t)
	insertOrg(t, 1, "Erlanger Tanzhaus", "https://tanzhaus.example", "Alt", 100)
	insertOrg(t, 2, "Erlangener Tanzhaus", "https://tanzhaus.example", "Neu", 200)
	db.Exec("INSERT INTO organization_members (organization_id, user_id) VALUES (1, 2)")
	db.Exec("INSERT INTO organization_members (organization_id, user_id) VALUES (2, 2)")
	db.Exec("INSERT INTO organization_members (organization_id, user_id) VALUES (2, 3)")
	db.Exec("INSERT INTO locations (id, location) VALUES (10, 'Saal')")
	db.Exec("INSERT INTO location_organizations (location_id, organization_id) VALUES (10, 2)")
	db.Exec("INSERT INTO location_organizations (location_id, organization_id) VALUES (10, 1)")
	insertTestEventForOrg(t, 2)

	w := orgMergeAdminRequest(t, http.MethodPost, "/api/v1/organizations/merge",
		mergeBody(t, 1, 2, map[string]string{"name": "merge", "description": "merge"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var name, desc string
	db.QueryRow("SELECT name, description FROM organizations WHERE id = 1").Scan(&name, &desc)
	if name != "Erlangener Tanzhaus" || desc != "Neu" {
		t.Fatalf("survivor = (%q, %q), want the chosen newer values", name, desc)
	}
	var gone int
	db.QueryRow("SELECT COUNT(*) FROM organizations WHERE id = 2").Scan(&gone)
	if gone != 0 {
		t.Fatal("dropped organisation still exists")
	}
	var members, locLinks, events int
	db.QueryRow("SELECT COUNT(*) FROM organization_members WHERE organization_id = 1").Scan(&members)
	db.QueryRow("SELECT COUNT(*) FROM location_organizations WHERE organization_id = 1").Scan(&locLinks)
	db.QueryRow("SELECT COUNT(*) FROM events WHERE organization_id = 1").Scan(&events)
	if members != 2 {
		t.Fatalf("members = %d, want 2 (union of users 2 and 3)", members)
	}
	if locLinks != 1 {
		t.Fatalf("location links = %d, want 1 (duplicate ignored)", locLinks)
	}
	if events != 1 {
		t.Fatalf("events on survivor = %d, want 1", events)
	}
}

// insertTestEventForOrg inserts one published event owned by org.
func insertTestEventForOrg(t *testing.T, org int) {
	t.Helper()
	const start = int64(2000000000)
	if _, _, _, err := insertEvent(db, EventInput{
		Title: "Ball", StartTime: start, EndTime: start + 3600, IsPublished: true,
		OrganizationID: &org,
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

// TestOrgMergeBannerChoice covers the banner in the per-field choice: when
// both organisations have one, the chosen file ends up at the survivor's path.
func TestOrgMergeBannerChoice(t *testing.T) {
	setupOrgMergeDB(t)
	insertOrg(t, 1, "Erlanger Tanzhaus", "", "", 100)
	insertOrg(t, 2, "Erlangener Tanzhaus", "", "", 200)
	if err := os.WriteFile(filepath.Join(orgImagesDir, "1.jpeg"), []byte("keep-banner"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orgImagesDir, "2.jpeg"), []byte("merge-banner"), 0o644); err != nil {
		t.Fatal(err)
	}
	orgImgCache.add(1, "image/jpeg")
	orgImgCache.add(2, "image/jpeg")

	w := orgMergeAdminRequest(t, http.MethodPost, "/api/v1/organizations/merge",
		mergeBody(t, 1, 2, map[string]string{"name": "merge", "banner": "merge"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(orgImagesDir, "1.jpeg"))
	if err != nil || string(got) != "merge-banner" {
		t.Fatalf("survivor banner = %q, %v; want the merged org's banner", got, err)
	}
	if _, err := os.Stat(filepath.Join(orgImagesDir, "2.jpeg")); !os.IsNotExist(err) {
		t.Fatal("dropped organisation's banner file should be removed")
	}
}

// TestOrgMergeCoversEveryOrgForeignKey fails when the schema gains a table
// with a foreign key to organizations that the merge does not handle.
func TestOrgMergeCoversEveryOrgForeignKey(t *testing.T) {
	setupDedupTestDB(t)
	rows, err := db.Query(`SELECT m.name, p."from" FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) p
		WHERE m.type = 'table' AND p."table" = 'organizations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	handled := map[string]bool{}
	for _, ref := range orgMergeRefTables {
		handled[ref.table+"."+ref.column] = true
	}
	for rows.Next() {
		var tbl, col string
		rows.Scan(&tbl, &col)
		if !handled[tbl+"."+col] {
			t.Errorf("foreign key %s.%s -> organizations is not handled by the org merge", tbl, col)
		}
	}
}
