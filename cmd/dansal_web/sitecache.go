package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"time"
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
	dateFormat           string // "de" → DD.MM.YYYY; "" → locale-based
	timeFormatSite       string // "24h" or "12h" override (empty = use web.yaml)
	tileToken            string // #1269: public tile-proxy token, see getOrCreateTileToken in tiles.go
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
	c.mu.Lock()
	c.contact, c.siteName, c.impressum, c.indexNowKey, c.holidayCountry, c.rescheduledBadgeDays,
		c.defaultDanceIDs, c.bannerAIGenerated, c.logoAIGenerated,
		c.dateFormat, c.timeFormatSite, c.tileToken, c.at =
		contact, siteName, imp, indexNowKey, holidayCountry, rescheduledBadgeDays,
		defaultDanceIDs, bannerAIGenerated, logoAIGenerated,
		dateFormat, timeFormatSite, tileToken, time.Now()
	c.mu.Unlock()
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
