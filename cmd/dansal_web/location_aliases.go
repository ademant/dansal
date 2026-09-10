package main

import (
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Country/region name normalization (#1284, follow-up to #1282): the same
// country or region ends up stored under several free-text spellings
// ("Germany"/"Deutschland"/"de"), which #1282's /search filter then showed as
// separate dropdown entries instead of merging into one. country_aliases and
// region_aliases mirror city_aliases' alias→canonical shape (see db.go's v2
// migration) — a country/region can have many alias rows all resolving to the
// same canonical display string, which is exactly the "1:m" relationship the
// city_aliases table already models for free via its UNIQUE(alias) column.
// Region aliases are additionally scoped by country_code since region names
// can collide across countries (e.g. two countries both having a "Central"
// region), unlike country names which are compared globally.

type CountryAlias struct {
	ID        int
	Alias     string
	Canonical string
}

type RegionAlias struct {
	ID          int
	CountryCode string
	Alias       string
	Canonical   string
}

func listCountryAliases(db *sql.DB) ([]CountryAlias, error) {
	rows, err := db.Query("SELECT id, alias, canonical FROM country_aliases ORDER BY canonical, alias COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CountryAlias
	for rows.Next() {
		var a CountryAlias
		if err := rows.Scan(&a.ID, &a.Alias, &a.Canonical); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func listRegionAliases(db *sql.DB) ([]RegionAlias, error) {
	rows, err := db.Query("SELECT id, country_code, alias, canonical FROM region_aliases ORDER BY country_code, canonical, alias COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RegionAlias
	for rows.Next() {
		var a RegionAlias
		if err := rows.Scan(&a.ID, &a.CountryCode, &a.Alias, &a.Canonical); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// locationAliasCache resolves raw Location.Country/Region text to a canonical
// display string, backed by country_aliases/region_aliases. TTL-refreshed
// like siteSettingsCache so an admin adding an alias is visible within one
// window without a restart, while normal page renders never hit the DB.
type locationAliasCache struct {
	db  *sql.DB
	ttl time.Duration

	mu      sync.RWMutex
	at      time.Time
	country map[string]string // lowercase alias -> canonical
	region  map[string]string // country_code + "|" + lowercase alias -> canonical
}

func newLocationAliasCache(db *sql.DB) *locationAliasCache {
	return &locationAliasCache{db: db, ttl: 10 * time.Second}
}

func (c *locationAliasCache) load() {
	countryAliases, err := listCountryAliases(c.db)
	if err != nil {
		log.Printf("location alias cache: could not load country aliases: %v", err)
	}
	regionAliases, err := listRegionAliases(c.db)
	if err != nil {
		log.Printf("location alias cache: could not load region aliases: %v", err)
	}
	country := make(map[string]string, len(countryAliases))
	for _, a := range countryAliases {
		country[strings.ToLower(a.Alias)] = a.Canonical
	}
	region := make(map[string]string, len(regionAliases))
	for _, a := range regionAliases {
		region[strings.ToUpper(a.CountryCode)+"|"+strings.ToLower(a.Alias)] = a.Canonical
	}
	c.mu.Lock()
	c.country, c.region, c.at = country, region, time.Now()
	c.mu.Unlock()
}

func (c *locationAliasCache) ensure() {
	c.mu.RLock()
	stale := time.Since(c.at) > c.ttl
	c.mu.RUnlock()
	if stale {
		c.load()
	}
}

// CanonicalCountry resolves raw country text to its canonical display name,
// or returns raw unchanged when no alias is on file for it.
func (c *locationAliasCache) CanonicalCountry(raw string) string {
	if raw == "" {
		return raw
	}
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if canon, ok := c.country[strings.ToLower(raw)]; ok {
		return canon
	}
	return raw
}

// CanonicalRegion resolves raw region text to its canonical display name,
// scoped by countryCode, or returns raw unchanged when no alias is on file.
func (c *locationAliasCache) CanonicalRegion(raw, countryCode string) string {
	if raw == "" {
		return raw
	}
	c.ensure()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if canon, ok := c.region[strings.ToUpper(countryCode)+"|"+strings.ToLower(raw)]; ok {
		return canon
	}
	return raw
}

// locAliasCache is the process-wide instance, set in main() alongside siteCfg.
var locAliasCache *locationAliasCache

// canonicalCountry/canonicalRegion are package-level convenience wrappers so
// callers (Location's Display* methods, flattenLocationOptions) don't need to
// carry the cache around explicitly. A nil locAliasCache (e.g. in a unit test
// that doesn't set it up) is treated as "no aliases known" rather than a
// panic, matching the smoke tests' minimal setup.
func canonicalCountry(raw string) string {
	if locAliasCache == nil {
		return raw
	}
	return locAliasCache.CanonicalCountry(raw)
}

func canonicalRegion(raw, countryCode string) string {
	if locAliasCache == nil {
		return raw
	}
	return locAliasCache.CanonicalRegion(raw, countryCode)
}

// ── admin CRUD handlers ──────────────────────────────────────────────────────
//
// Both country_aliases and region_aliases follow the exact same
// alias(+scope)/canonical add-and-delete shape as city_aliases, so the
// handlers are generic over table name and column list rather than being
// copy-pasted per table. table/cols are internal constants, never user input.

// genericAliasNewHandler adds one alias row to table, reading each of cols
// from the POSTed form. Every column must be non-empty or the submission is
// silently dropped (mirrors adminEnrichAliasNewHandler's existing behavior).
func genericAliasNewHandler(db *sql.DB, table string, cols []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		vals := make([]any, len(cols))
		for i, col := range cols {
			v := strings.TrimSpace(r.FormValue(col))
			if v == "" {
				http.Redirect(w, r, "/admin/enrich", http.StatusSeeOther)
				return
			}
			vals[i] = v
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
		query := "INSERT OR REPLACE INTO " + table + "(" + strings.Join(cols, ",") + ") VALUES(" + placeholders + ")"
		if _, err := db.Exec(query, vals...); err != nil {
			log.Printf("could not insert %s row %v: %v", table, vals, err)
		}
		http.Redirect(w, r, "/admin/enrich", http.StatusSeeOther)
	}
}

// genericAliasDeleteHandler removes one alias row from table by id.
func genericAliasDeleteHandler(db *sql.DB, table string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Redirect(w, r, "/admin/enrich", http.StatusSeeOther)
			return
		}
		if _, err := db.Exec("DELETE FROM "+table+" WHERE id=?", id); err != nil {
			log.Printf("could not delete %s row %d: %v", table, id, err)
		}
		http.Redirect(w, r, "/admin/enrich", http.StatusSeeOther)
	}
}
