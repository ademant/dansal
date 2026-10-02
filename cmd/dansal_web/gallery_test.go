package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestSaveMusicianGallery drives the form → API mapping (#1362): the PUT
// carries kept pictures in submitted order with their captions/AI flags
// (removed ones left out), each new file becomes one upload, and a 409 from
// the API becomes the gallery_full notice.
func TestSaveMusicianGallery(t *testing.T) {
	var mu sync.Mutex
	var putBody map[string][]GalleryItemUpdate
	var uploads []string
	full := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/musicians/7/gallery":
			json.NewDecoder(r.Body).Decode(&putBody)
			w.Write([]byte("[]"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/musicians/7/gallery-images":
			if full {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":"gallery is full"}`))
				return
			}
			r.ParseMultipartForm(1 << 20)
			_, fh, _ := r.FormFile("image")
			uploads = append(uploads, fh.Filename+"|"+r.FormValue("ai_generated"))
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte("{}"))
		default:
			t.Errorf("unexpected API call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	client := &DansalClient{BaseURL: api.URL, HTTP: api.Client()}

	build := func() *http.Request {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("gallery_present", "1")
		// Submitted order 12, 10, 11 — the rows were reordered client-side.
		mw.WriteField("gallery_id", "12")
		mw.WriteField("gallery_id", "10")
		mw.WriteField("gallery_id", "11")
		mw.WriteField("gallery_caption_12", "  On stage ")
		mw.WriteField("gallery_ai_12", "1")
		mw.WriteField("gallery_caption_10", "")
		mw.WriteField("gallery_remove_11", "1")
		mw.WriteField("gallery_new_ai", "1")
		for _, name := range []string{"a.jpg", "b.png"} {
			fw, _ := mw.CreateFormFile("gallery_new", name)
			fw.Write([]byte("fake image bytes"))
		}
		mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/admin/musicians/7/edit", &buf)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		if err := r.ParseMultipartForm(maxMultipartSize); err != nil {
			t.Fatal(err)
		}
		return r
	}

	if f := saveMusicianGallery(client, build(), 7, "tok"); f.ImageUploadError != "" {
		t.Fatalf("unexpected flash %+v", f)
	}
	want := []GalleryItemUpdate{{ID: 12, Caption: "On stage", AIGenerated: true}, {ID: 10}}
	got := putBody["items"]
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("PUT items = %+v, want %+v", got, want)
	}
	if strings.Join(uploads, ",") != "a.jpg|1,b.png|1" {
		t.Fatalf("uploads = %v", uploads)
	}

	full = true
	uploads = nil
	if f := saveMusicianGallery(client, build(), 7, "tok"); f.ImageUploadError != "gallery_full" || f.ImageUploadWidget != "gallery" {
		t.Fatalf("409 must map to gallery_full, got %+v", f)
	}

	// No gallery_present: the PUT must not run (it would wipe the gallery).
	putBody = nil
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("bandname=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ParseForm()
	saveMusicianGallery(client, r, 7, "tok")
	if putBody != nil {
		t.Fatal("PUT sent although the form had no gallery section")
	}
}

// TestSmokeRenderMusicianGallery renders the admin form and the public page
// with a gallery.
func TestSmokeRenderMusicianGallery(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Language", "en")
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	m := Musician{ID: 7, Bandname: "Gallery Band", GalleryMax: 8, Gallery: []GalleryImage{
		{ID: 3, Caption: "On stage", URL: "/api/v1/gallery-images/3", ThumbURL: "/api/v1/gallery-images/3?thumb=sq"},
		{ID: 4, AIGenerated: true, URL: "/api/v1/gallery-images/4", ThumbURL: "/api/v1/gallery-images/4?thumb=sq"},
	}}

	render := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK || strings.Contains(string(body), "template error") || !strings.Contains(string(body), "</html>") {
			t.Fatalf("bad render: status=%d tail=%s", rec.Code, body[max(0, len(body)-400):])
		}
		return string(body)
	}

	t.Run("admin", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.adminMusicianEdit, tmplData(req, cfg, i18n, "test", AdminMusicianEditData{
			Musician: m, ImageUploadError: "gallery_full", ImageUploadWidget: "gallery",
		}))
		body := render(t, rec)
		for _, want := range []string{
			`name="gallery_present" value="1"`,
			`name="gallery_caption_3" value="On stage"`,
			`name="gallery_ai_4" value="1" checked`,
			"2 of 8 pictures",
			"The gallery is full",
			`name="gallery_new"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("admin form misses %q", want)
			}
		}
	})

	t.Run("public", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.musician, tmplData(req, cfg, i18n, "Gallery Band", MusicianPageData{Musician: m, Slug: "gallery-band"}))
		body := render(t, rec)
		for _, want := range []string{
			`<figcaption>On stage</figcaption>`,
			`alt="On stage"`,
			`alt="Gallery Band"`, // no caption → band name
			`href="/api/v1/gallery-images/4"`,
			`class="gallery-ai"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("public page misses %q", want)
			}
		}
	})
}
