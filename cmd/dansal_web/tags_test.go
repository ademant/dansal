package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// setupSiteCfg gives tmplData (invoked by tagHandler for HTML/AP rendering)
// a working siteCfg global, mirroring the pattern used by the other
// template-rendering smoke tests in this package.
func setupSiteCfg(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)
}

// tagsTestClient spins up a fake dansal API serving just enough of
// /api/v1/tags, /api/v1/events, and /api/v1/organizations for the tag
// handler tests below. The caller must Close() the returned server.
func tagsTestClient(t *testing.T) (*DansalClient, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/tags", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]Tag{
			{Slug: "bal-folk", Name: "Bal-folk", Category: "format"},
		})
	})
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tag") != "bal-folk" {
			json.NewEncoder(w).Encode([]Event{})
			return
		}
		orgID := 7
		json.NewEncoder(w).Encode([]Event{
			{ID: 1, Title: "Test Ball", StartTime: "2027-01-01T20:00:00Z", Tags: []string{"bal-folk"}, OrganizationID: &orgID},
		})
	})
	mux.HandleFunc("/api/v1/organizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]Organization{{ID: 7, Name: "Test Org"}})
	})
	srv := httptest.NewServer(mux)
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	return client, srv
}

func TestTagHandlerUnknownSlug404s(t *testing.T) {
	setupSiteCfg(t)
	client, srv := tagsTestClient(t)
	defer srv.Close()

	cfg := &Config{Domain: "example.test"}
	i18n := loadI18n("")
	tmpls := loadTemplates()
	handler := tagHandler(cfg, tmpls, client, i18n)

	req := httptest.NewRequest(http.MethodGet, "/tags/nonexistent", nil)
	req.SetPathValue("slug", "nonexistent")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

func TestTagHandlerHTML(t *testing.T) {
	setupSiteCfg(t)
	client, srv := tagsTestClient(t)
	defer srv.Close()

	cfg := &Config{Domain: "example.test"}
	i18n := loadI18n("")
	tmpls := loadTemplates()
	handler := tagHandler(cfg, tmpls, client, i18n)

	req := httptest.NewRequest(http.MethodGet, "/tags/bal-folk", nil)
	req.SetPathValue("slug", "bal-folk")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" && !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestTagHandlerActivityPubCollection(t *testing.T) {
	client, srv := tagsTestClient(t)
	defer srv.Close()

	cfg := &Config{Domain: "example.test"}
	i18n := loadI18n("")
	tmpls := loadTemplates()
	handler := tagHandler(cfg, tmpls, client, i18n)

	req := httptest.NewRequest(http.MethodGet, "/tags/bal-folk", nil)
	req.SetPathValue("slug", "bal-folk")
	req.Header.Set("Accept", "application/activity+json")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var col OrderedCollection
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if col.Type != "OrderedCollection" || col.TotalItems != 1 || col.First == "" {
		t.Errorf("unexpected collection: %+v", col)
	}

	// Fetch the page and confirm the Note is attributed to the event's org.
	req2 := httptest.NewRequest(http.MethodGet, "/tags/bal-folk?page=true", nil)
	req2.SetPathValue("slug", "bal-folk")
	req2.Header.Set("Accept", "application/activity+json")
	rec2 := httptest.NewRecorder()
	handler(rec2, req2)

	var page OrderedCollectionPage
	if err := json.Unmarshal(rec2.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if page.TotalItems != 1 || len(page.OrderedItems) != 1 {
		t.Fatalf("unexpected page: %+v", page)
	}
	note, _ := page.OrderedItems[0].(map[string]any)
	if attrTo, _ := note["attributedTo"].(string); attrTo != "https://example.test/org/test-org" {
		t.Errorf("attributedTo = %q, want the event's org actor", attrTo)
	}
}

func TestTagAtomAndJSONFeed(t *testing.T) {
	client, srv := tagsTestClient(t)
	defer srv.Close()
	cfg := &Config{Domain: "example.test"}

	t.Run("atom", func(t *testing.T) {
		handler := tagAtomHandler(cfg, client)
		req := httptest.NewRequest(http.MethodGet, "/tags/bal-folk.atom", nil)
		req.SetPathValue("slug", "bal-folk")
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "<title>Test Ball</title>") {
			t.Errorf("missing entry title, body: %s", rec.Body.String())
		}
	})

	t.Run("jsonfeed", func(t *testing.T) {
		handler := tagJSONFeedHandler(cfg, client)
		req := httptest.NewRequest(http.MethodGet, "/tags/bal-folk.jsonfeed", nil)
		req.SetPathValue("slug", "bal-folk")
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var doc jsonFeedDoc
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if doc.Version != "https://jsonfeed.org/version/1.1" || len(doc.Items) != 1 {
			t.Fatalf("unexpected doc: %+v", doc)
		}
	})

	t.Run("unknown slug 404s", func(t *testing.T) {
		handler := tagAtomHandler(cfg, client)
		req := httptest.NewRequest(http.MethodGet, "/tags/nonexistent.atom", nil)
		req.SetPathValue("slug", "nonexistent")
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d, want 404", rec.Code)
		}
	})
}

// TestEventAudienceType covers the deterministic tag-to-audienceType mapping
// used by the event JSON-LD (#1063).
func TestEventAudienceType(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags []string
		want string
	}{
		{"no tags", nil, "dancers"},
		{"plain tags", []string{"bal-folk", "festival"}, "dancers"},
		{"musician-workshop", []string{"bal-folk", "musician-workshop"}, "dancers, musicians"},
		{"session", []string{"session"}, "dancers, musicians"},
		{"music-course", []string{"music-course"}, "dancers, musicians"},
	} {
		if got := eventAudienceType(tc.tags); got != tc.want {
			t.Errorf("%s: eventAudienceType(%v) = %q, want %q", tc.name, tc.tags, got, tc.want)
		}
	}
}
