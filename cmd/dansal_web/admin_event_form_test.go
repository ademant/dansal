package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSmokeLoadTemplates(t *testing.T) { loadTemplates() }

func TestSmokeRenderAdminEventForm(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/admin/events/new", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	orgID := 1
	locID := 2
	render := func(name string, data AdminEventFormData) {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.adminEventForm, tmplData(req, cfg, i18n, "test", data))
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, body)
			}
			if len(body) == 0 {
				t.Fatal("empty body")
			}
			if strings.Contains(string(body), "template error") {
				t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
			}
			if !strings.Contains(string(body), "</html>") {
				t.Fatalf("truncated render (no closing </html>), body tail: %s", body[max(0, len(body)-300):])
			}
		})
	}

	render("new-blank", AdminEventFormData{IsNew: true})

	render("new-with-prefill", AdminEventFormData{
		IsNew: true,
		Event: eventFromPrefill(&EventPrefill{
			Title: "Cloned event", Date: "2026-07-01", StartTime: "20:00",
			OrgID: orgID, LocID: locID, CloneMode: true, OriginalDate: "2026-06-01",
		}),
		Prefill:       &EventPrefill{Title: "Cloned event", CloneMode: true, OriginalDate: "2026-06-01"},
		Organizations: []Organization{{ID: orgID, Name: "Test Org"}},
		Locations:     []Location{{ID: locID, Location: "Test Hall"}},
		UserOrgs:      []Organization{{ID: orgID, Name: "Test Org"}},
		Templates:     []EventTemplate{{ID: 1, Name: "Tpl"}},
	})

	render("edit-existing", AdminEventFormData{
		IsNew: false,
		Event: Event{
			ID: 42, Title: "Existing event", StartTime: "2026-07-01T20:00:00",
			Cancelable: true, OrganizationID: &orgID, LocationID: &locID,
		},
		Org:           &Organization{ID: orgID, Name: "Test Org"},
		Organizations: []Organization{{ID: orgID, Name: "Test Org"}},
		Locations:     []Location{{ID: locID, Location: "Test Hall"}},
		UserOrgs:      []Organization{{ID: orgID, Name: "Test Org"}},
		Templates:     []EventTemplate{{ID: 1, Name: "Tpl"}},
		Series:        []EventSeries{{ID: 5, Title: "Some series"}},
	})

	render("edit-with-current-series", AdminEventFormData{
		IsNew:         false,
		Event:         Event{ID: 43, Title: "Series event"},
		Organizations: []Organization{{ID: orgID, Name: "Test Org"}},
		Locations:     []Location{{ID: locID, Location: "Test Hall"}},
		CurrentSeries: &EventSeries{ID: 5, Title: "Some series"},
	})
}
