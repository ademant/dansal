package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAdminImportPreviewHidesPastEvents covers #1416: the admin import
// preview table hides past-dated feed entries by default (disabled
// checkbox, hidden row) behind a "show past events" toggle, rather than
// silently importing or silently dropping them.
func TestAdminImportPreviewHidesPastEvents(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/admin/events/import", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	past := time.Now().AddDate(0, -3, 0).Format(time.RFC3339)
	future := time.Now().AddDate(0, 0, 7).Format(time.RFC3339)

	events := []PreviewEvent{
		{Title: "Past Event", StartTime: past, Status: "new"},
		{Title: "Future Event", StartTime: future, Status: "new"},
	}
	previewJSON := make([]string, len(events))
	for i := range events {
		previewJSON[i] = "{}"
	}

	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.adminEventsImport, tmplData(req, cfg, i18n, "test", AdminImportEventsData{
		PreviewEvents:  events,
		PreviewJSON:    previewJSON,
		PastEventCount: 1,
	}))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body tail=%s", rec.Code, body[max(0, len(body)-500):])
	}
	checkInlineJS(t, string(body))
	html := string(body)

	if !strings.Contains(html, `class="pe-row-past" style="display:none"`) {
		t.Error("expected the past event's row to be hidden by default")
	}
	if !strings.Contains(html, `class="row-cb row-cb-past" data-default-checked="true" disabled`) {
		t.Error("expected the past event's checkbox to be disabled by default")
	}
	if !strings.Contains(html, "id=\"show-past\"") {
		t.Error("expected the \"show past events\" toggle to render when PastEventCount > 0")
	}

	// The future event's own row must render completely normally: visible,
	// enabled, checked (status "new").
	futureRowIdx := strings.Index(html, "Future Event")
	if futureRowIdx == -1 {
		t.Fatal("future event row not found")
	}
	trStart := strings.LastIndex(html[:futureRowIdx], "<tr")
	futureRow := html[trStart:futureRowIdx]
	if strings.Contains(futureRow, "pe-row-past") || strings.Contains(futureRow, "display:none") {
		t.Errorf("future event's row should not be hidden: %s", futureRow)
	}
	if !strings.Contains(futureRow, "checked") || strings.Contains(futureRow, "disabled") {
		t.Errorf("future event's checkbox should be checked and enabled: %s", futureRow)
	}
}

// TestAdminImportPreviewNoPastEventsNoticeWhenNoneFound confirms the
// "show past events" toggle doesn't render when nothing was hidden.
func TestAdminImportPreviewNoPastEventsNoticeWhenNoneFound(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/admin/events/import", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	future := time.Now().AddDate(0, 0, 7).Format(time.RFC3339)
	events := []PreviewEvent{{Title: "Future Event", StartTime: future, Status: "new"}}

	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.adminEventsImport, tmplData(req, cfg, i18n, "test", AdminImportEventsData{
		PreviewEvents:  events,
		PreviewJSON:    []string{"{}"},
		PastEventCount: 0,
	}))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if strings.Contains(string(body), "id=\"show-past\"") {
		t.Error("the \"show past events\" toggle should not render when PastEventCount is 0")
	}
}
