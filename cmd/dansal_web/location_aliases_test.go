package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCountryRegionAliasSeeding checks the v6 migration actually creates both
// tables and seeds country_aliases from the #214 list (#1284).
func TestCountryRegionAliasSeeding(t *testing.T) {
	db := initDB(":memory:")
	defer db.Close()

	aliases, err := listCountryAliases(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) == 0 {
		t.Fatal("expected country_aliases to be seeded from the #214 list, got none")
	}
	got := map[string]string{}
	for _, a := range aliases {
		got[a.Alias] = a.Canonical
	}
	for _, want := range [][2]string{
		{"Deutschland", "Germany"},
		{"de", "Germany"},
		{"UK", "United Kingdom"},
	} {
		if got[want[0]] != want[1] {
			t.Errorf("alias %q: got canonical %q, want %q", want[0], got[want[0]], want[1])
		}
	}

	regionAliases, err := listRegionAliases(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(regionAliases) != 0 {
		t.Errorf("expected region_aliases to start empty, got %d rows", len(regionAliases))
	}
}

// TestLocationAliasCacheResolvesCaseInsensitively covers the exact bug report
// behind #1284: "de"/"DE"/"De"/"Deutschland" must all resolve to one
// canonical country name, and an unrelated raw value passes through
// unchanged. Region resolution is additionally scoped by country code.
func TestLocationAliasCacheResolvesCaseInsensitively(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE country_aliases (id INTEGER PRIMARY KEY, alias TEXT NOT NULL COLLATE NOCASE, canonical TEXT NOT NULL, UNIQUE(alias))`)
	db.Exec(`CREATE TABLE region_aliases (id INTEGER PRIMARY KEY, country_code TEXT NOT NULL, alias TEXT NOT NULL COLLATE NOCASE, canonical TEXT NOT NULL, UNIQUE(country_code, alias))`)
	db.Exec(`INSERT INTO country_aliases(alias, canonical) VALUES ('de', 'Germany'), ('Deutschland', 'Germany')`)
	db.Exec(`INSERT INTO region_aliases(country_code, alias, canonical) VALUES ('DE', 'Bayern', 'Bavaria')`)

	cache := newLocationAliasCache(db)

	for _, raw := range []string{"de", "DE", "De", "Deutschland", "deutschland"} {
		if got := cache.CanonicalCountry(raw); got != "Germany" {
			t.Errorf("CanonicalCountry(%q) = %q, want Germany", raw, got)
		}
	}
	if got := cache.CanonicalCountry("France"); got != "France" {
		t.Errorf("CanonicalCountry(France) = %q, want unchanged France", got)
	}
	if got := cache.CanonicalCountry(""); got != "" {
		t.Errorf("CanonicalCountry(\"\") = %q, want empty", got)
	}

	if got := cache.CanonicalRegion("bayern", "DE"); got != "Bavaria" {
		t.Errorf("CanonicalRegion(bayern, DE) = %q, want Bavaria", got)
	}
	// Same alias text under a different country code must not match — region
	// aliases are scoped, unlike country aliases.
	if got := cache.CanonicalRegion("bayern", "FR"); got != "bayern" {
		t.Errorf("CanonicalRegion(bayern, FR) = %q, want unchanged (wrong country scope)", got)
	}
}

// TestLocationDisplayCountryRegion checks Location.DisplayCountry/DisplayRegion
// go through the process-wide locAliasCache while leaving the raw
// Country/Region fields untouched (admin edit forms read those directly).
func TestLocationDisplayCountryRegion(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE country_aliases (id INTEGER PRIMARY KEY, alias TEXT NOT NULL COLLATE NOCASE, canonical TEXT NOT NULL, UNIQUE(alias))`)
	db.Exec(`CREATE TABLE region_aliases (id INTEGER PRIMARY KEY, country_code TEXT NOT NULL, alias TEXT NOT NULL COLLATE NOCASE, canonical TEXT NOT NULL, UNIQUE(country_code, alias))`)
	db.Exec(`INSERT INTO country_aliases(alias, canonical) VALUES ('de', 'Germany')`)

	prev := locAliasCache
	locAliasCache = newLocationAliasCache(db)
	defer func() { locAliasCache = prev }()

	loc := Location{Country: "de", CountryCode: "DE", Region: "Bayern"}
	if got := loc.DisplayCountry(); got != "Germany" {
		t.Errorf("DisplayCountry() = %q, want Germany", got)
	}
	if got := loc.Country; got != "de" {
		t.Errorf("raw Country field was mutated: %q", got)
	}
	// No region alias seeded for Bayern here, so it passes through unchanged.
	if got := loc.DisplayRegion(); got != "Bayern" {
		t.Errorf("DisplayRegion() = %q, want unchanged Bayern", got)
	}
}

// TestLocationDisplayCountryNilCache exercises the nil-locAliasCache fallback
// path other tests in this package rely on implicitly (most don't set up
// locAliasCache at all) — it must return the raw value, not panic.
func TestLocationDisplayCountryNilCache(t *testing.T) {
	prev := locAliasCache
	locAliasCache = nil
	defer func() { locAliasCache = prev }()

	loc := Location{Country: "France", Region: "Occitanie"}
	if got := loc.DisplayCountry(); got != "France" {
		t.Errorf("DisplayCountry() with nil cache = %q, want France", got)
	}
	if got := loc.DisplayRegion(); got != "Occitanie" {
		t.Errorf("DisplayRegion() with nil cache = %q, want Occitanie", got)
	}
}

// TestGenericAliasHandlersRoundTrip covers the admin add/delete handlers
// shared by country_aliases and region_aliases (#1284).
func TestGenericAliasHandlersRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE country_aliases (id INTEGER PRIMARY KEY AUTOINCREMENT, alias TEXT NOT NULL COLLATE NOCASE, canonical TEXT NOT NULL, UNIQUE(alias))`)

	addHandler := genericAliasNewHandler(db, "country_aliases", []string{"alias", "canonical"})
	delHandler := genericAliasDeleteHandler(db, "country_aliases")

	form := "alias=Deutschland&canonical=Germany"
	req := httptest.NewRequest(http.MethodPost, "/admin/enrich/country-aliases/new", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
	rec := httptest.NewRecorder()
	addHandler(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add: status=%d", rec.Code)
	}

	aliases, err := listCountryAliases(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].Alias != "Deutschland" || aliases[0].Canonical != "Germany" {
		t.Fatalf("unexpected aliases after add: %+v", aliases)
	}

	delReq := httptest.NewRequest(http.MethodPost, "/admin/enrich/country-aliases/1/delete", nil)
	delReq.SetPathValue("id", "1")
	delReq = withSessionUser(delReq, &SessionUser{ID: 1, Role: "admin"})
	delRec := httptest.NewRecorder()
	delHandler(delRec, delReq)
	if delRec.Code != http.StatusSeeOther {
		t.Fatalf("delete: status=%d", delRec.Code)
	}

	aliases, err = listCountryAliases(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 0 {
		t.Fatalf("expected alias to be deleted, got %+v", aliases)
	}
}
