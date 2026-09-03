package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAdminImportSingleMatchLongDescriptionDoesNotRedirect is a regression
// test for #1228: a feed import that resolves to exactly one event used to
// redirect to /admin/events/new?title=...&description=...&..., carrying the
// full event description through the Location response header. A long
// description (e.g. a real multi-paragraph iCal DESCRIPTION, as seen on
// prod importing a kirche-bremen.de event) overflowed nginx's header
// buffer and 502'd before the admin ever saw the form. The fix renders the
// new-event form directly, in-process, instead of redirecting — so this
// drives the real handler with a several-KB description and asserts the
// response is a normal 200 render (no Location header at all) with the
// description actually present in the rendered form.
func TestAdminImportSingleMatchLongDescriptionDoesNotRedirect(t *testing.T) {
	longDesc := "Lorem ipsum dolor sit amet marker-needle. " + strings.Repeat("Consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore. ", 100)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/events/preview", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"title": "Long Description Event",
			"description": ` + jsonQuote(longDesc) + `,
			"start_time": "2026-10-01T18:00:00Z",
			"location": {"location": "Village Hall", "town": "Testville"}
		}]`))
	})
	for _, path := range []string{"GET /api/v1/organizations", "GET /api/v1/locations", "GET /api/v1/musicians", "GET /api/v1/instructors", "GET /api/v1/dances"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	db := initDB(":memory:")
	defer db.Close()
	siteCfg = newSiteSettingsCache(db)

	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test"}
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	handler := adminImportEventsHandler(cfg, tmpls, db, client, i18n)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("url", "https://example.test/feed.ics")
	mw.WriteField("type", "ical")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/admin/events/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	rec := httptest.NewRecorder()
	handler(rec, req)

	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("expected no redirect, got Location: %d bytes: %.100q...", len(loc), loc)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "marker-needle") {
		t.Errorf("rendered form does not contain the imported description")
	}
}

func jsonQuote(s string) string {
	var b bytes.Buffer
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
