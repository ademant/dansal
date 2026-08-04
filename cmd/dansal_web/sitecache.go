package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// siteSettingsCache reads contact, site_name and impressum_* from web.db at
// most once per ttl. Changes saved via webmin are visible within one window
// without any process signal or restart.
type siteSettingsCache struct {
	db        *sql.DB
	ttl       time.Duration
	imagesDir string

	mu                   sync.RWMutex
	at                   time.Time
	contact              string
	siteName             string
	impressum            map[string]string
	indexNowKey          string
	holidayCountry       string
	rescheduledBadgeDays int
	bannerAIGenerated    bool
	logoAIGenerated      bool
	aiBadgeExists        bool
}

func newSiteSettingsCache(db *sql.DB, imagesDir string) *siteSettingsCache {
	return &siteSettingsCache{db: db, ttl: 10 * time.Second, imagesDir: imagesDir}
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
	aiBadgeExists := siteAssetOnDiskExists(c.imagesDir, "ai-badge")
	c.mu.Lock()
	c.contact, c.siteName, c.impressum, c.indexNowKey, c.holidayCountry, c.rescheduledBadgeDays,
		c.bannerAIGenerated, c.logoAIGenerated, c.aiBadgeExists, c.at =
		contact, siteName, imp, indexNowKey, holidayCountry, rescheduledBadgeDays,
		bannerAIGenerated, logoAIGenerated, aiBadgeExists, time.Now()
	c.mu.Unlock()
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

func (c *siteSettingsCache) AIBadgeExists() bool {
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.aiBadgeExists
}

// siteAssetOnDiskExists checks whether a site asset file exists for the given key.
func siteAssetOnDiskExists(dir, key string) bool {
	if dir == "" {
		return false
	}
	for _, ext := range siteAssetExts {
		if _, err := os.Stat(filepath.Join(dir, key+ext)); err == nil {
			return true
		}
	}
	return false
}
