package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeMediaLinks(t *testing.T) {
	good, err := normalizeMediaLinks([]MediaLink{
		{Kind: "Video", Title: "  Live  ", URL: " https://www.youtube.com/watch?v=abc "},
		{URL: ""}, // blank row dropped
		{URL: "https://example.org/a.mp3"},
	})
	if err != nil {
		t.Fatalf("valid list rejected: %v", err)
	}
	if len(good) != 2 {
		t.Fatalf("len = %d, want 2 (blank dropped)", len(good))
	}
	if good[0].Kind != "video" || good[0].Title != "Live" || good[0].URL != "https://www.youtube.com/watch?v=abc" {
		t.Errorf("first not normalized: %+v", good[0])
	}
	if good[1].Kind != "other" {
		t.Errorf("empty kind should default to other, got %q", good[1].Kind)
	}

	bad := map[string]MediaLink{
		"http":        {URL: "http://example.org/x"},
		"javascript":  {URL: "javascript:alert(1)"},
		"no host":     {URL: "https://"},
		"credentials": {URL: "https://user:pw@example.org/"},
		"relative":    {URL: "/local/path"},
		"bad kind":    {Kind: "embed", URL: "https://example.org/"},
		"long title":  {Title: strings.Repeat("x", maxMediaTitleLen+1), URL: "https://example.org/"},
		"long url":    {URL: "https://example.org/" + strings.Repeat("a", maxMediaURLLen)},
	}
	for name, l := range bad {
		if _, err := normalizeMediaLinks([]MediaLink{l}); err == nil {
			t.Errorf("%s: expected error for %+v", name, l)
		}
	}

	many := make([]MediaLink, maxMediaLinksPerOwner+1)
	for i := range many {
		many[i] = MediaLink{URL: "https://example.org/"}
	}
	if _, err := normalizeMediaLinks(many); err == nil {
		t.Error("expected error above the per-owner cap")
	}
}

func TestOwnerMediaReplaceKeepsOrderAndIsolatesOwners(t *testing.T) {
	setupDedupTestDB(t)

	first := []MediaLink{
		{Kind: "video", Title: "B", URL: "https://example.org/b"},
		{Kind: "audio", Title: "A", URL: "https://example.org/a"},
	}
	if err := replaceOwnerMedia(db, ownerTypeMusician, 1, first); err != nil {
		t.Fatal(err)
	}
	if err := replaceOwnerMedia(db, ownerTypeMusician, 2, []MediaLink{{Kind: "image", URL: "https://example.org/other"}}); err != nil {
		t.Fatal(err)
	}

	got := loadOwnerMedia(db, ownerTypeMusician, 1)
	if len(got) != 2 || got[0].Title != "B" || got[1].Title != "A" {
		t.Fatalf("order not kept: %+v", got)
	}

	// Replace reorders/shrinks; the other owner is untouched.
	if err := replaceOwnerMedia(db, ownerTypeMusician, 1, []MediaLink{first[1]}); err != nil {
		t.Fatal(err)
	}
	if got := loadOwnerMedia(db, ownerTypeMusician, 1); len(got) != 1 || got[0].Title != "A" {
		t.Fatalf("replace failed: %+v", got)
	}
	if got := loadOwnerMedia(db, ownerTypeMusician, 2); len(got) != 1 {
		t.Fatalf("other owner affected: %+v", got)
	}

	deleteOwnerMedia(db, ownerTypeMusician, 1)
	if got := loadOwnerMedia(db, ownerTypeMusician, 1); len(got) != 0 {
		t.Fatalf("delete failed: %+v", got)
	}
}

func mediaTestRequest(method, target, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("X-User-ID", "1")
	r.Header.Set("X-User-Role", RoleAdmin)
	return r
}

func TestMusicianMediaAPIRoundTrip(t *testing.T) {
	setupDedupTestDB(t)
	if musicianAvatars == nil {
		musicianAvatars = newAvatarSet(t.TempDir(), "/api/v1/musician-avatars/")
	}
	if _, err := db.Exec("INSERT INTO users (id, email, role) VALUES (1, 'admin@example.org', 'admin')"); err != nil {
		t.Fatal(err)
	}

	// Create with links.
	w := httptest.NewRecorder()
	createMusician(w, mediaTestRequest("POST", "/api/v1/musicians",
		`{"bandname":"Linky","media":[{"kind":"video","title":"Live","url":"https://youtu.be/x"},{"kind":"audio","url":"https://example.org/a.mp3"}]}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var created []Musician
	json.Unmarshal(w.Body.Bytes(), &created)
	id := created[0].ID
	if len(created[0].Media) != 2 {
		t.Fatalf("create response media = %+v", created[0].Media)
	}

	get := func() Musician {
		r := httptest.NewRequest("GET", "/api/v1/musicians/1", nil)
		r.SetPathValue("id", "1")
		rw := httptest.NewRecorder()
		getMusician(rw, r)
		var m Musician
		json.Unmarshal(rw.Body.Bytes(), &m)
		return m
	}
	_ = id
	if m := get(); len(m.Media) != 2 || m.Media[0].Title != "Live" {
		t.Fatalf("GET media = %+v", m.Media)
	}

	put := func(body string) *httptest.ResponseRecorder {
		r := mediaTestRequest("PUT", "/api/v1/musicians/1", body)
		r.SetPathValue("id", "1")
		rw := httptest.NewRecorder()
		updateMusician(rw, r)
		return rw
	}

	// PUT without "media" must leave the list alone (older clients).
	if rw := put(`{"bandname":"Linky"}`); rw.Code != http.StatusOK {
		t.Fatalf("PUT = %d", rw.Code)
	}
	if m := get(); len(m.Media) != 2 {
		t.Fatalf("omitted media wiped the list: %+v", m.Media)
	}

	// An invalid URL is rejected and changes nothing.
	if rw := put(`{"bandname":"Renamed","media":[{"url":"http://insecure.example/"}]}`); rw.Code != http.StatusBadRequest {
		t.Fatalf("http:// link = %d, want 400", rw.Code)
	}
	if m := get(); m.Bandname != "Linky" || len(m.Media) != 2 {
		t.Fatalf("rejected update leaked: %+v", m)
	}

	// "media": [] clears.
	if rw := put(`{"bandname":"Linky","media":[]}`); rw.Code != http.StatusOK {
		t.Fatalf("clear = %d", rw.Code)
	}
	if m := get(); len(m.Media) != 0 {
		t.Fatalf("not cleared: %+v", m.Media)
	}

	// Deleting the musician removes its links.
	replaceOwnerMedia(db, ownerTypeMusician, 1, []MediaLink{{Kind: "video", URL: "https://example.org/v"}})
	dr := mediaTestRequest("DELETE", "/api/v1/musicians/1", "")
	dr.SetPathValue("id", "1")
	deleteMusician(httptest.NewRecorder(), dr)
	if got := loadOwnerMedia(db, ownerTypeMusician, 1); len(got) != 0 {
		t.Fatalf("media survived owner delete: %+v", got)
	}
}

func TestOrganizationAndLocationMediaAPI(t *testing.T) {
	setupDedupTestDB(t)
	if orgAvatars == nil {
		orgAvatars = newAvatarSet(t.TempDir(), "/api/v1/org-avatars/")
	}
	if _, err := db.Exec("INSERT INTO users (id, email, role) VALUES (1, 'admin@example.org', 'admin')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO organizations (id, name) VALUES (1, 'Org')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO locations (id, location) VALUES (1, 'Hall')"); err != nil {
		t.Fatal(err)
	}
	links := `[{"kind":"video","title":"Tour","url":"https://example.org/tour"},{"kind":"image","url":"https://example.org/p.jpg"}]`

	do := func(h http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
		r := mediaTestRequest(method, target, body)
		r.SetPathValue("id", "1")
		rw := httptest.NewRecorder()
		h(rw, r)
		return rw
	}

	// --- organization ---
	if rw := do(updateOrganization, "PUT", "/api/v1/organizations/1", `{"name":"Org","media":`+links+`}`); rw.Code != http.StatusOK {
		t.Fatalf("org PUT = %d: %s", rw.Code, rw.Body.String())
	}
	var org Organization
	json.Unmarshal(do(getOrganization, "GET", "/api/v1/organizations/1", "").Body.Bytes(), &org)
	if len(org.Media) != 2 || org.Media[0].Title != "Tour" {
		t.Fatalf("org GET media = %+v", org.Media)
	}
	// omitted -> untouched; bad url -> 400 and unchanged
	do(updateOrganization, "PUT", "/api/v1/organizations/1", `{"name":"Org"}`)
	if rw := do(updateOrganization, "PUT", "/api/v1/organizations/1", `{"name":"Org","media":[{"url":"ftp://x/y"}]}`); rw.Code != http.StatusBadRequest {
		t.Fatalf("org bad url = %d", rw.Code)
	}
	if got := loadOwnerMedia(db, ownerTypeOrganization, 1); len(got) != 2 {
		t.Fatalf("org media disturbed: %+v", got)
	}
	do(deleteOrganization, "DELETE", "/api/v1/organizations/1", "")
	if got := loadOwnerMedia(db, ownerTypeOrganization, 1); len(got) != 0 {
		t.Fatalf("org media survived delete: %+v", got)
	}

	// --- location ---
	if rw := do(putLocation, "PUT", "/api/v1/locations/1", `{"location":"Hall","media":`+links+`}`); rw.Code != http.StatusOK {
		t.Fatalf("location PUT = %d: %s", rw.Code, rw.Body.String())
	}
	var loc Location
	json.Unmarshal(do(getLocation, "GET", "/api/v1/locations/1", "").Body.Bytes(), &loc)
	if len(loc.Media) != 2 || loc.Media[1].Kind != "image" {
		t.Fatalf("location GET media = %+v", loc.Media)
	}
	if rw := do(putLocation, "PUT", "/api/v1/locations/1", `{"location":"Hall","media":[{"url":"http://insecure/"}]}`); rw.Code != http.StatusBadRequest {
		t.Fatalf("location bad url = %d", rw.Code)
	}
	if got := loadOwnerMedia(db, ownerTypeLocation, 1); len(got) != 2 {
		t.Fatalf("location media disturbed: %+v", got)
	}
}

func TestDeleteLocationRemovesRoomMediaAndMergeFoldsMedia(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, role) VALUES (1, 'admin@example.org', 'admin')")
	db.Exec("INSERT INTO locations (id, location) VALUES (1, 'Building'), (3, 'Other')")
	db.Exec("INSERT INTO locations (id, location, parent_id) VALUES (2, 'Room', 1)")
	replaceOwnerMedia(db, ownerTypeLocation, 1, []MediaLink{{Kind: "other", URL: "https://example.org/b"}})
	replaceOwnerMedia(db, ownerTypeLocation, 2, []MediaLink{{Kind: "other", URL: "https://example.org/r"}})

	// Deleting the building takes its room's links with it.
	r := mediaTestRequest("DELETE", "/api/v1/locations/1", "")
	r.SetPathValue("id", "1")
	rw := httptest.NewRecorder()
	deleteLocation(rw, r)
	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rw.Code, rw.Body.String())
	}
	for _, id := range []int{1, 2} {
		if got := loadOwnerMedia(db, ownerTypeLocation, id); len(got) != 0 {
			t.Errorf("location %d media survived: %+v", id, got)
		}
	}

	// Merge: keep's links first, then drop's minus duplicates, capped.
	replaceOwnerMedia(db, ownerTypeLocation, 3, []MediaLink{{URL: "https://example.org/k", Kind: "video"}, {URL: "https://example.org/dup", Kind: "video"}})
	db.Exec("INSERT INTO locations (id, location) VALUES (4, 'Dropped')")
	replaceOwnerMedia(db, ownerTypeLocation, 4, []MediaLink{{URL: "https://example.org/dup", Kind: "audio"}, {URL: "https://example.org/new", Kind: "audio"}})
	if err := mergeOwnerMedia(db, ownerTypeLocation, 3, 4); err != nil {
		t.Fatal(err)
	}
	got := loadOwnerMedia(db, ownerTypeLocation, 3)
	if len(got) != 3 || got[0].URL != "https://example.org/k" || got[2].URL != "https://example.org/new" {
		t.Fatalf("merged media = %+v", got)
	}
	if got := loadOwnerMedia(db, ownerTypeLocation, 4); len(got) != 0 {
		t.Fatalf("dropped owner's rows remain: %+v", got)
	}
}

// PATCH (RFC 7396) carries media too: present replaces, absent leaves alone,
// invalid is rejected without changing anything. The web layer saves
// locations through PATCH, so this is the path the admin form actually uses.
func TestPatchMediaForAllOwnerTypes(t *testing.T) {
	setupDedupTestDB(t)
	if musicianAvatars == nil {
		musicianAvatars = newAvatarSet(t.TempDir(), "/api/v1/musician-avatars/")
	}
	if orgAvatars == nil {
		orgAvatars = newAvatarSet(t.TempDir(), "/api/v1/org-avatars/")
	}
	db.Exec("INSERT INTO users (id, email, role) VALUES (1, 'admin@example.org', 'admin')")
	db.Exec("INSERT INTO musicians (id, bandname) VALUES (1, 'Band')")
	db.Exec("INSERT INTO organizations (id, name) VALUES (1, 'Org')")
	db.Exec("INSERT INTO locations (id, location) VALUES (1, 'Hall')")

	cases := []struct {
		name      string
		h         http.HandlerFunc
		ownerType string
	}{
		{"musician", patchMusician, ownerTypeMusician},
		{"organization", patchOrganization, ownerTypeOrganization},
		{"location", patchLocation, ownerTypeLocation},
	}
	for _, c := range cases {
		patch := func(body string) *httptest.ResponseRecorder {
			r := mediaTestRequest("PATCH", "/x/1", body)
			r.Header.Set("Content-Type", "application/merge-patch+json")
			r.SetPathValue("id", "1")
			rw := httptest.NewRecorder()
			c.h(rw, r)
			return rw
		}
		if rw := patch(`{"media":[{"kind":"audio","title":"T","url":"https://example.org/a"}]}`); rw.Code != http.StatusOK {
			t.Fatalf("%s: PATCH set = %d: %s", c.name, rw.Code, rw.Body.String())
		}
		if got := loadOwnerMedia(db, c.ownerType, 1); len(got) != 1 || got[0].Title != "T" {
			t.Fatalf("%s: not stored: %+v", c.name, got)
		}
		if rw := patch(`{}`); rw.Code != http.StatusOK {
			t.Fatalf("%s: empty PATCH = %d: %s", c.name, rw.Code, rw.Body.String())
		}
		if got := loadOwnerMedia(db, c.ownerType, 1); len(got) != 1 {
			t.Fatalf("%s: omitted media wiped the list: %+v", c.name, got)
		}
		if rw := patch(`{"media":[{"url":"http://insecure/"}]}`); rw.Code != http.StatusBadRequest {
			t.Fatalf("%s: http link = %d", c.name, rw.Code)
		}
		if got := loadOwnerMedia(db, c.ownerType, 1); len(got) != 1 {
			t.Fatalf("%s: rejected PATCH changed the list: %+v", c.name, got)
		}
		if rw := patch(`{"media":[]}`); rw.Code != http.StatusOK {
			t.Fatalf("%s: clear = %d", c.name, rw.Code)
		}
		if got := loadOwnerMedia(db, c.ownerType, 1); len(got) != 0 {
			t.Fatalf("%s: not cleared: %+v", c.name, got)
		}
	}
}
