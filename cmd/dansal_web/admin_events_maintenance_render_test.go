package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1584: the maintenance page renders the musician and instructor multi-selects
// with the current selection, and the CSV export link with the same filters.
func TestMaintenanceFilterFormRenders(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/admin/events/maintenance?musician_id=2", nil)

	data := AdminEventsMaintenanceData{
		Musicians:          []Musician{{ID: 1, Bandname: "Band One"}, {ID: 2, Bandname: "Band Two"}},
		Instructors:        []Instructor{{ID: 4, Name: "Teacher Four"}},
		MusicianSelected:   map[int]bool{2: true},
		InstructorSelected: map[int]bool{},
		ExportURL:          "/admin/events/export.csv?musician_id=2",
		TotalPages:         1,
	}
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.adminEventsMaintenance, tmplData(req, cfg, i18n, "Maintenance", data))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}
	for _, want := range []string{
		`<option value="2" selected>Band Two</option>`,
		`<option value="1">Band One</option>`,
		`<option value="4">Teacher Four</option>`,
		`href="/admin/events/export.csv?musician_id=2"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("maintenance page missing %s", want)
		}
	}
	if strings.Contains(html, `<option value="4" selected>`) {
		t.Error("instructor 4 is selected but was not in the filter")
	}
}
