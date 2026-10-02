package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1421: admin forms explain why a save failed.

func TestAdminSaveError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want SaveError
	}{
		{"rate limited", &apiHTTPError{StatusCode: 429, Message: "Rate limit exceeded for this account. Please slow down.", ErrorID: "32e90758"},
			SaveError{Key: "admin_save_error_rate_limited", Ref: "32e90758"}},
		{"forbidden", &apiHTTPError{StatusCode: 403, Message: "forbidden"}, SaveError{Key: "admin_save_error_forbidden"}},
		{"validation", &apiHTTPError{StatusCode: 400, Message: "title and start_time are required", ErrorID: "ab12"},
			SaveError{Key: "admin_save_error_detail", Detail: "title and start_time are required", Ref: "ab12"}},
		{"server error", &apiHTTPError{StatusCode: 500, Message: "Internal server error", ErrorID: "cd34"},
			SaveError{Key: "admin_save_error", Ref: "cd34"}},
		{"non-API error", errors.New("dial tcp: connection refused"), SaveError{Key: "admin_save_error"}},
	}
	for _, c := range cases {
		if got := adminSaveError(c.err); got != c.want {
			t.Errorf("%s: adminSaveError = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// The generic CRUD path (musicians/instructors) re-renders the form with the
// classified error instead of a bare "Save failed.".
func TestAdminCRUDCreateShowsRateLimit(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{"error": "Rate limit exceeded for this account. Please slow down.", "error_id": "32e90758"})
	}))
	defer api.Close()
	client := &DansalClient{BaseURL: api.URL, HTTP: api.Client()}
	h := musicianEntity.Create(&Config{Domain: "example.test"}, loadTemplates(), client, loadI18n(""))

	req := httptest.NewRequest(http.MethodPost, "/admin/musicians/new", strings.NewReader("bandname=Trio+Test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
	rec := httptest.NewRecorder()
	h(rec, req)
	body := rec.Body.String()
	en := loadI18n("").Strings("en")
	de := loadI18n("").Strings("de")
	if !strings.Contains(body, en.T("admin_save_error_rate_limited")) && !strings.Contains(body, de.T("admin_save_error_rate_limited")) {
		t.Errorf("rate-limit explanation missing; body excerpt: %s", body[max(0, strings.Index(body, "form-error")-20):min(len(body), strings.Index(body, "form-error")+300)])
	}
	if !strings.Contains(body, "32e90758") {
		t.Error("error reference missing")
	}
}

func TestSaveErrorRendersInAdminForms(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)
	tm := loadTemplates()
	i18n := loadI18n("")
	req := withSessionUser(httptest.NewRequest(http.MethodGet, "/admin/x", nil), &SessionUser{ID: 1, Role: "admin"})
	const key, detail, ref = "admin_save_error_detail", "title and start_time are required", "ab12cd"

	pages := []struct {
		name string
		tmpl *template.Template
		data any
	}{
		{"event", tm.adminEventForm, AdminEventFormData{IsNew: true, ErrorKey: key, ErrorDetail: detail, ErrorRef: ref}},
		{"musician", tm.adminMusicianEdit, AdminMusicianEditData{IsNew: true, ErrorKey: key, ErrorDetail: detail, ErrorRef: ref}},
		{"instructor", tm.adminInstructorEdit, AdminInstructorEditData{IsNew: true, ErrorKey: key, ErrorDetail: detail, ErrorRef: ref}},
		{"location", tm.adminLocationEdit, AdminLocationEditData{ErrorKey: key, ErrorDetail: detail, ErrorRef: ref}},
		{"org", tm.adminOrgEdit, AdminOrgEditData{ErrorKey: key, ErrorDetail: detail, ErrorRef: ref}},
		{"fetchurl edit", tm.adminFetchurlEdit, AdminFetchurlEditData{ErrorKey: key, ErrorDetail: detail, ErrorRef: ref}},
		{"fetchurl new", tm.adminFetchurlNew, AdminFetchurlNewData{ErrorKey: key, ErrorDetail: detail, ErrorRef: ref}},
	}
	for _, p := range pages {
		rec := httptest.NewRecorder()
		renderTemplate(rec, p.tmpl, tmplData(req, &Config{Domain: "example.test"}, i18n, "test", p.data))
		body := rec.Body.String()
		if !strings.Contains(body, detail) || !strings.Contains(body, ref) || strings.Contains(body, key) {
			t.Errorf("%s form: save error not rendered with detail and reference", p.name)
		}
	}
}
