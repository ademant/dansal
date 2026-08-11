package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// TestAPImageURL verifies the AP image URL always points at the JPEG variant
// (?format=jpeg) so the declared image/jpeg mediaType is honest (issue #1054).
func TestAPImageURL(t *testing.T) {
	cfg := &Config{Domain: "example.com"}
	cases := []struct {
		name  string
		in    string
		want  string
	}{
		{"empty", "", ""},
		{"relative", "/api/v1/images/5", "https://example.com/api/v1/images/5?format=jpeg"},
		{"absolute no query", "https://example.com/api/v1/images/5", "https://example.com/api/v1/images/5?format=jpeg"},
		{"absolute existing query", "https://cdn.example.com/x?size=large", "https://cdn.example.com/x?size=large&format=jpeg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := apImageURL(cfg, tc.in); got != tc.want {
				t.Errorf("apImageURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestBuildNoteContentMarkdown verifies the event description is rendered as
// HTML (goldmark) rather than leaking raw Markdown into Mastodon's Note.content
// (issue #1053).
func TestBuildNoteContentMarkdown(t *testing.T) {
	cfg := &Config{Domain: "example.com"}
	cases := []struct {
		name string
		desc string
		want string
	}{
		{
			name: "emphasis",
			desc: "Tanz mit *unserem* Orchester",
			want: "<p>Tanz mit <em>unserem</em> Orchester</p>",
		},
		{
			name: "heading",
			desc: "## Zielgruppe\n\nalle",
			want: "<h2>Zielgruppe</h2>\n<p>alle</p>",
		},
		{
			name: "empty",
			desc: "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := buildNoteContent(cfg, Event{ID: 1, Title: "T", Description: tc.desc})
			if !strings.Contains(content, tc.want) {
				t.Errorf("buildNoteContent description = %q, want it to contain %q", content, tc.want)
			}
			if tc.desc != "" && strings.Contains(content, "*") && strings.Contains(content, "##") {
				t.Errorf("buildNoteContent leaked raw markdown: %q", content)
			}
		})
	}
}

// TestEventAttachmentDeclaresJpegVariant verifies both the Event object and the
// Note attachment point at ?format=jpeg with mediaType image/jpeg (issue #1054).
func TestEventAttachmentDeclaresJpegVariant(t *testing.T) {
	cfg := &Config{Domain: "example.com"}
	e := Event{ID: 5, Title: "Fest Noz", ImageURL: "/api/v1/images/5"}

	apEvent := buildAPEvent(cfg, "myorg", e)
	if len(apEvent.Attachment) != 1 {
		t.Fatalf("APEvent.Attachment length = %d, want 1", len(apEvent.Attachment))
	}
	if got := apEvent.Attachment[0].MediaType; got != "image/jpeg" {
		t.Errorf("APEvent mediaType = %q, want image/jpeg", got)
	}
	if got := apEvent.Attachment[0].URL; got != "https://example.com/api/v1/images/5?format=jpeg" {
		t.Errorf("APEvent URL = %q, want jpeg variant", got)
	}

	note := buildNoteFromEvent(cfg, "myorg", e)
	if len(note.Attachment) != 1 {
		t.Fatalf("APNote.Attachment length = %d, want 1", len(note.Attachment))
	}
	if got := note.Attachment[0].MediaType; got != "image/jpeg" {
		t.Errorf("APNote mediaType = %q, want image/jpeg", got)
	}
	if got := note.Attachment[0].URL; got != "https://example.com/api/v1/images/5?format=jpeg" {
		t.Errorf("APNote URL = %q, want jpeg variant", got)
	}

	noImg := Event{ID: 6, Title: "No image"}
	if got := buildNoteFromEvent(cfg, "myorg", noImg).Attachment; len(got) != 0 {
		t.Errorf("attachment present without image: %+v", got)
	}
}

// TestRelayActorBrowserRedirect verifies a browser visit to /org/{relay}
// redirects to the homepage instead of 404ing, because the relay actor is
// synthetic and has no backing org page (issue #1057).
func TestRelayActorBrowserRedirect(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.com", RelayActorName: "relay"}
	handler := orgFrontendHandler(cfg, tmpls, db, nil, i18n)

	req := httptest.NewRequest(http.MethodGet, "/org/relay", nil)
	req.SetPathValue("name", "relay")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302 (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/" {
		t.Errorf("Location = %q, want https://example.com/", loc)
	}
}

// TestActorCacheControlHeader verifies AP actor documents set a short
// Cache-Control hint so proxies and Mastodon don't refetch every request
// (issue #1058).
func TestActorCacheControlHeader(t *testing.T) {
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

	cfg := &Config{Domain: "example.com", RelayActorName: "relay"}
	handler := apActorHandler(cfg, db, nil)

	req := httptest.NewRequest(http.MethodGet, "/org/relay", nil)
	req.Header.Set("Accept", "application/activity+json")
	req.SetPathValue("name", "relay")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("Cache-Control = %q, want public, max-age=300", cc)
	}
}

// TestWebfingerOrgActorProfilePage verifies org actor WebFinger responses
// advertise the profile-page rel so Mastodon can show an "Open original"
// button on org actor profiles (issue #1056).
func TestWebfingerOrgActorProfilePage(t *testing.T) {
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
	if _, err := db.Exec(`INSERT INTO actors (org_id, org_slug, public_key_pem, private_key_pem) VALUES (1, 'myorg', 'pub', 'priv')`); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{Domain: "example.com"}
	handler := webfingerHandler(cfg, db, nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/webfinger?resource=acct:myorg@example.com", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var wf WebFinger
	if err := json.Unmarshal(rec.Body.Bytes(), &wf); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	sawSelf := false
	sawProfilePage := false
	for _, l := range wf.Links {
		if l.Rel == "self" && l.Type == "application/activity+json" {
			sawSelf = true
		}
		if l.Rel == "http://webfinger.net/rel/profile-page" && l.Type == "text/html" && l.Href == "https://example.com/org/myorg" {
			sawProfilePage = true
		}
	}
	if !sawSelf {
		t.Errorf("missing self link in %+v", wf.Links)
	}
	if !sawProfilePage {
		t.Errorf("missing profile-page link in %+v", wf.Links)
	}
}

// TestOutboxPagination verifies the outbox reports the real totalItems and
// pages through the full history via offset-based next links, instead of
// truncating at the API's default limit of 100 (issue #1055).
func TestOutboxPagination(t *testing.T) {
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

	const total = 250
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		offset := 0
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}
		end := offset + limit
		if end > total {
			end = total
		}
		events := make([]Event, 0, end-offset)
		for i := offset; i < end; i++ {
			events = append(events, Event{ID: i + 1, Title: fmt.Sprintf("Event %d", i+1), IsPublished: true})
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	}))
	defer srv.Close()

	cfg := &Config{Domain: "example.test", RelayActorName: "relay"}
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	handler := outboxHandler(cfg, db, client)

	// Root collection reports the real count, not the page size.
	req := httptest.NewRequest(http.MethodGet, "/org/relay/outbox", nil)
	req.SetPathValue("name", "relay")
	req.Header.Set("Accept", "application/activity+json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("collection status=%d body=%s", rec.Code, rec.Body.String())
	}
	var col OrderedCollection
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatalf("decode collection: %v", err)
	}
	if col.TotalItems != total {
		t.Errorf("collection totalItems = %d, want %d", col.TotalItems, total)
	}
	wantFirst := "https://example.test/org/relay/outbox?page=true"
	if col.First != wantFirst {
		t.Errorf("collection first = %q, want %q", col.First, wantFirst)
	}

	// First page holds outboxPageSize items and links to the next offset.
	req2 := httptest.NewRequest(http.MethodGet, "/org/relay/outbox?page=true", nil)
	req2.SetPathValue("name", "relay")
	req2.Header.Set("Accept", "application/activity+json")
	rec2 := httptest.NewRecorder()
	handler(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("page status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	var page OrderedCollectionPage
	if err := json.Unmarshal(rec2.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if page.TotalItems != total {
		t.Errorf("page totalItems = %d, want %d", page.TotalItems, total)
	}
	if len(page.OrderedItems) != outboxPageSize {
		t.Errorf("first page has %d items, want %d", len(page.OrderedItems), outboxPageSize)
	}
	wantNext := "https://example.test/org/relay/outbox?page=true&offset=100"
	if page.Next != wantNext {
		t.Errorf("first page next = %q, want %q", page.Next, wantNext)
	}
	if page.Prev != "" {
		t.Errorf("first page prev = %q, want empty", page.Prev)
	}

	// Last page: no next link, prev points back one page.
	req3 := httptest.NewRequest(http.MethodGet, "/org/relay/outbox?page=true&offset=200", nil)
	req3.SetPathValue("name", "relay")
	req3.Header.Set("Accept", "application/activity+json")
	rec3 := httptest.NewRecorder()
	handler(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("last page status=%d body=%s", rec3.Code, rec3.Body.String())
	}
	var page3 OrderedCollectionPage
	if err := json.Unmarshal(rec3.Body.Bytes(), &page3); err != nil {
		t.Fatalf("decode last page: %v", err)
	}
	if len(page3.OrderedItems) != total-outboxPageSize*2 {
		t.Errorf("last page has %d items, want %d", len(page3.OrderedItems), total-outboxPageSize*2)
	}
	if page3.Next != "" {
		t.Errorf("last page next = %q, want empty", page3.Next)
	}
	if page3.Prev != "https://example.test/org/relay/outbox?page=true&offset=100" {
		t.Errorf("last page prev = %q, want offset=100", page3.Prev)
	}
}
