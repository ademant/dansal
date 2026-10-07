package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// #1462: org slug redirects — 410 for deleted orgs, 301 for renamed ones.

// TestEnsureActorRenameRecordsSlugRedirect verifies ensureActor's rename
// branch writes a forwarding record, and that resolveOrgSlugRedirect
// chain-walks through more than one hop (A renamed to B, then to C) to the
// final live slug.
func TestEnsureActorRenameRecordsSlugRedirect(t *testing.T) {
	db := initDB(":memory:")
	defer db.Close()

	if _, err := ensureActor(db, 1, "slug-a"); err != nil {
		t.Fatalf("create actor: %v", err)
	}
	if _, err := ensureActor(db, 1, "slug-b"); err != nil {
		t.Fatalf("rename a->b: %v", err)
	}
	if target, gone := resolveOrgSlugRedirect(db, "slug-a"); gone || target != "slug-b" {
		t.Fatalf("resolveOrgSlugRedirect(slug-a) = (%q, %v), want (slug-b, false)", target, gone)
	}

	if _, err := ensureActor(db, 1, "slug-c"); err != nil {
		t.Fatalf("rename b->c: %v", err)
	}
	// The a->b hop is unchanged; resolving from the original slug must walk
	// through b to reach the current slug, c.
	if target, gone := resolveOrgSlugRedirect(db, "slug-a"); gone || target != "slug-c" {
		t.Fatalf("resolveOrgSlugRedirect(slug-a) after second rename = (%q, %v), want (slug-c, false)", target, gone)
	}
	if target, gone := resolveOrgSlugRedirect(db, "slug-b"); gone || target != "slug-c" {
		t.Fatalf("resolveOrgSlugRedirect(slug-b) = (%q, %v), want (slug-c, false)", target, gone)
	}

	// A slug with no redirect record at all.
	if target, gone := resolveOrgSlugRedirect(db, "never-existed"); gone || target != "" {
		t.Errorf("resolveOrgSlugRedirect(never-existed) = (%q, %v), want (\"\", false)", target, gone)
	}
}

// TestTombstoneOrgSlugResolvesGone verifies the delete path's tombstone
// sentinel (empty new_slug) resolves as gone, not as a redirect to "".
func TestTombstoneOrgSlugResolvesGone(t *testing.T) {
	db := initDB(":memory:")
	defer db.Close()

	tombstoneOrgSlug(db, "deleted-org")
	target, gone := resolveOrgSlugRedirect(db, "deleted-org")
	if !gone || target != "" {
		t.Fatalf("resolveOrgSlugRedirect(deleted-org) = (%q, %v), want (\"\", true)", target, gone)
	}
}

// TestEnsureActorReleasesStaleRedirectOnReuse verifies that when a slug
// previously tombstoned or redirected away is claimed by a (new or
// renamed-into) actor, the stale forwarding record is dropped rather than
// lingering — #1462's "a tombstoned slug must be released if a new org
// later takes the same slug".
func TestEnsureActorReleasesStaleRedirectOnReuse(t *testing.T) {
	db := initDB(":memory:")
	defer db.Close()

	tombstoneOrgSlug(db, "reused-slug")
	if _, gone := resolveOrgSlugRedirect(db, "reused-slug"); !gone {
		t.Fatalf("precondition: expected reused-slug to be tombstoned")
	}

	// A different org (org_id=42) now claims that same slug.
	if _, err := ensureActor(db, 42, "reused-slug"); err != nil {
		t.Fatalf("create actor: %v", err)
	}
	if target, gone := resolveOrgSlugRedirect(db, "reused-slug"); gone || target != "" {
		t.Errorf("resolveOrgSlugRedirect(reused-slug) after reuse = (%q, %v), want (\"\", false) — stale tombstone should be released", target, gone)
	}
}

// mockOrgsServer returns an httptest.Server answering GetOrganizations with
// orgs and GetOrganizationDetail per detail (id -> status code; 200 looks up
// orgs by id, anything else is returned verbatim as the status).
func mockOrgsServer(t *testing.T, orgs []Organization, detailStatus map[int]int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/organizations" {
			json.NewEncoder(w).Encode(orgs)
			return
		}
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/organizations/"))
		status := detailStatus[id]
		if status == 0 {
			status = http.StatusNotFound
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
			return
		}
		for _, o := range orgs {
			if o.ID == id {
				json.NewEncoder(w).Encode(o)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestOrgFrontendHandlerRedirectsRenamedSlug verifies the actor==nil branch
// (no live org currently claims the requested slug) consults
// org_slug_redirects and 301s to the renamed org's current slug.
func TestOrgFrontendHandlerRedirectsRenamedSlug(t *testing.T) {
	db := initDB(":memory:")
	defer db.Close()
	recordOrgSlugRename(db, "old-name", "new-name")

	srv := mockOrgsServer(t, nil, nil)
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	cfg := &Config{Domain: "example.test"}
	handler := orgFrontendHandler(cfg, loadTemplates(), db, client, loadI18n(""))

	req := httptest.NewRequest(http.MethodGet, "/org/old-name", nil)
	req.SetPathValue("name", "old-name")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301 (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/org/new-name" {
		t.Errorf("Location = %q, want /org/new-name", loc)
	}
}

// TestOrgFrontendHandlerGoneForDeletedOrg verifies the second 404 site (an
// actor row survives an org's deletion, so the org fetch itself 404s)
// returns 410 when a tombstone was recorded for that slug.
func TestOrgFrontendHandlerGoneForDeletedOrg(t *testing.T) {
	db := initDB(":memory:")
	defer db.Close()
	if _, err := ensureActor(db, 7, "deleted-org"); err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	tombstoneOrgSlug(db, "deleted-org")

	srv := mockOrgsServer(t, nil, map[int]int{7: http.StatusNotFound})
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	cfg := &Config{Domain: "example.test"}
	handler := orgFrontendHandler(cfg, loadTemplates(), db, client, loadI18n(""))

	req := httptest.NewRequest(http.MethodGet, "/org/deleted-org", nil)
	req.SetPathValue("name", "deleted-org")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestOrgFrontendHandlerPlainNotFoundWithoutRedirectRecord is the baseline:
// a slug that never existed and has no redirect record still 404s normally.
func TestOrgFrontendHandlerPlainNotFoundWithoutRedirectRecord(t *testing.T) {
	db := initDB(":memory:")
	defer db.Close()

	srv := mockOrgsServer(t, nil, nil)
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	cfg := &Config{Domain: "example.test"}
	handler := orgFrontendHandler(cfg, loadTemplates(), db, client, loadI18n(""))

	req := httptest.NewRequest(http.MethodGet, "/org/never-existed", nil)
	req.SetPathValue("name", "never-existed")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}
