package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ademant/dansal/internal/webcommon"
	"gopkg.in/yaml.v2"
)

// siteSettingsCache reads contact, site_name and impressum_* from web.db at
// most once per ttl. Changes saved via webmin are visible within one window
// without any process signal or restart.
type siteSettingsCache struct {
	db  *sql.DB
	ttl time.Duration

	mu                   sync.RWMutex
	at                   time.Time
	contact              string
	siteName             string
	impressum            map[string]string
	indexNowKey          string
	holidayCountry       string
	rescheduledBadgeDays int
	defaultDanceIDs      []int
	bannerAIGenerated    bool
	logoAIGenerated      bool
	dateFormat           string            // "de" → DD.MM.YYYY; "" → locale-based
	timeFormatSite       string            // "24h" or "12h" override (empty = use web.yaml)
	tileToken            string            // #1269: public tile-proxy token, see getOrCreateTileToken in tiles.go
	sameAs               []string          // #1296: external profile URLs for the site-wide WebSite JSON-LD's sameAs
	homeIntro            map[string]string // #1298: lang -> homepage intro paragraph ("%s" placeholder for site name)
}

func newSiteSettingsCache(db *sql.DB) *siteSettingsCache {
	return &siteSettingsCache{db: db, ttl: 10 * time.Second}
}

func (c *siteSettingsCache) load() {
	contact := getSiteSetting(c.db, "contact")
	siteName := getSiteSetting(c.db, "site_name")
	indexNowKey := getSiteSetting(c.db, "indexnow_key")
	holidayCountry := getSiteSetting(c.db, "holiday_country")
	rescheduledBadgeDays := 7
	if v := getSiteSetting(c.db, "rescheduled_badge_days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			rescheduledBadgeDays = n
		}
	}
	imp := make(map[string]string)
	for _, lang := range impressumLangs {
		if v := getSiteSetting(c.db, "impressum_"+lang); v != "" {
			imp[lang] = v
		}
	}
	bannerAIGenerated := getSiteSetting(c.db, "banner_ai_generated") == "1"
	logoAIGenerated := getSiteSetting(c.db, "logo_ai_generated") == "1"
	defaultDanceIDs := parseDanceIDs(getSiteSetting(c.db, "default_dance_ids"))
	dateFormat := getSiteSetting(c.db, "date_format")
	timeFormatSite := getSiteSetting(c.db, "time_format")
	tileToken := getSiteSetting(c.db, "tile_token")
	sameAs := parseSameAs(getSiteSetting(c.db, "same_as"))
	homeIntro := parseHomeIntro(getSiteSetting(c.db, "home_intro"))
	c.mu.Lock()
	c.contact, c.siteName, c.impressum, c.indexNowKey, c.holidayCountry, c.rescheduledBadgeDays,
		c.defaultDanceIDs, c.bannerAIGenerated, c.logoAIGenerated,
		c.dateFormat, c.timeFormatSite, c.tileToken, c.sameAs, c.homeIntro, c.at =
		contact, siteName, imp, indexNowKey, holidayCountry, rescheduledBadgeDays,
		defaultDanceIDs, bannerAIGenerated, logoAIGenerated,
		dateFormat, timeFormatSite, tileToken, sameAs, homeIntro, time.Now()
	c.mu.Unlock()
}

// parseHomeIntro parses the webmin-managed home_intro setting — YAML text
// mapping language code to the homepage intro paragraph (#1298) — merged
// ON TOP OF webcommon.DefaultHomeIntroYAML rather than replacing it, so an
// admin who only edits (or only ever fills in) a subset of languages still
// gets working default text for every language they didn't touch, instead
// of that language silently going blank. A malformed edit is logged and
// ignored entirely, leaving the shipped default in place for every language.
func parseHomeIntro(raw string) map[string]string {
	m := map[string]string{}
	yaml.Unmarshal([]byte(webcommon.DefaultHomeIntroYAML), &m)
	if raw == "" {
		return m
	}
	var override map[string]string
	if err := yaml.Unmarshal([]byte(raw), &override); err != nil {
		log.Printf("could not parse home_intro YAML, using default for every language: %v", err)
		return m
	}
	for lang, text := range override {
		if text != "" {
			m[lang] = text
		}
	}
	return m
}

// parseSameAs splits the webmin-managed same_as setting (one URL per line)
// into a clean list — trimmed, blank lines dropped. Returns nil (not an
// empty slice) when nothing is configured, so callers can treat "no sameAs"
// and "not yet loaded" the same way.
func parseSameAs(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// parseDanceIDs decodes the JSON array stored in the default_dance_ids site
// setting, returning nil for a missing or unparseable value.
func parseDanceIDs(raw string) []int {
	if raw == "" {
		return nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		log.Printf("could not parse default_dance_ids %q: %v", raw, err)
		return nil
	}
	return ids
}

func (c *siteSettingsCache) ensure() {
	c.mu.RLock()
	stale := time.Since(c.at) > c.ttl
	c.mu.RUnlock()
	if stale {
		c.load()
	}
}

func (c *siteSettingsCache) Contact() string {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.contact
}

func (c *siteSettingsCache) SiteName() string {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.siteName
}

func (c *siteSettingsCache) IndexNowKey() string {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.indexNowKey
}

func (c *siteSettingsCache) HolidayCountry() string {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.holidayCountry
}

func (c *siteSettingsCache) TileToken() string {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tileToken
}

// SameAs returns the webmin-configured external profile URLs (#1296) for
// the site-wide WebSite JSON-LD's sameAs, or nil when none are configured.
func (c *siteSettingsCache) SameAs() []string {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sameAs
}

// HomeIntro returns the "%s"-templated homepage intro paragraph for lang
// (#1298), falling back to "de" when lang isn't present — matching
// pages.go's existing fallback convention for this kind of admin-editable
// site text — or "" if neither is set (home_intro's own parse failure
// already falls back to the shipped default before this is ever reached,
// so "" here only happens for a lang genuinely absent from both).
func (c *siteSettingsCache) HomeIntro(lang string) string {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if v, ok := c.homeIntro[lang]; ok {
		return v
	}
	return c.homeIntro["de"]
}

// DefaultDanceIDs returns the admin-configured dance presets for the event
// form, or nil when none are set.
func (c *siteSettingsCache) DefaultDanceIDs() []int {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.defaultDanceIDs
}

// RescheduledBadgeDays returns how many days before an event's start_time the
// "Rescheduled" badge should be shown on its public page (#927). Default 7.
func (c *siteSettingsCache) RescheduledBadgeDays() int {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rescheduledBadgeDays
}

func (c *siteSettingsCache) Impressum() map[string]string {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := make(map[string]string, len(c.impressum))
	for k, v := range c.impressum {
		cp[k] = v
	}
	return cp
}

func (c *siteSettingsCache) BannerAIGenerated() bool {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bannerAIGenerated
}

func (c *siteSettingsCache) LogoAIGenerated() bool {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.logoAIGenerated
}

// DateFormat returns the site-wide date notation override.
// "de" means DD.MM.YYYY (numeric); "" means locale-based (the default).
func (c *siteSettingsCache) DateFormat() string {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dateFormat
}

// TimeFormatSite returns the site-wide time notation override from the
// database, or "" when none is set (falls back to web.yaml time_format).
func (c *siteSettingsCache) TimeFormatSite() string {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.timeFormatSite
}
