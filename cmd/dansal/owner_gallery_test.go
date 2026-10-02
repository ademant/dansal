package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// setupGalleryTest gives a fresh DB with one musician, a temp gallery dir and
// a jpeg config (the AVIF encoder is too slow for unit tests).
func setupGalleryTest(t *testing.T, max int) (musicianID int, dir string) {
	t.Helper()
	// A file DB, not :memory: — update() runs a transaction next to plain
	// queries, which on :memory: would see separate databases per connection.
	conn, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "gallery.db"))
	if err != nil {
		t.Fatal(err)
	}
	oldDB, oldConfig, oldDir := db, config, galleryImagesDir
	db = conn
	config = &Config{}
	config.Server.MaxBodyBytes = 1 << 20
	config.Server.ImageXMax = 1024
	config.Server.ImageYMax = 1024
	config.Server.ImageFormat = "jpeg"
	config.Server.GalleryMaxImages = max
	dir = t.TempDir()
	galleryImagesDir = dir
	t.Cleanup(func() { db, config, galleryImagesDir = oldDB, oldConfig, oldDir; conn.Close() })

	if err := createTables(); err != nil {
		t.Fatal(err)
	}
	migrateDB()
	if err := conn.QueryRow("INSERT INTO musicians (bandname) VALUES ('Gallery Band') RETURNING id").Scan(&musicianID); err != nil {
		t.Fatal(err)
	}
	return musicianID, dir
}

func galleryRequest(t *testing.T, method string, musicianID int, gid string, body *bytes.Buffer, ctype string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/v1/musicians/" + strconv.Itoa(musicianID) + "/gallery"
	if gid != "" {
		path += "/" + gid
	}
	if body == nil {
		body = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleUser)
	req.SetPathValue("id", strconv.Itoa(musicianID))
	if gid != "" {
		req.SetPathValue("gid", gid)
	}
	rec := httptest.NewRecorder()
	switch method {
	case http.MethodPost:
		musicianGallery.upload(rec, req)
	case http.MethodPut:
		musicianGallery.update(rec, req)
	case http.MethodDelete:
		musicianGallery.remove(rec, req)
	}
	return rec
}

func uploadGalleryPicture(t *testing.T, musicianID int, caption string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("image", "pic.webp")
	fw.Write(data)
	mw.WriteField("caption", caption)
	mw.WriteField("ai_generated", "1")
	mw.Close()
	return galleryRequest(t, http.MethodPost, musicianID, "", &buf, mw.FormDataContentType())
}

func galleryFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// TestOwnerGalleryLifecycle drives upload → limit → reorder/caption/drop →
// delete → musician delete through the handlers (#1362).
func TestOwnerGalleryLifecycle(t *testing.T) {
	mid, dir := setupGalleryTest(t, 2)
	pic := decodeWebPBytes(t)

	var ids []int
	for _, c := range []string{"first", "second"} {
		rec := uploadGalleryPicture(t, mid, c, pic)
		if rec.Code != http.StatusCreated {
			t.Fatalf("upload %s: %d %s", c, rec.Code, rec.Body.String())
		}
		var g GalleryImage
		json.Unmarshal(rec.Body.Bytes(), &g)
		if g.Caption != c || !g.AIGenerated || g.ThumbURL != g.URL+"?thumb=sq" {
			t.Fatalf("unexpected upload response %+v", g)
		}
		ids = append(ids, g.ID)
	}
	if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(ids[0])+".sq.jpeg")); err != nil {
		t.Fatalf("square thumbnail missing: %v (files %v)", err, galleryFiles(t, dir))
	}

	// Limit reached.
	if rec := uploadGalleryPicture(t, mid, "third", pic); rec.Code != http.StatusConflict {
		t.Fatalf("upload past the limit: got %d, want 409", rec.Code)
	}
	// Not an image: 415, and no row or file is left behind.
	if rec := uploadGalleryPicture(t, mid, "", []byte("not an image at all")); rec.Code != http.StatusConflict {
		// still full — free a slot first to test the 415 path below
		t.Fatalf("full gallery must reject before decoding, got %d", rec.Code)
	}

	// Reorder, re-caption, and drop the first picture in one PUT.
	body, _ := json.Marshal(GalleryUpdateRequest{Items: []GalleryItemUpdate{{ID: ids[1], Caption: "  renamed  "}}})
	rec := galleryRequest(t, http.MethodPut, mid, "", bytes.NewBuffer(body), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	got := loadOwnerGallery(db, ownerTypeMusician, mid)
	if len(got) != 1 || got[0].ID != ids[1] || got[0].Caption != "renamed" || got[0].AIGenerated {
		t.Fatalf("after PUT: %+v", got)
	}
	for _, f := range galleryFiles(t, dir) {
		if strings.HasPrefix(f, strconv.Itoa(ids[0])+".") {
			t.Fatalf("dropped picture's file %s still on disk", f)
		}
	}

	// A foreign id in the PUT body is rejected.
	body, _ = json.Marshal(GalleryUpdateRequest{Items: []GalleryItemUpdate{{ID: ids[0]}}})
	if rec := galleryRequest(t, http.MethodPut, mid, "", bytes.NewBuffer(body), "application/json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with unknown id: got %d, want 400", rec.Code)
	}

	// Now there's room: a non-image is 415 and leaves nothing behind.
	if rec := uploadGalleryPicture(t, mid, "", []byte("not an image at all")); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-image upload: got %d, want 415", rec.Code)
	}
	if n := len(loadOwnerGallery(db, ownerTypeMusician, mid)); n != 1 {
		t.Fatalf("failed upload left a row behind: %d rows", n)
	}

	// Single delete.
	if rec := galleryRequest(t, http.MethodDelete, mid, strconv.Itoa(ids[1]), nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", rec.Code)
	}
	if rec := galleryRequest(t, http.MethodDelete, mid, strconv.Itoa(ids[1]), nil, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE: got %d, want 404", rec.Code)
	}
	if f := galleryFiles(t, dir); len(f) != 0 {
		t.Fatalf("files left after delete: %v", f)
	}

	// Deleting the musician removes its gallery.
	if rec := uploadGalleryPicture(t, mid, "x", pic); rec.Code != http.StatusCreated {
		t.Fatalf("re-upload: %d", rec.Code)
	}
	deleteOwnerGallery(db, ownerTypeMusician, mid)
	if n := len(loadOwnerGallery(db, ownerTypeMusician, mid)); n != 0 {
		t.Fatalf("rows left after owner delete: %d", n)
	}
	if f := galleryFiles(t, dir); len(f) != 0 {
		t.Fatalf("files left after owner delete: %v", f)
	}
}

func TestOwnerGalleryUnknownMusicianAndRole(t *testing.T) {
	mid, _ := setupGalleryTest(t, 8)
	if rec := uploadGalleryPicture(t, mid+100, "", decodeWebPBytes(t)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown musician: got %d, want 404", rec.Code)
	}
	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.Header.Set("X-User-Role", RolePublisher)
	req.SetPathValue("id", strconv.Itoa(mid))
	req.SetPathValue("gid", "1")
	rec := httptest.NewRecorder()
	musicianGallery.remove(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("publisher role: got %d, want 403", rec.Code)
	}
}

// TestSmokeMigrationOwnerGallery: fresh install, upgrade (v46 not applied),
// pre-marked with the table missing (safety net), idempotent re-run.
func TestSmokeMigrationOwnerGallery(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	old := db
	db = conn
	t.Cleanup(func() { db = old; conn.Close() })

	hasTable := func() bool {
		var n int
		conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='owner_gallery'").Scan(&n)
		return n > 0
	}
	if err := createTables(); err != nil {
		t.Fatal(err)
	}
	migrateDB()
	if !hasTable() {
		t.Fatal("owner_gallery missing on fresh install")
	}
	conn.Exec("DROP TABLE owner_gallery")
	conn.Exec("DELETE FROM schema_migrations WHERE version = 46")
	migrateDB()
	if !hasTable() {
		t.Fatal("owner_gallery not created by the v46 migration")
	}
	conn.Exec("DROP TABLE owner_gallery")
	migrateDB()
	if !hasTable() {
		t.Fatal("safety net did not recreate owner_gallery")
	}
	migrateDB()
}
