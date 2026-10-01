package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1409: external syndication is untested end-to-end and must stay off by
// default, both as a dansal-web admin UI (org config fieldset, per-event
// buttons) and as the 4 backing proxy routes — until an instance explicitly
// opts in via web.yaml's enable_syndication.

func TestRequireSyndicationEnabled(t *testing.T) {
	t.Run("disabled: writes 404 and returns false", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if requireSyndicationEnabled(rec, &Config{}) {
			t.Fatal("expected false when EnableSyndication is unset")
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
	t.Run("enabled: returns true, writes nothing", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if !requireSyndicationEnabled(rec, &Config{EnableSyndication: true}) {
			t.Fatal("expected true when EnableSyndication is set")
		}
	})
}

func TestSyndicationRoutesDisabledByDefault(t *testing.T) {
	client := &DansalClient{}
	routes := []struct {
		name    string
		handler func(*Config, *DansalClient) http.HandlerFunc
		method  string
		path    string
		pathID  string
	}{
		{"adminSyndicationGetHandler", adminSyndicationGetHandler, http.MethodGet, "/admin/orgs/1/syndication", "1"},
		{"adminSyndicationSaveHandler", adminSyndicationSaveHandler, http.MethodPost, "/admin/orgs/1/syndication", "1"},
		{"adminGetSyncStatusHandler", adminGetSyncStatusHandler, http.MethodGet, "/admin/events/1/syndication", "1"},
	}
	for _, rt := range routes {
		t.Run(rt.name+"/disabled", func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			req.SetPathValue("id", rt.pathID)
			rec := httptest.NewRecorder()
			rt.handler(&Config{}, client)(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 (disabled), body=%s", rec.Code, rec.Body.String())
			}
		})
		t.Run(rt.name+"/enabled falls through past the syndication gate", func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			req.SetPathValue("id", rt.pathID)
			rec := httptest.NewRecorder()
			rt.handler(&Config{EnableSyndication: true}, client)(rec, req)
			// No session on the request -> requireLogin redirects (303), the
			// next check after the syndication gate. Any outcome other than
			// the disabled-gate's 404 proves the gate let the request through.
			if rec.Code == http.StatusNotFound {
				t.Errorf("status = 404, expected the request to pass the syndication gate when enabled")
			}
		})
	}

	t.Run("adminSyndicatePlatformHandler/disabled", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/admin/events/1/syndicate/eventbrite", nil)
		req.SetPathValue("id", "1")
		req.SetPathValue("platform", "eventbrite")
		rec := httptest.NewRecorder()
		adminSyndicatePlatformHandler(&Config{}, client)(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (disabled), body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("adminSyndicatePlatformHandler/enabled falls through past the syndication gate", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/admin/events/1/syndicate/eventbrite", nil)
		req.SetPathValue("id", "1")
		req.SetPathValue("platform", "eventbrite")
		rec := httptest.NewRecorder()
		adminSyndicatePlatformHandler(&Config{EnableSyndication: true}, client)(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("status = 404, expected the request to pass the syndication gate when enabled")
		}
	})
}

func TestAdminEventFormSyndicationSectionGatedByConfig(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	tmpls := loadTemplates()
	i18n := loadI18n("")
	req := httptest.NewRequest(http.MethodGet, "/admin/events/1/edit", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	data := AdminEventFormData{
		IsNew: false,
		Event: Event{ID: 1, Title: "Test Event"},
	}

	render := func(cfg *Config) string {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.adminEventForm, tmplData(req, cfg, i18n, "test", data))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		return string(body)
	}

	t.Run("disabled (default): section absent from /admin/events/{id}/edit", func(t *testing.T) {
		body := render(&Config{Domain: "example.test"})
		if strings.Contains(body, `id="sec-syndication"`) {
			t.Error("syndication section rendered even though EnableSyndication is unset")
		}
	})
	t.Run("enabled: section present", func(t *testing.T) {
		body := render(&Config{Domain: "example.test", EnableSyndication: true})
		if !strings.Contains(body, `id="sec-syndication"`) {
			t.Error("syndication section missing even though EnableSyndication is true")
		}
	})
}

func TestAdminOrgEditSyndicationSectionGatedByConfig(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	tmpls := loadTemplates()
	i18n := loadI18n("")
	req := httptest.NewRequest(http.MethodGet, "/admin/organizations/1/edit", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	data := AdminOrgEditData{
		Org: Organization{ID: 1, Name: "Test Org"},
	}

	render := func(cfg *Config) string {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.adminOrgEdit, tmplData(req, cfg, i18n, "test", data))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		return string(body)
	}

	t.Run("disabled (default): section absent, Members fieldset still present", func(t *testing.T) {
		body := render(&Config{Domain: "example.test"})
		if strings.Contains(body, `id="sec-syndication"`) {
			t.Error("syndication section rendered even though EnableSyndication is unset")
		}
		if !strings.Contains(body, "Members") {
			t.Error("Members fieldset should still render regardless of syndication gating")
		}
	})
	t.Run("enabled: section present", func(t *testing.T) {
		body := render(&Config{Domain: "example.test", EnableSyndication: true})
		if !strings.Contains(body, `id="sec-syndication"`) {
			t.Error("syndication section missing even though EnableSyndication is true")
		}
	})
}
