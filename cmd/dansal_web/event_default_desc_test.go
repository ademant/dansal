package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEventTypeBucket(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"ball via bal-folk", []string{"bal-folk"}, "ball"},
		{"ball via fest-noz", []string{"fest-noz"}, "ball"},
		{"workshop via dance-workshop", []string{"dance-workshop"}, "workshop"},
		{"workshop via musician-workshop", []string{"musician-workshop"}, "workshop"},
		{"workshop via music-course", []string{"music-course"}, "workshop"},
		{"festival", []string{"festival"}, "festival"},
		{"festival wins over workshop", []string{"festival", "workshop"}, "festival"},
		{"workshop wins over ball", []string{"workshop", "bal-folk"}, "workshop"},
		{"festival wins over ball too", []string{"bal-folk", "festival"}, "festival"},
		{"none of the buckets", []string{"session", "concert", "open-air"}, ""},
		{"no tags at all", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := eventTypeBucket(c.tags); got != c.want {
				t.Errorf("eventTypeBucket(%v) = %q, want %q", c.tags, got, c.want)
			}
		})
	}
}

func TestPricingSentence(t *testing.T) {
	strs := I18nStrings{"evt_admission": "Admission:", "evt_free": "Free", "evt_donation": "By donation"}

	cases := []struct {
		name string
		p    *Pricing
		want string
	}{
		{"nil pricing", nil, ""},
		{"free", &Pricing{Type: "free"}, "Admission: Free."},
		{"donation, no amount", &Pricing{Type: "donation"}, "Admission: By donation."},
		{"donation, with amount", &Pricing{Type: "donation", Amount: 5}, "Admission: 5 EUR."},
		{"single", &Pricing{Type: "single", Amount: 10}, "Admission: 10 EUR."},
		{"single, custom currency", &Pricing{Type: "single", Amount: 10, Currency: "USD"}, "Admission: 10 USD."},
		{"single, fractional amount", &Pricing{Type: "single", Amount: 7.5}, "Admission: 7.5 EUR."},
		{
			"multiple",
			&Pricing{Type: "multiple", Prices: []Price{{Label: "Adults", Amount: 10}, {Label: "Students", Amount: 5}}},
			"Admission: Adults: 10 EUR, Students: 5 EUR.",
		},
		{"multiple, no prices", &Pricing{Type: "multiple"}, ""},
		{"unknown type", &Pricing{Type: "bogus"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pricingSentence(strs, c.p); got != c.want {
				t.Errorf("pricingSentence(%+v) = %q, want %q", c.p, got, c.want)
			}
		})
	}
}

// TestDefaultEventDescription covers #1290's full composition: event-type
// sentence (from siteCfg, gated on the tag bucket), each associated dance's
// own description, and the admission sentence, joined in that order — and
// that an event matching none of the three produces "".
func TestDefaultEventDescription(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	old := siteCfg
	defer func() { siteCfg = old }()
	siteCfg = newSiteSettingsCache(db)

	strs := I18nStrings{"evt_admission": "Admission:", "evt_free": "Free", "evt_donation": "By donation"}

	t.Run("all three parts", func(t *testing.T) {
		ev := Event{
			Tags:       []string{"festival"},
			DanceNames: []string{"An Dro", "Undescribed Dance"},
			Pricing:    &Pricing{Type: "free"},
		}
		danceDesc := map[string]string{"An Dro": "A Breton chain dance."}
		got := defaultEventDescription(strs, "en", ev, danceDesc)
		want := siteCfg.DescFestival("en") + " A Breton chain dance. Admission: Free."
		if got != want {
			t.Errorf("defaultEventDescription() = %q, want %q", got, want)
		}
	})

	t.Run("no tags, no dances, no pricing: empty", func(t *testing.T) {
		if got := defaultEventDescription(strs, "en", Event{}, nil); got != "" {
			t.Errorf("defaultEventDescription(empty event) = %q, want empty", got)
		}
	})

	t.Run("only a dance description, no type or pricing", func(t *testing.T) {
		ev := Event{Tags: []string{"session"}, DanceNames: []string{"Waltz"}}
		got := defaultEventDescription(strs, "en", ev, map[string]string{"Waltz": "A couple dance in 3/4 time."})
		if got != "A couple dance in 3/4 time." {
			t.Errorf("defaultEventDescription() = %q, want just the dance sentence", got)
		}
	})
}

// TestSmokeRenderEventDefaultDescription covers the actual event.html
// render: when EventData.DefaultDescription is set (Event.Description
// empty), the exact same text appears in both the JSON-LD "description" and
// the visible "Description" section — not the old title/location/date
// placeholder, and not silently dropped from the visible page the way an
// empty Event.Description used to hide the section entirely.
func TestSmokeRenderEventDefaultDescription(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/events/1", nil)

	render := func(data EventData) string {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.event, tmplData(req, cfg, i18n, data.Event.Title, data))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		return string(body)
	}

	t.Run("default description used in both places", func(t *testing.T) {
		body := render(EventData{
			Event:              Event{ID: 1, Title: "Fest Noz", StartTime: "2026-08-01T20:00:00Z"},
			DefaultDescription: "This is a bal-folk dance evening. Admission: Free.",
		})
		if strings.Contains(body, "template error") {
			t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
		}
		if !strings.Contains(body, `"description": "This is a bal-folk dance evening. Admission: Free."`) {
			t.Errorf("expected the default description in JSON-LD, body tail: %s", body[max(0, len(body)-800):])
		}
		if !strings.Contains(body, `<p class="evt-description">This is a bal-folk dance evening. Admission: Free.</p>`) {
			t.Errorf("expected the default description in the visible section, body tail: %s", body[max(0, len(body)-800):])
		}
	})

	t.Run("real description wins over DefaultDescription", func(t *testing.T) {
		body := render(EventData{
			Event:              Event{ID: 2, Title: "Fest Noz", StartTime: "2026-08-01T20:00:00Z", Description: "The organizer's own words."},
			DefaultDescription: "Should never be used.",
		})
		if !strings.Contains(body, `"description": "The organizer&#39;s own words."`) && !strings.Contains(body, "The organizer&#39;s own words.") && !strings.Contains(body, "The organizer's own words.") {
			t.Errorf("expected the real description to win, body tail: %s", body[max(0, len(body)-800):])
		}
		if strings.Contains(body, "Should never be used.") {
			t.Errorf("DefaultDescription must not appear when Description is set")
		}
	})

	t.Run("neither set: old placeholder, no Description section at all", func(t *testing.T) {
		body := render(EventData{
			Event: Event{ID: 3, Title: "Fest Noz", StartTime: "2026-08-01T20:00:00Z"},
		})
		if !strings.Contains(body, `"description": "Fest Noz`) {
			t.Errorf("expected the old title-based placeholder, body tail: %s", body[max(0, len(body)-800):])
		}
		if strings.Contains(body, `class="evt-description"`) {
			t.Errorf("no Description section should render when neither Description nor DefaultDescription is set")
		}
	})
}
