package main

import (
	"archive/zip"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ademant/dansal/internal/places"
)

// #1429: while typing, /search/geocode answers from the local place table
// only; Nominatim is asked only with full=1 (Enter) and only when the table
// has no hit.
func TestGeocodeHandlerPlacesThenNominatimOnEnter(t *testing.T) {
	db := initDB(filepath.Join(t.TempDir(), "web.db"))
	defer db.Close()
	db.Exec("INSERT INTO site_settings (key, value) VALUES ('place_countries', 'DE')")
	oldCfg := siteCfg
	siteCfg = newSiteSettingsCache(db)
	t.Cleanup(func() { siteCfg = oldCfg })

	// Import a one-place GeoNames dump through the real importer.
	dir := t.TempDir()
	f, _ := os.Create(filepath.Join(dir, "DE.zip"))
	zw := zip.NewWriter(f)
	w, _ := zw.Create("DE.txt")
	w.Write([]byte("2911298\tHamburg\tHamburg\t\t53.55\t9.99\tP\tPPLA\tDE\t\t04\t\t\t\t1845229\t\t8\tEurope/Berlin\t2024-01-01\n"))
	zw.Close()
	f.Close()
	os.WriteFile(filepath.Join(dir, "admin1CodesASCII.txt"), []byte("DE.04\tHamburg\tHamburg\t1\n"), 0o644)
	places.Sync(context.Background(), db, http.DefaultClient, "file://"+dir+"/", []string{"DE"}, false)

	nominatimCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nominatimCalls++
		w.Write([]byte(`[{"display_name":"Eggenfelden, Bayern","lat":"48.4","lon":"12.76"}]`))
	}))
	defer upstream.Close()
	oldBase := nominatimBaseURL
	nominatimBaseURL = upstream.URL
	t.Cleanup(func() { nominatimBaseURL = oldBase })
	oldThrottle := geocodeThrottle
	geocodeThrottle = newSubmissionThrottle(100, time.Minute)
	t.Cleanup(func() { geocodeThrottle = oldThrottle })

	h := geocodeHandler(&Config{Domain: "example.test"}, db)
	get := func(q string) string {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/search/geocode?"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
		return strings.TrimSpace(rec.Body.String())
	}

	if got := get("q=hamurg"); !strings.Contains(got, `"name":"Hamburg"`) || !strings.Contains(got, `"region":"Hamburg"`) || !strings.Contains(got, `"src":"geonames"`) {
		t.Errorf("typo query = %s", got)
	}
	if got := get("q=Eggenfe"); got != "[]" {
		t.Errorf("no place hit while typing must return [] without Nominatim, got %s", got)
	}
	if nominatimCalls != 0 {
		t.Fatalf("Nominatim called %d times while typing", nominatimCalls)
	}
	if got := get("q=Eggenfelden&full=1"); !strings.Contains(got, "Eggenfelden, Bayern") {
		t.Errorf("Enter fallback = %s", got)
	}
	if got := get("q=Hamburg&full=1"); !strings.Contains(got, `"src":"geonames"`) {
		t.Errorf("Enter with a place hit must not need Nominatim, got %s", got)
	}
	if nominatimCalls != 1 {
		t.Errorf("Nominatim calls = %d, want 1", nominatimCalls)
	}
}

// #1459: a postcode-shaped query searches the postcodes table (prefix match,
// no typo tolerance) instead of place names, and — only on Enter, when the
// local table has no hit — the Nominatim fallback uses postalcode=/
// countrycodes= instead of q=/featureType=settlement.
func TestGeocodeHandlerPostcodeQuery(t *testing.T) {
	db := initDB(filepath.Join(t.TempDir(), "web.db"))
	defer db.Close()
	db.Exec("INSERT INTO site_settings (key, value) VALUES ('place_countries', 'DE')")
	oldCfg := siteCfg
	siteCfg = newSiteSettingsCache(db)
	t.Cleanup(func() { siteCfg = oldCfg })

	dir := t.TempDir()
	writeFixtureZip(t, dir, "DE.zip", "DE.txt",
		"2911298\tHamburg\tHamburg\t\t53.55\t9.99\tP\tPPLA\tDE\t\t04\t\t\t\t1845229\t\t8\tEurope/Berlin\t2024-01-01\n")
	os.WriteFile(filepath.Join(dir, "admin1CodesASCII.txt"), []byte("DE.04\tHamburg\tHamburg\t1\n"), 0o644)
	zipDir := filepath.Join(dir, "zip")
	os.MkdirAll(zipDir, 0o755)
	writeFixtureZip(t, zipDir, "DE.zip", "DE.txt",
		"DE\t72108\tRottenburg am Neckar\tBaden-Württemberg\t08\tTübingen\t084\tTübingen\t08416\t48.4833\t8.9333\t4\n")
	places.Sync(context.Background(), db, http.DefaultClient, "file://"+dir+"/", []string{"DE"}, false)

	var gotQuery url.Values
	nominatimCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nominatimCalls++
		gotQuery = r.URL.Query()
		w.Write([]byte(`[{"display_name":"72108, Rottenburg am Neckar, Deutschland","lat":"48.48","lon":"8.93"}]`))
	}))
	defer upstream.Close()
	oldBase := nominatimBaseURL
	nominatimBaseURL = upstream.URL
	t.Cleanup(func() { nominatimBaseURL = oldBase })
	oldThrottle := geocodeThrottle
	geocodeThrottle = newSubmissionThrottle(100, time.Minute)
	t.Cleanup(func() { geocodeThrottle = oldThrottle })

	h := geocodeHandler(&Config{Domain: "example.test"}, db)
	get := func(q string) string {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/search/geocode?"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
		return strings.TrimSpace(rec.Body.String())
	}

	if got := get("q=721"); !strings.Contains(got, `"name":"72108 Rottenburg am Neckar"`) || !strings.Contains(got, `"src":"geonames"`) {
		t.Errorf("postcode prefix while typing = %s", got)
	}
	if got := get("q=Hamburg"); !strings.Contains(got, `"name":"Hamburg"`) {
		t.Errorf("a town name must still search places, not postcodes, got %s", got)
	}
	if nominatimCalls != 0 {
		t.Fatalf("Nominatim called %d times while typing", nominatimCalls)
	}

	// No local hit for this code; Enter must fall through to Nominatim using
	// postalcode=, not q=/featureType=settlement.
	if got := get("q=99999&full=1"); !strings.Contains(got, "Rottenburg am Neckar") {
		t.Errorf("Enter fallback = %s", got)
	}
	if nominatimCalls != 1 {
		t.Fatalf("Nominatim calls = %d, want 1", nominatimCalls)
	}
	if got := gotQuery.Get("postalcode"); got != "99999" {
		t.Errorf("Nominatim postalcode param = %q, want \"99999\"", got)
	}
	if got := gotQuery.Get("countrycodes"); got != "de" {
		t.Errorf("Nominatim countrycodes param = %q, want \"de\"", got)
	}
	if gotQuery.Get("q") != "" || gotQuery.Get("featureType") != "" {
		t.Errorf("postcode fallback must not send q=/featureType=, got %v", gotQuery)
	}
}

// writeFixtureZip creates dir/zipName containing one file (innerName) with
// the given content — a small shared helper for the DE.zip-of-DE.txt shape
// both the place and postcode dumps use.
func writeFixtureZip(t *testing.T, dir, zipName, innerName, content string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, zipName))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create(innerName)
	w.Write([]byte(content))
	zw.Close()
	f.Close()
}
