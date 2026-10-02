package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// writeSchemaExemptions lists POST/PUT/PATCH route paths that deliberately have
// no OPTIONS schema responder. The value is a short family tag; the comment
// above each group carries the documented reason. API.md states these are
// "account/credential-provisioning endpoints that stay admin-driven rather than
// a self-service integration target"; this map makes that policy enforceable —
// a new write route must either register an OPTIONS responder or appear here
// with a reason. The audit below fails the build otherwise (#1383).
//
// Note: paths here are matched against the OPTIONS twin by exact string
// equality on the registered path (braces and all), so the entries must use the
// same spelling as in cmd/dansal/main.go. DELETE-only paths are not listed —
// they carry no request body, so there is nothing for a schema to describe.
var writeSchemaExemptions = map[string]string{
	// apikeys: admin API-key management; wp-dansal integrates via /apikeys/current + /apikeys/renew, which are covered separately
	"/api/v1/apikeys":                       "apikeys",
	"/api/v1/apikeys/renew":                 "apikeys",
	"/api/v1/apikeys/rotate-signing-secret": "apikeys",
	// auth: 2FA / passkey ceremony endpoints; request bodies are symmetric with the challenge response, not client-built JSON
	"/api/v1/auth/totp/confirm":            "auth", // deprecated alias of /me/totp/confirm (#1381)
	"/api/v1/auth/webauthn/login/begin":    "auth",
	"/api/v1/auth/webauthn/login/finish":   "auth",
	"/api/v1/auth/webauthn/totp-challenge": "auth",
	// board-sessions: board-session lifecycle; token handled in header/body
	"/api/v1/board-sessions":               "board-sessions",
	"/api/v1/board-sessions/renew":         "board-sessions",
	"/api/v1/board-sessions/renew-request": "board-sessions",
	// bookings: booking lifecycle; verify/status are single-field closures
	"/api/v1/bookings/verify":      "bookings",
	"/api/v1/bookings/{id}/status": "bookings",
	"/api/v1/events/{id}/bookings": "bookings",
	// category-aliases: admin category-alias maintenance
	"/api/v1/category-aliases": "category-aliases",
	// cert-login: mTLS certificate login; no request body
	"/api/v1/cert-login": "cert-login",
	// contact-posts: contact-post follow-ups; token-link / multipart flows
	"/api/v1/contact-posts/resend-manage": "contact-posts",
	"/api/v1/contact-posts/{id}/contact":  "contact-posts",
	"/api/v1/contact-posts/{id}/images":   "contact-posts",
	// dances: admin dance-dictionary maintenance
	"/api/v1/dances":      "dances",
	"/api/v1/dances/{id}": "dances",
	// events: admin/curator workflow on an already schema-covered resource (bulk-*, preview, suggest, publish, cancel, clone, enrich, pending-edit, timetable, syndication, join rows, assign-org, recheck-source, remove-from-series)
	"/api/v1/events/bulk-set-attributes":                  "events",
	"/api/v1/events/bulk-set-location":                    "events",
	"/api/v1/events/bulk-set-time":                        "events",
	"/api/v1/events/preview":                              "events",
	"/api/v1/events/suggest":                              "events",
	"/api/v1/events/suggest-preview":                      "events",
	"/api/v1/events/suggest/manage/{token}":               "events",
	"/api/v1/events/suggest/manage/{token}/image":         "events",
	"/api/v1/events/{id}/assign-org":                      "events",
	"/api/v1/events/{id}/cancel":                          "events",
	"/api/v1/events/{id}/clone":                           "events",
	"/api/v1/events/{id}/dances/{dance_id}":               "events",
	"/api/v1/events/{id}/enrich":                          "events",
	"/api/v1/events/{id}/instructors":                     "events",
	"/api/v1/events/{id}/instructors/{instructor_id}":     "events",
	"/api/v1/events/{id}/locations/{location_id}":         "events",
	"/api/v1/events/{id}/locations/{location_id}/primary": "events",
	"/api/v1/events/{id}/musicians/{musician_id}":         "events",
	"/api/v1/events/{id}/pending-edit/approve":            "events",
	"/api/v1/events/{id}/pending-edit/reject":             "events",
	"/api/v1/events/{id}/publish":                         "events",
	"/api/v1/events/{id}/recheck-source":                  "events",
	"/api/v1/events/{id}/remove-from-series":              "events",
	"/api/v1/events/{id}/syndicate/eventbrite":            "events",
	"/api/v1/events/{id}/syndicate/social-dance-today":    "events",
	"/api/v1/events/{id}/timetable":                       "events",
	"/api/v1/events/{id}/timetable/{entry_id}":            "events",
	// feeds (#1384, formerly /fetchurl — the fetchurl* entries below are its
	// deprecated aliases): bulk operations, suggestion flow, suggestion review,
	// manual refetch — same reasons as their fetchurl* twins
	"/api/v1/feeds/bulk-assign-org":          "feeds",
	"/api/v1/feeds/bulk-delete":              "feeds",
	"/api/v1/feeds/bulk-fetch":               "feeds",
	"/api/v1/feeds/suggest":                  "feeds",
	"/api/v1/feeds/suggest-preview":          "feeds",
	"/api/v1/feeds/suggestions/{id}/approve": "feeds",
	"/api/v1/feeds/suggestions/{id}/reject":  "feeds",
	"/api/v1/feeds/{id}/fetch":               "feeds",
	// fetchurl-suggestions: admin feed-suggestion review
	"/api/v1/fetchurl-suggestions/{id}/approve": "fetchurl-suggestions",
	"/api/v1/fetchurl-suggestions/{id}/reject":  "fetchurl-suggestions",
	// fetchurl-bulk: admin feed-import bulk operations
	"/api/v1/fetchurl/bulk-assign-org": "fetchurl-bulk",
	"/api/v1/fetchurl/bulk-delete":     "fetchurl-bulk",
	"/api/v1/fetchurl/bulk-fetch":      "fetchurl-bulk",
	// fetchurl-suggest: feed suggestion flow
	"/api/v1/fetchurl/suggest":         "fetchurl-suggest",
	"/api/v1/fetchurl/suggest-preview": "fetchurl-suggest",
	// fetchurl-fetch: admin manual feed refetch
	"/api/v1/fetchurl/{id}/fetch": "fetchurl-fetch",
	// images: multipart image upload; not a JSON body, so JSON-only reflection has nothing to describe
	// (owner-scoped since #1384; the *-images/*-avatars paths are deprecated aliases)
	"/api/v1/events/{event_id}/image":       "images",
	"/api/v1/instructors/{id}/avatar":       "images",
	"/api/v1/musicians/{id}/avatar":         "images",
	"/api/v1/musicians/{id}/image":          "images",
	"/api/v1/organizations/{id}/avatar":     "images",
	"/api/v1/organizations/{id}/image":      "images",
	"/api/v1/series/{id}/image":             "images",
	"/api/v1/images/{event_id}":             "images",
	"/api/v1/instructor-avatars/{id}":       "images",
	"/api/v1/musician-avatars/{id}":         "images",
	"/api/v1/musician-images/{id}":          "images",
	"/api/v1/musicians/{id}/gallery-images": "images",
	"/api/v1/org-avatars/{id}":              "images",
	"/api/v1/org-images/{id}":               "images",
	// invites: invitation / challenge flows; token-in-path
	"/api/v1/invites":                         "invites",
	"/api/v1/invites/{token}":                 "invites",
	"/api/v1/invites/{token}/publisher":       "invites",
	"/api/v1/invites/{token}/webauthn/begin":  "invites",
	"/api/v1/invites/{token}/webauthn/finish": "invites",
	// locations-admin: admin location surgery (merge, re-org, children, site-plan)
	"/api/v1/locations/bulk-assign-org": "locations-admin",
	"/api/v1/locations/merge":           "locations-admin",
	"/api/v1/locations/unassign-org":    "locations-admin",
	"/api/v1/locations/{id}/assign-org": "locations-admin",
	"/api/v1/locations/{id}/children":   "locations-admin",
	// PUT links a location to an org (#1380); no request body, the org id is in the path
	"/api/v1/locations/{id}/organizations/{org_id}": "locations-admin",
	"/api/v1/locations/{id}/site-plan":              "locations-admin",
	// login: session auth; form body documented in API.md, not a JSON object
	"/api/v1/login":       "login",
	"/api/v1/login/magic": "login",
	// oidc: OIDC redirect/callback ceremony; not self-service JSON APIs
	"/api/v1/oidc/callback":   "oidc",
	"/api/v1/oidc/link-start": "oidc",
	"/api/v1/oidc/providers":  "oidc",
	"/api/v1/oidc/start":      "oidc",
	// organizations: admin organization management
	"/api/v1/organizations":                  "organizations",
	"/api/v1/organizations/{id}":             "organizations",
	"/api/v1/organizations/{id}/members":     "organizations",
	"/api/v1/organizations/{id}/syndication": "organizations",
	// pending-invites: admin moderation queue
	"/api/v1/pending-invites/{id}/resend": "pending-invites",
	// pending-registrations: admin moderation queue
	"/api/v1/pending-registrations/{id}/approve": "pending-registrations",
	// publishers: admin publisher management
	"/api/v1/publishers":                                 "publishers",
	"/api/v1/publishers/token":                           "publishers",
	"/api/v1/publishers/{id}/reconnect-invite":           "publishers",
	"/api/v1/publishers/{id}/regenerate-key":             "publishers",
	"/api/v1/publishers/{id}/webhooks":                   "publishers",
	"/api/v1/publishers/{id}/webhooks/{webhook_id}":      "publishers",
	"/api/v1/publishers/{id}/webhooks/{webhook_id}/test": "publishers",
	// register: account bootstrap flows; token/challenge body
	"/api/v1/register":                "register",
	"/api/v1/register/passkey/begin":  "register",
	"/api/v1/register/passkey/finish": "register",
	"/api/v1/register/password":       "register",
	"/api/v1/register/resend/{token}": "register",
	// series: admin series management
	"/api/v1/series":                        "series",
	"/api/v1/series/{id}":                   "series",
	"/api/v1/series/{id}/add-date":          "series",
	"/api/v1/series/{id}/apply-to-events":   "series",
	"/api/v1/series/{id}/assign-events":     "series",
	"/api/v1/series/{id}/descriptions":      "series",
	"/api/v1/series/{id}/events/{event_id}": "series",
	"/api/v1/series/{id}/token/regenerate":  "series",
	"/api/v1/series/{id}/token/revoke":      "series",
	// series-by-token: publisher series edit-token flow
	"/api/v1/series-by-token/{token}/events/{eventID}": "series-by-token",
	// series-images: multipart image upload; not a JSON body
	"/api/v1/series-images/{id}": "series-images",
	// me: the caller's own account (#1381); password change and passkey
	// ceremony bodies are credential material, not integration JSON — the
	// same reason the auth family above is exempt
	"/api/v1/me/magic-link":               "me",
	"/api/v1/me/password":                 "me",
	"/api/v1/me/totp/confirm":             "me",
	"/api/v1/me/webauthn/register/begin":  "me",
	"/api/v1/me/webauthn/register/finish": "me",
	// user: deprecated aliases of the /me routes above (#1381)
	"/api/v1/user/password":                 "user",
	"/api/v1/user/webauthn/register/begin":  "user",
	"/api/v1/user/webauthn/register/finish": "user",
	// users: admin account management; /users/me/magic-link is a deprecated alias of /me/magic-link (#1381)
	"/api/v1/users/me/magic-link":         "users",
	"/api/v1/users/{id}":                  "users",
	"/api/v1/users/{id}/magic-link":       "users",
	"/api/v1/users/{id}/telegram/message": "users",
	"/api/v1/users/{id}/verify":           "users",
	// telegram-webhook: inbound Telegram bot callback; the body is Telegram's
	// documented Update schema, not a dansal JSON API request
	"/telegram/webhook": "telegram-webhook",
}

// writeRouteSchemaAudit enforces that every POST/PUT/PATCH route has either an
// OPTIONS schema responder on the same path or an explicit, documented entry in
// writeSchemaExemptions. It parses the source registrations directly (the same
// style the phase-18 audit uses) because Go's http.ServeMux cannot enumerate
// its patterns and the real registration function needs a live DB.
func TestWriteRoutesHaveSchemaDiscovery(t *testing.T) {
	src := readAllSources(t)

	writePaths := map[string]bool{}
	writeMethods := map[string]map[string]bool{}
	optionsPaths := map[string]bool{}
	for _, m := range routeReg.FindAllStringSubmatch(src, -1) {
		method, path := m[1], m[2]
		switch method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			writePaths[path] = true
			if writeMethods[path] == nil {
				writeMethods[path] = map[string]bool{}
			}
			writeMethods[path][method] = true
		case http.MethodOptions:
			// Hand-registered OPTIONS lines would bypass the registry.
			t.Errorf("OPTIONS %s is registered by hand — add it to writeSchemaRoutes (schema.go) instead", path)
		}
	}
	// OPTIONS responders come from the schema registry (schema.go).
	for _, sr := range writeSchemaRoutes {
		if optionsPaths[sr.Path] {
			t.Errorf("writeSchemaRoutes lists %s twice", sr.Path)
		}
		optionsPaths[sr.Path] = true
		// The registry's methods must be exactly the write methods registered
		// on that path, so the registry can describe the API faithfully.
		var reg []string
		for m := range writeMethods[sr.Path] {
			reg = append(reg, m)
		}
		sort.Strings(reg)
		got := append([]string(nil), sr.Methods...)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(reg, ",") {
			t.Errorf("writeSchemaRoutes %s lists methods %v, but %v are registered", sr.Path, got, reg)
		}
	}
	if len(writePaths) == 0 {
		t.Fatal("no POST/PUT/PATCH registrations resolved — the source-scanning assumptions broke, not the routes")
	}

	// Every write route is either schema-discovered or exempted with a reason.
	for _, path := range sortedKeys(writePaths) {
		if optionsPaths[path] {
			continue
		}
		if fam, ok := writeSchemaExemptions[path]; !ok {
			t.Errorf("write route %s has no OPTIONS schema responder and no exemption entry — add an OPTIONS responder or an entry to writeSchemaExemptions", path)
		} else if fam == "" {
			t.Errorf("writeSchemaExemptions[%s] has an empty reason — document why this route is exempt", path)
		}
	}

	// Stale exemptions: an entry that no longer matches a write route, or that is
	// now covered by an OPTIONS responder, must be removed.
	for _, path := range sortedStrings(writeSchemaExemptions) {
		if !writePaths[path] {
			t.Errorf("writeSchemaExemptions lists %q but no POST/PUT/PATCH route is registered at it — remove the stale entry", path)
		}
		if optionsPaths[path] {
			t.Errorf("writeSchemaExemptions lists %q which now has an OPTIONS schema responder — remove the stale entry", path)
		}
	}

	// Every OPTIONS responder must actually back a write route (no orphans).
	for _, path := range sortedKeys(optionsPaths) {
		if !writePaths[path] {
			t.Errorf("OPTIONS responder at %q has no write route — it is dead weight", path)
		}
	}
}

func sortedStrings(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// HEAD is served wherever GET is, by the stdlib mux: since Go 1.22 a "GET
// /path" pattern also matches HEAD, and the HTTP server suppresses the response
// body for HEAD while keeping Content-Length. This pins that behaviour so a
// future routing change can't silently break existence checks (#1383).
func TestHeadMatchesEveryGetRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("BODY"))
	})
	mux.HandleFunc("GET /api/v1/events/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("id=" + r.PathValue("id")))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := srv.Client()

	cases := []struct{ path string }{
		{"/api/v1/events"},
		{"/api/v1/events/42"},
	}
	for _, c := range cases {
		resp, err := client.Head(srv.URL + c.path)
		if err != nil {
			t.Fatalf("HEAD %s: %v", c.path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("HEAD %s read body: %v", c.path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("HEAD %s: status %d, want 200", c.path, resp.StatusCode)
		}
		if len(body) != 0 {
			t.Errorf("HEAD %s: body %q must be suppressed for HEAD", c.path, body)
		}
		if resp.ContentLength == 0 {
			t.Errorf("HEAD %s: Content-Length %d, want the GET body size so clients can detect the body exists", c.path, resp.ContentLength)
		}
	}

	// The GET response must still carry the body the HEAD promises.
	getResp, err := client.Get(srv.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if string(got) != "BODY" {
		t.Errorf("GET /api/v1/events: body %q, want BODY", got)
	}
}

// Every registry entry is served: OPTIONS returns a non-empty field schema
// for its request type (#1383).
func TestRegisterSchemaRoutesServesEveryEntry(t *testing.T) {
	mux := http.NewServeMux()
	registerSchemaRoutes(mux)
	for _, sr := range writeSchemaRoutes {
		path := strings.NewReplacer("{id}", "1").Replace(sr.Path)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, path, nil))
		var body struct {
			Fields map[string]any `json:"fields"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Fields) == 0 {
			t.Errorf("OPTIONS %s: status %d, body %s", path, rec.Code, rec.Body.String())
		}
	}
}

// HEAD on the .ics feeds, which icsRouter dispatches by hand (the mux can't
// express {id}.ics): it used to only handle GET, so HEAD fell through to a
// 404 while GET worked (#1383).
func TestICSRouterServesHead(t *testing.T) {
	old := db
	defer func() { db = old }()
	conn, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "calendar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn
	if err := createTables(); err != nil {
		t.Fatal(err)
	}
	migrateDB()
	if instanceTimezone == nil {
		instanceTimezone, _ = time.LoadLocation("Europe/Berlin")
	}
	srv := httptest.NewServer(icsRouter(http.NotFoundHandler()))
	defer srv.Close()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequest(method, srv.URL+"/api/v1/events.ics", nil)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/calendar") {
			t.Errorf("%s /api/v1/events.ics: %d %q", method, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}
