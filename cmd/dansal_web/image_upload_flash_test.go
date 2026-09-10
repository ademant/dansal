package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSmokeRenderImageUploadErrorNotices renders every template touched by
// #1285 with ImageUploadError/ImageUploadWidget set, to catch a template
// syntax error the other smoke tests' plain renders wouldn't exercise (none
// of them set these fields).
func TestSmokeRenderImageUploadErrorNotices(t *testing.T) {
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
	req.Header.Set("Accept-Language", "en") // pin language so the content assertion below is stable
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	assertRenders := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		if strings.Contains(string(body), "template error") {
			t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
		}
		if !strings.Contains(string(body), "</html>") {
			t.Fatalf("truncated render (no closing </html>), body tail: %s", body[max(0, len(body)-300):])
		}
		return string(body)
	}

	t.Run("series", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.adminSeriesEdit, tmplData(req, cfg, i18n, "test", AdminSeriesEditData{
			Series:            EventSeries{ID: 1, Title: "Test series"},
			ImageUploadError:  "image_too_large",
			ImageUploadWidget: "image",
		}))
		assertRenders(t, rec)
	})

	t.Run("org", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.adminOrgEdit, tmplData(req, cfg, i18n, "test", AdminOrgEditData{
			Org:               Organization{ID: 1, Name: "Test org"},
			IsAdmin:           true,
			ImageUploadError:  "image_invalid_format",
			ImageUploadWidget: "avatar",
		}))
		body := assertRenders(t, rec)
		// The notice must be scoped to the widget that actually failed
		// (avatar): it should appear once, not also duplicated under the
		// unrelated "image" fieldset.
		if n := strings.Count(body, "Unsupported image format."); n != 1 {
			t.Errorf("expected the image_invalid_format notice exactly once (scoped to avatar), got %d", n)
		}
	})

	t.Run("musician", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.adminMusicianEdit, tmplData(req, cfg, i18n, "test", AdminMusicianEditData{
			Musician:          Musician{ID: 1, Bandname: "Test band"},
			ImageUploadError:  "image_too_large",
			ImageUploadWidget: "avatar",
		}))
		assertRenders(t, rec)
	})

	t.Run("instructor", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.adminInstructorEdit, tmplData(req, cfg, i18n, "test", AdminInstructorEditData{
			Instructor:        Instructor{ID: 1, Name: "Test instructor"},
			ImageUploadError:  "image_too_large",
			ImageUploadWidget: "avatar",
		}))
		assertRenders(t, rec)
	})

	t.Run("event", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.adminEventForm, tmplData(req, cfg, i18n, "test", AdminEventFormData{
			Event:             Event{ID: 1, Title: "Test event"},
			ImageUploadError:  "image_invalid_format",
			ImageUploadWidget: "image",
		}))
		assertRenders(t, rec)
	})

	t.Run("suggest-done", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.suggestDone, tmplData(req, cfg, i18n, "test", SuggestDoneData{
			ImageUploadError: "image_too_large",
		}))
		assertRenders(t, rec)
	})

	t.Run("contact-manage", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.contactManage, tmplData(req, cfg, i18n, "test", ContactManageData{
			Token:            "tok",
			Post:             ContactManageResult{ID: 1, Type: "lost_item"},
			ImageUploadError: "image_too_large",
		}))
		assertRenders(t, rec)
	})
}
