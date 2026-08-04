package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebfingerSlug(t *testing.T) {
	cfg := testCfg()

	cases := []struct {
		name         string
		resource     string
		wantSlug     string
		wantNotFound bool
		wantOK       bool
	}{
		{"acct valid", "acct:myorg@example.com", "myorg", false, true},
		{"acct wrong domain", "acct:myorg@other.example", "", true, false},
		{"acct missing user part", "acct:@example.com", "", true, false},
		{"acct malformed no @", "acct:myorg", "", true, false},
		{"https org url valid", "https://example.com/org/myorg", "myorg", false, true},
		{"https org url trailing slash empty slug", "https://example.com/org/", "", true, false},
		{"https unrelated url", "https://example.com/federation/u/relay", "", false, false},
		{"unsupported scheme", "mailto:foo@example.com", "", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug, notFound, ok := webfingerSlug(cfg, tc.resource)
			if slug != tc.wantSlug || notFound != tc.wantNotFound || ok != tc.wantOK {
				t.Errorf("webfingerSlug(%q) = (%q, %v, %v), want (%q, %v, %v)",
					tc.resource, slug, notFound, ok, tc.wantSlug, tc.wantNotFound, tc.wantOK)
			}
		})
	}
}

// TestWebfingerAliasFallback verifies that a common local-part probe (e.g.
// admin@domain) configured via WebfingerAliases resolves to the aliased
// actor's own subject/self-link instead of a 404 (issue #947).
func TestWebfingerAliasFallback(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE actors (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		org_id INTEGER UNIQUE NOT NULL,
		org_slug TEXT UNIQUE NOT NULL,
		public_key_pem TEXT NOT NULL,
		private_key_pem TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO actors (org_id, org_slug, public_key_pem, private_key_pem) VALUES (0, 'relay', 'pub', 'priv')`); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{Domain: "example.com", WebfingerAliases: map[string]string{"admin": "relay"}}
	handler := webfingerHandler(cfg, db, nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/webfinger?resource="+"acct:admin@example.com", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var wf WebFinger
	if err := json.Unmarshal(rec.Body.Bytes(), &wf); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if wf.Subject != "acct:relay@example.com" {
		t.Errorf("Subject = %q, want acct:relay@example.com (admin should alias to relay)", wf.Subject)
	}
}

// TestWebfingerNoAliasStillMisses confirms an unaliased unknown local-part
// still 404s rather than silently resolving to something.
func TestWebfingerNoAliasStillMisses(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE actors (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		org_id INTEGER UNIQUE NOT NULL,
		org_slug TEXT UNIQUE NOT NULL,
		public_key_pem TEXT NOT NULL,
		private_key_pem TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{Domain: "example.com"}
	handler := webfingerHandler(cfg, db, &DansalClient{HTTP: http.DefaultClient, BaseURL: "http://127.0.0.1:1"})

	req := httptest.NewRequest(http.MethodGet, "/.well-known/webfinger?resource=acct:nobody@example.com", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for unknown unaliased slug, got 200: %s", rec.Body.String())
	}
}

func TestHostMetaJSON(t *testing.T) {
	cfg := &Config{Domain: "example.com"}
	req := httptest.NewRequest(http.MethodGet, "/.well-known/host-meta.json", nil)
	rec := httptest.NewRecorder()
	hostMetaJSONHandler(cfg)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var doc struct {
		Links []struct {
			Rel      string `json:"rel"`
			Type     string `json:"type"`
			Template string `json:"template"`
		} `json:"links"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v, body=%s", err, rec.Body.String())
	}
	if len(doc.Links) != 1 || doc.Links[0].Rel != "lrdd" {
		t.Fatalf("unexpected links: %+v", doc.Links)
	}
	want := "https://example.com/.well-known/webfinger?resource={uri}"
	if doc.Links[0].Template != want {
		t.Errorf("template = %q, want %q", doc.Links[0].Template, want)
	}
}

// TestActorOrFrontendContentNegotiation verifies apActorHandler vs the HTML
// frontend handler are selected purely from the Accept header (issue #947).
func TestActorOrFrontendContentNegotiation(t *testing.T) {
	cases := []struct {
		accept string
		wantAP bool
	}{
		{"application/activity+json", true},
		{"application/ld+json", true},
		{"text/html", false},
		{"", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/org/myorg", nil)
		if tc.accept != "" {
			req.Header.Set("Accept", tc.accept)
		}
		if got := isAPRequest(req); got != tc.wantAP {
			t.Errorf("isAPRequest(Accept=%q) = %v, want %v", tc.accept, got, tc.wantAP)
		}
	}
}

// TestRelayActorURLSiteWideLink verifies every page (not just /org/{name})
// carries a <link rel="alternate" type="application/activity+json"> pointing
// at the relay actor, so federated crawlers that only look at page <head>
// can discover it from anywhere on the site (issue #951).
func TestRelayActorURLSiteWideLink(t *testing.T) {
	setupSiteCfg(t)
	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test", RelayActorName: "relay"}
	req := httptest.NewRequest(http.MethodGet, "/tags/bal-folk", nil)

	td := tmplData(req, cfg, i18n, "Bal-folk", TagPageData{
		Tag: Tag{Slug: "bal-folk", Name: "Bal-folk"},
	})
	if td.RelayActorURL != "https://example.test/org/relay" {
		t.Fatalf("RelayActorURL = %q, want https://example.test/org/relay", td.RelayActorURL)
	}

	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.tag, td)
	body := rec.Body.String()
	want := `<link rel="alternate" type="application/activity+json" href="https://example.test/org/relay">`
	if !strings.Contains(body, want) {
		t.Errorf("missing site-wide relay actor link; body tail: %s", body[max(0, len(body)-1500):])
	}
}

// TestRelayActorURLEmptyWhenUnconfigured guards against emitting a broken
// /org/ link (missing slug) if relay_actor_name is ever blanked out.
func TestRelayActorURLEmptyWhenUnconfigured(t *testing.T) {
	setupSiteCfg(t)
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test", RelayActorName: ""}
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	td := tmplData(req, cfg, i18n, "Home", nil)
	if td.RelayActorURL != "" {
		t.Errorf("RelayActorURL = %q, want empty when RelayActorName is unset", td.RelayActorURL)
	}
}
