package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var reJSONLDBlocks = regexp.MustCompile(`(?s)<script type="application/ld\+json">\s*(.*?)\s*</script>`)

// TestSmokeBreadcrumbJSONLD renders the event/location/org/musician/instructor
// pages and checks that every application/ld+json block is syntactically valid
// JSON, that a BreadcrumbList block is present, and that every top-level
// entity (including each node of the event @graph) carries a stable "@id"
// (#1064, #1061).
func TestSmokeBreadcrumbJSONLD(t *testing.T) {
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

	checkBlocks := func(t *testing.T, body string, wantBreadcrumb bool) {
		matches := reJSONLDBlocks.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			t.Fatalf("no ld+json blocks found")
		}
		sawBreadcrumb := false
		for _, m := range matches {
			var v map[string]any
			if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
				t.Fatalf("invalid JSON in ld+json block: %v\nblock: %s", err, m[1])
			}
			if graph, ok := v["@graph"].([]any); ok {
				for _, node := range graph {
					n, _ := node.(map[string]any)
					if id, _ := n["@id"].(string); id == "" {
						t.Errorf("JSON-LD @graph node of type %v lacks @id", n["@type"])
					}
				}
				continue
			}
			if v["@type"] == "BreadcrumbList" {
				sawBreadcrumb = true
				items, ok := v["itemListElement"].([]any)
				if !ok || len(items) < 2 {
					t.Fatalf("expected itemListElement with >=2 entries, got %v", v["itemListElement"])
				}
				continue
			}
			if id, _ := v["@id"].(string); id == "" {
				t.Errorf("JSON-LD block of type %v lacks @id", v["@type"])
			}
		}
		if wantBreadcrumb && !sawBreadcrumb {
			t.Fatalf("no BreadcrumbList block found")
		}
	}

	renderEvent := func(ev Event, org *Organization, orgSlug string) string {
		req := httptest.NewRequest(http.MethodGet, "/events/1", nil)
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, "test", EventData{Event: ev, Org: org, OrgSlug: orgSlug})
		renderTemplate(rec, tmpls.event, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		return string(body)
	}

	t.Run("event page @graph", func(t *testing.T) {
		body := renderEvent(Event{
			ID:         1,
			Title:      "Fest Noz",
			StartTime:  "2026-08-01T20:00:00Z",
			EndTime:    "2026-08-02T01:00:00Z",
			Location:   &Location{ID: 5, Location: "Salle des Fêtes", Address: "1 Rue de la Mairie", Town: "Rennes"},
			DanceNames: []string{"An Dro", "Hanter Dro"},
			Tags:       []string{"bal-folk", "musician-workshop"},
		}, &Organization{ID: 2, Name: "Test Org", ImageURL: "/api/v1/org-images/2"}, "test-org")
		checkBlocks(t, body, true)
		// #1061: single @graph block linking Event, Organization and Place by @id.
		if !strings.Contains(body, `"@graph"`) {
			t.Errorf("event JSON-LD should use @graph (#1061)")
		}
		for _, want := range []string{
			`"@id": "https:\/\/example.test\/events\/1"`,
			`"location": {"@id": "https://example.test/location/5"}`,
			`"organizer": {"@id": "https://example.test/org/test-org"}`,
			`"@id": "https://example.test/location/5"`,
			`"@id": "https://example.test/org/test-org"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("event @graph missing %s", want)
			}
		}
		// #1063: keywords is a JSON array, audienceType from a deterministic tag check.
		if !strings.Contains(body, `"keywords": ["An Dro", "Hanter Dro", "bal-folk", "musician-workshop"]`) {
			t.Errorf("event keywords should be a JSON array (#1063)")
		}
		if !strings.Contains(body, `"audienceType": "dancers, musicians"`) {
			t.Errorf("musician-workshop tag should yield audienceType \"dancers, musicians\" (#1063)")
		}
		// #1065: no event image -> org banner fallback; #1083 replaces the raw
		// org image with the generated title/date/location overlay, and the
		// URL is now made absolute like every other JSON-LD image (js filter
		// escapes "/").
		if !strings.Contains(body, `"image": "https:\/\/example.test\/api\/v1\/event-banner\/1"`) {
			t.Errorf("event without image should fall back to the generated org banner overlay (#1065, #1083)")
		}
	})

	t.Run("event page performer @id/sameAs", func(t *testing.T) {
		// #1292: performer entries need a stable @id (their own dansal page)
		// distinct from an optional sameAs (their external website) — a
		// musician with no Internetsite/instructor with no Website must omit
		// sameAs entirely rather than emit an empty/null value.
		body := renderEvent(Event{
			ID:        4,
			Title:     "Fest Noz Quatre",
			StartTime: "2026-08-07T20:00:00Z",
			EndTime:   "2026-08-08T01:00:00Z",
			Musicians: []Musician{
				{ID: 48, Bandname: "Duo Vague", Internetsite: "https://duovague.example.com"},
				{ID: 49, Bandname: "No Website Band"},
			},
			Instructors: []Instructor{
				{ID: 12, Name: "Instructor With Site", Website: "https://instructor-site.example.com"},
				{ID: 13, Name: "Instructor Without Site"},
			},
		}, nil, "")
		checkBlocks(t, body, true)

		matches := reJSONLDBlocks.FindAllStringSubmatch(body, -1)
		var graphDoc map[string]any
		for _, m := range matches {
			var v map[string]any
			json.Unmarshal([]byte(m[1]), &v)
			if _, ok := v["@graph"]; ok {
				graphDoc = v
				break
			}
		}
		if graphDoc == nil {
			t.Fatal("no @graph document found")
		}
		var eventNode map[string]any
		for _, node := range graphDoc["@graph"].([]any) {
			n := node.(map[string]any)
			if n["@type"] == "Event" {
				eventNode = n
				break
			}
		}
		if eventNode == nil {
			t.Fatal("no Event node in @graph")
		}
		performers, ok := eventNode["performer"].([]any)
		if !ok || len(performers) != 4 {
			t.Fatalf("expected 4 performer entries, got %v", eventNode["performer"])
		}
		byName := map[string]map[string]any{}
		for _, p := range performers {
			pm := p.(map[string]any)
			byName[pm["name"].(string)] = pm
		}

		duo := byName["Duo Vague"]
		if duo["@id"] != "https://example.test/musicians/48" {
			t.Errorf("Duo Vague @id = %v, want musician page URL", duo["@id"])
		}
		if duo["url"] != "https://example.test/musicians/48" {
			t.Errorf("Duo Vague url = %v, want musician page URL", duo["url"])
		}
		if same, _ := duo["sameAs"].([]any); len(same) != 1 || same[0] != "https://duovague.example.com" {
			t.Errorf("Duo Vague sameAs = %v, want [\"https://duovague.example.com\"]", duo["sameAs"])
		}

		noSiteBand := byName["No Website Band"]
		if _, ok := noSiteBand["sameAs"]; ok {
			t.Errorf("No Website Band should omit sameAs entirely, got %v", noSiteBand["sameAs"])
		}

		instrWithSite := byName["Instructor With Site"]
		if instrWithSite["@id"] != "https://example.test/instructors/12" {
			t.Errorf("Instructor With Site @id = %v, want instructor page URL", instrWithSite["@id"])
		}
		if same, _ := instrWithSite["sameAs"].([]any); len(same) != 1 || same[0] != "https://instructor-site.example.com" {
			t.Errorf("Instructor With Site sameAs = %v, want [\"https://instructor-site.example.com\"]", instrWithSite["sameAs"])
		}

		instrNoSite := byName["Instructor Without Site"]
		if instrNoSite["@id"] != "https://example.test/instructors/13" {
			t.Errorf("Instructor Without Site @id = %v, want instructor page URL", instrNoSite["@id"])
		}
		if _, ok := instrNoSite["sameAs"]; ok {
			t.Errorf("Instructor Without Site should omit sameAs entirely, got %v", instrNoSite["sameAs"])
		}
	})

	// eventNodeOf renders ev and returns the Event node from its @graph, for
	// tests that need to inspect offers/isAccessibleForFree directly.
	eventNodeOf := func(t *testing.T, body string) map[string]any {
		t.Helper()
		matches := reJSONLDBlocks.FindAllStringSubmatch(body, -1)
		for _, m := range matches {
			var v map[string]any
			json.Unmarshal([]byte(m[1]), &v)
			graph, ok := v["@graph"].([]any)
			if !ok {
				continue
			}
			for _, node := range graph {
				n := node.(map[string]any)
				if n["@type"] == "Event" {
					return n
				}
			}
		}
		t.Fatal("no Event node found in @graph")
		return nil
	}

	t.Run("event page offers (#1293)", func(t *testing.T) {
		t.Run("no Pricing, BookingURL set: Offer with url, no price", func(t *testing.T) {
			body := renderEvent(Event{
				ID: 5, Title: "Fest Noz Cinq", StartTime: "2026-08-09T20:00:00Z", EndTime: "2026-08-10T01:00:00Z",
				BookingURL: "https://tickets.example.com/event",
			}, nil, "")
			checkBlocks(t, body, true)
			ev := eventNodeOf(t, body)
			offers, ok := ev["offers"].(map[string]any)
			if !ok {
				t.Fatalf("expected an offers object, got %v", ev["offers"])
			}
			if offers["url"] != "https://tickets.example.com/event" {
				t.Errorf("offers.url = %v, want the booking URL", offers["url"])
			}
			if offers["availability"] != "https://schema.org/InStock" {
				t.Errorf("offers.availability = %v, want InStock", offers["availability"])
			}
			if _, hasPrice := offers["price"]; hasPrice {
				t.Errorf("offers.price should be absent (no known price), got %v", offers["price"])
			}
			if _, ok := ev["isAccessibleForFree"]; ok {
				t.Errorf("isAccessibleForFree should be absent, got %v", ev["isAccessibleForFree"])
			}
		})

		t.Run("no Pricing, no BookingURL: offers omitted entirely", func(t *testing.T) {
			body := renderEvent(Event{
				ID: 6, Title: "Fest Noz Six", StartTime: "2026-08-11T20:00:00Z", EndTime: "2026-08-12T01:00:00Z",
			}, nil, "")
			checkBlocks(t, body, true)
			ev := eventNodeOf(t, body)
			if _, ok := ev["offers"]; ok {
				t.Errorf("offers should be absent when there's no Pricing and no BookingURL, got %v", ev["offers"])
			}
			if _, ok := ev["isAccessibleForFree"]; ok {
				t.Errorf("isAccessibleForFree should be absent, got %v", ev["isAccessibleForFree"])
			}
		})

		t.Run("donation with no amount: neither offers nor isAccessibleForFree", func(t *testing.T) {
			body := renderEvent(Event{
				ID: 7, Title: "Fest Noz Sept", StartTime: "2026-08-13T20:00:00Z", EndTime: "2026-08-14T01:00:00Z",
				Pricing: &Pricing{Type: "donation"},
			}, nil, "")
			checkBlocks(t, body, true)
			ev := eventNodeOf(t, body)
			if _, ok := ev["offers"]; ok {
				t.Errorf("offers should be absent for an amountless donation, got %v", ev["offers"])
			}
			if _, ok := ev["isAccessibleForFree"]; ok {
				t.Errorf("isAccessibleForFree should be absent for an amountless donation (ambiguous, not asserted either way), got %v", ev["isAccessibleForFree"])
			}
		})

		t.Run("donation with a suggested amount: Offer with that price, still no isAccessibleForFree", func(t *testing.T) {
			body := renderEvent(Event{
				ID: 8, Title: "Fest Noz Huit", StartTime: "2026-08-15T20:00:00Z", EndTime: "2026-08-16T01:00:00Z",
				Pricing: &Pricing{Type: "donation", Amount: 5},
			}, nil, "")
			checkBlocks(t, body, true)
			ev := eventNodeOf(t, body)
			offers, ok := ev["offers"].(map[string]any)
			if !ok {
				t.Fatalf("expected an offers object, got %v", ev["offers"])
			}
			if offers["price"] != 5.0 {
				t.Errorf("offers.price = %v, want 5", offers["price"])
			}
			if _, ok := ev["isAccessibleForFree"]; ok {
				t.Errorf("isAccessibleForFree should still be absent even with a suggested amount, got %v", ev["isAccessibleForFree"])
			}
		})

		t.Run("free type unaffected: isAccessibleForFree true and an Offer", func(t *testing.T) {
			body := renderEvent(Event{
				ID: 9, Title: "Fest Noz Neuf", StartTime: "2026-08-17T20:00:00Z", EndTime: "2026-08-18T01:00:00Z",
				Pricing: &Pricing{Type: "free"},
			}, nil, "")
			checkBlocks(t, body, true)
			ev := eventNodeOf(t, body)
			if ev["isAccessibleForFree"] != true {
				t.Errorf("isAccessibleForFree = %v, want true", ev["isAccessibleForFree"])
			}
			if _, ok := ev["offers"].(map[string]any); !ok {
				t.Errorf("expected an offers object for a free event, got %v", ev["offers"])
			}
		})
	})

	t.Run("event page own image", func(t *testing.T) {
		body := renderEvent(Event{
			ID:        2,
			Title:     "Fest Noz Deux",
			StartTime: "2026-08-03T20:00:00Z",
			EndTime:   "2026-08-04T01:00:00Z",
			ImageURL:  "/api/v1/images/2",
		}, &Organization{ID: 2, Name: "Test Org", ImageURL: "/api/v1/org-images/2"}, "test-org")
		checkBlocks(t, body, true)
		// Relative image URLs are now made absolute in JSON-LD, same as og:image.
		if !strings.Contains(body, `"image": "https:\/\/example.test\/api\/v1\/images\/2"`) {
			t.Errorf("event image should win over the org image (#1065)")
		}
	})

	t.Run("event page no images", func(t *testing.T) {
		body := renderEvent(Event{
			ID:        3,
			Title:     "Fest Noz Trois",
			StartTime: "2026-08-05T20:00:00Z",
			EndTime:   "2026-08-06T01:00:00Z",
		}, &Organization{ID: 2, Name: "Test Org"}, "test-org")
		checkBlocks(t, body, true)
		// #1072: JSON-LD always emits an image, falling back to the website banner
		// when neither event, series, nor org has one. The js filter escapes
		// forward slashes, hence the \/\/ in the expected string.
		if !strings.Contains(body, `"image": "https:\/\/example.test\/banner.avif"`) {
			t.Errorf("event JSON-LD should fall back to website banner image when no other image exists (#1072)")
		}
		if !strings.Contains(body, `"audienceType": "dancers"`) {
			t.Errorf("event without musician tags should keep audienceType \"dancers\" (#1063)")
		}
	})

	t.Run("location page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/location/5", nil)
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, "test", LocationPageData{
			Location: Location{ID: 5, Location: "Salle des Fêtes"},
		})
		renderTemplate(rec, tmpls.location, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		checkBlocks(t, string(body), true)
	})

	t.Run("org page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/org/test-org", nil)
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, "test", OrgData{
			Org:  Organization{ID: 2, Name: "Test Org"},
			Slug: "test-org",
		})
		renderTemplate(rec, tmpls.org, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		checkBlocks(t, string(body), true)
	})

	t.Run("musician page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/musicians/7", nil)
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, "test", MusicianPageData{
			Musician: Musician{ID: 7, Bandname: "Duo Trad"},
		})
		renderTemplate(rec, tmpls.musician, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		checkBlocks(t, string(body), false)
	})

	t.Run("instructor page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/instructors/9", nil)
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, "test", InstructorPageData{
			Instructor: Instructor{ID: 9, Name: "Yann Durand"},
		})
		renderTemplate(rec, tmpls.instructor, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		checkBlocks(t, string(body), false)
	})
}
