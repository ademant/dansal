package places

import (
	"archive/zip"
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestNormalizeAndVariants(t *testing.T) {
	for in, want := range map[string]string{
		"München":                    "munchen",
		"Frankfurt (Oder)":           "frankfurt oder",
		"Neustadt an der Weinstraße": "neustadt an der weinstrasse",
		"  Angermünde ":              "angermunde",
		"Ærøskøbing":                 "aeroskobing",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	got := strings.Join(variants("München", "Muenchen"), ",")
	if got != "munchen,muenchen" {
		t.Errorf("variants = %s", got)
	}
}

func TestParseCountries(t *testing.T) {
	if got := strings.Join(ParseCountries(" de, at;ch  DE xyz 1A"), ","); got != "DE,AT,CH" {
		t.Errorf("ParseCountries = %s", got)
	}
}

func TestZipBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://download.geonames.org/export/dump/": "https://download.geonames.org/export/zip/",
		"https://download.geonames.org/export/dump":  "https://download.geonames.org/export/zip/",
		"file:///mirror/export/dump/":                "file:///mirror/export/zip/",
		"file:///tmp/fixture/":                       "file:///tmp/fixture/zip/", // no "dump" suffix: zip/ appended as a subdir
	}
	for in, want := range cases {
		if got := ZipBaseURL(in); got != want {
			t.Errorf("ZipBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsPostcodeQuery(t *testing.T) {
	cases := map[string]bool{
		"721":                   true,  // DE prefix while typing
		"72108":                 true,  // DE full code
		"1234 AB":               true,  // NL style
		"SW1A 1AA":              true,  // UK style
		"Hamburg":               false, // town name, no digit
		"Neustadt":              false,
		"ha":                    false, // too short for the shape regex
		strings.Repeat("1", 11): false, // too long
	}
	for in, want := range cases {
		if got := IsPostcodeQuery(in); got != want {
			t.Errorf("IsPostcodeQuery(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPrefixDistance(t *testing.T) {
	cases := []struct {
		q, s    string
		d, want int
	}{
		{"hambu", "hamburg", 1, 0},
		{"hamurg", "hamburg", 1, 1},      // missing letter
		{"magdeburh", "magdeburg", 2, 1}, // wrong letter
		{"lnggries", "lenggries", 2, 1},
		{"hmaburg", "hamburg", 2, 1}, // swapped letters
		{"berlin", "hamburg", 2, 3},  // > maxd
		{"eggenfe", "eggenfelden", 1, 0},
	}
	for _, c := range cases {
		if got := prefixDistance([]rune(c.q), []rune(c.s), c.d); got != c.want {
			t.Errorf("prefixDistance(%q, %q, %d) = %d, want %d", c.q, c.s, c.d, got, c.want)
		}
	}
}

// fixture is a tiny GeoNames country dump (19 tab-separated columns).
var fixture = []string{
	"2911298\tHamburg\tHamburg\tHH\t53.55073\t9.99302\tP\tPPLA\tDE\t\t04\t\t\t\t1845229\t\t8\tEurope/Berlin\t2024-01-01",
	"2911290\tHamberg\tHamberg\t\t48.9\t8.7\tP\tPPL\tDE\t\t01\t\t\t\t0\t\t300\tEurope/Berlin\t2024-01-01",
	"2867714\tMünchen\tMuenchen\tMunich\t48.13743\t11.57549\tP\tPPLA\tDE\t\t02\t\t\t\t1260391\t\t524\tEurope/Berlin\t2024-01-01",
	"2874545\tMagdeburg\tMagdeburg\t\t52.12773\t11.62916\tP\tPPLA\tDE\t\t14\t\t\t\t229826\t\t50\tEurope/Berlin\t2024-01-01",
	"2862850\tNeustadt\tNeustadt\t\t50.0\t9.0\tP\tPPL\tDE\t\t05\t\t\t\t3000\t\t100\tEurope/Berlin\t2024-01-01",
	"2862851\tNeustadt\tNeustadt\t\t51.0\t10.0\tP\tPPL\tDE\t\t15\t\t\t\t8000\t\t100\tEurope/Berlin\t2024-01-01",
	"2999999\tAlthof\tAlthof\t\t50.0\t9.0\tP\tPPLF\tDE\t\t05\t\t\t\t0\t\t100\tEurope/Berlin\t2024-01-01",                // farm: skipped
	"2888888\tHamburger Berg\tHamburger Berg\t\t50.0\t9.0\tT\tHLL\tDE\t\t05\t\t\t\t0\t\t100\tEurope/Berlin\t2024-01-01", // a hill: skipped
}

const admin1Fixture = "DE.04\tHamburg\tHamburg\t1\nDE.02\tBavaria\tBavaria\t2\nDE.14\tSaxony-Anhalt\tSaxony-Anhalt\t3\nDE.05\tHesse\tHesse\t4\nDE.15\tThuringia\tThuringia\t5\n"

// writeDump creates baseDir/DE.zip and admin1CodesASCII.txt.
func writeDump(t *testing.T, dir string, lines []string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "DE.zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("DE.txt")
	w.Write([]byte(strings.Join(lines, "\n") + "\n"))
	zw.Create("readme.txt")
	zw.Close()
	f.Close()
	os.WriteFile(filepath.Join(dir, "admin1CodesASCII.txt"), []byte(admin1Fixture), 0o644)
}

// postcodeFixture is a tiny GeoNames postal-code dump (12 tab-separated
// columns: country, postcode, place, admin name1, admin code1, admin name2,
// admin code2, admin name3, admin code3, lat, lng, accuracy).
var postcodeFixture = []string{
	"DE\t72108\tRottenburg am Neckar\tBaden-Württemberg\t08\tTübingen\t084\tTübingen\t08416\t48.4833\t8.9333\t4",
	"DE\t72141\tWalddorfhäslach\tBaden-Württemberg\t08\tTübingen\t084\tTübingen\t08416\t48.55\t9.1\t4",
	"DE\t50667\tKöln\tNordrhein-Westfalen\t05\tKöln\t053\tKöln\t05315\t50.9375\t6.9603\t4",
}

// writePostcodeDump creates dir/zip/DE.zip (sibling of dir's own place
// dump, matching ZipBaseURL's fallback convention for a non-"dump/" base).
func writePostcodeDump(t *testing.T, dir string, lines []string) {
	t.Helper()
	zipDir := filepath.Join(dir, "zip")
	if err := os.MkdirAll(zipDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(zipDir, "DE.zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("DE.txt")
	w.Write([]byte(strings.Join(lines, "\n") + "\n"))
	zw.Close()
	f.Close()
}

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSyncAndSearch(t *testing.T) {
	db := setupDB(t)
	dir := t.TempDir()
	writeDump(t, dir, fixture)
	base := "file://" + dir + "/"

	Sync(context.Background(), db, http.DefaultClient, base, []string{"DE"}, false)
	st, err := Statuses(db)
	if err != nil || len(st) != 1 || st[0].Status != "ok" || st[0].PlaceCount != 6 {
		t.Fatalf("status after import = %+v, %v", st, err)
	}

	names := func(q string) string {
		t.Helper()
		res, err := Search(db, []string{"DE"}, q, 8)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range res {
			out = append(out, r.Name+"/"+r.Region)
		}
		return strings.Join(out, ",")
	}
	cases := map[string]string{
		"ham":       "Hamburg/Hamburg,Hamberg/",          // prefix, bigger first
		"hamurg":    "Hamburg/Hamburg",                   // missing letter
		"Magdeburh": "Magdeburg/Saxony-Anhalt",           // wrong letter
		"münch":     "München/Bavaria",                   // umlaut
		"muench":    "München/Bavaria",                   // ue spelling
		"munch":     "München/Bavaria",                   // folded
		"neust":     "Neustadt/Thuringia,Neustadt/Hesse", // same name: population decides, state tells apart
		"alth":      "",                                  // farm not imported
		"hamburger": "Hamburg/Hamburg",                   // the hill "Hamburger Berg" isn't imported; the city is 2 edits away
	}
	for q, want := range cases {
		if got := names(q); got != want {
			t.Errorf("Search(%q) = %q, want %q", q, got, want)
		}
	}
	if res, _ := Search(db, []string{"DE"}, "ha", 8); res != nil {
		t.Errorf("2-letter query should return nothing, got %v", res)
	}
	if res, _ := Search(db, []string{"AT"}, "ham", 8); len(res) != 0 {
		t.Errorf("other country must not see DE places, got %v", res)
	}

	// Removing the country from the list deletes its rows.
	Sync(context.Background(), db, http.DefaultClient, base, nil, false)
	var n int
	db.QueryRow("SELECT COUNT(*) FROM places").Scan(&n)
	if st, _ := Statuses(db); n != 0 || len(st) != 0 {
		t.Fatalf("after removing DE: %d rows, statuses %+v", n, st)
	}
}

func TestSyncFailureKeepsOldRows(t *testing.T) {
	db := setupDB(t)
	dir := t.TempDir()
	writeDump(t, dir, fixture)
	base := "file://" + dir + "/"
	Sync(context.Background(), db, http.DefaultClient, base, []string{"DE"}, false)

	os.Remove(filepath.Join(dir, "DE.zip")) // re-import can't download
	Sync(context.Background(), db, http.DefaultClient, base, []string{"DE"}, true)
	st, _ := Statuses(db)
	if len(st) != 1 || st[0].Status != "error" || st[0].Error == "" || st[0].PlaceCount != 6 {
		t.Fatalf("status after failed re-import = %+v", st)
	}
	if res, _ := Search(db, []string{"DE"}, "hamb", 8); len(res) == 0 {
		t.Fatal("old rows must survive a failed re-import")
	}
}

// TestSyncAndSearchPostcodes covers #1459: postal codes import alongside
// places (best-effort — a missing/failed postcode dump doesn't fail the
// country's status), prefix search works with no typo tolerance, status
// reports the postcode count, and removing a country deletes its postcodes
// too.
func TestSyncAndSearchPostcodes(t *testing.T) {
	db := setupDB(t)
	dir := t.TempDir()
	writeDump(t, dir, fixture)
	writePostcodeDump(t, dir, postcodeFixture)
	base := "file://" + dir + "/"

	Sync(context.Background(), db, http.DefaultClient, base, []string{"DE"}, false)
	st, err := Statuses(db)
	if err != nil || len(st) != 1 || st[0].Status != "ok" || st[0].PostcodeCount != 3 {
		t.Fatalf("status after import = %+v, %v", st, err)
	}

	labels := func(q string) string {
		t.Helper()
		res, err := SearchPostcodes(db, []string{"DE"}, q, 8)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range res {
			out = append(out, r.Name+"/"+r.Region)
		}
		return strings.Join(out, ",")
	}
	if got, want := labels("721"), "72108 Rottenburg am Neckar/Baden-Württemberg,72141 Walddorfhäslach/Baden-Württemberg"; got != want {
		t.Errorf("prefix search = %q, want %q", got, want)
	}
	if got, want := labels("72108"), "72108 Rottenburg am Neckar/Baden-Württemberg"; got != want {
		t.Errorf("full code = %q, want %q", got, want)
	}
	if got := labels("50667"); got != "50667 Köln/Nordrhein-Westfalen" {
		t.Errorf("full code = %q", got)
	}
	// No typo tolerance for digits: a near-miss code must not match.
	if got := labels("72109"); got != "" {
		t.Errorf("unknown code should return nothing, got %q", got)
	}
	if res, _ := SearchPostcodes(db, []string{"AT"}, "721", 8); len(res) != 0 {
		t.Errorf("other country must not see DE postcodes, got %v", res)
	}

	// Removing the country deletes its postcodes too.
	Sync(context.Background(), db, http.DefaultClient, base, nil, false)
	var n int
	db.QueryRow("SELECT COUNT(*) FROM postcodes").Scan(&n)
	if n != 0 {
		t.Errorf("postcodes after removing DE: %d rows, want 0", n)
	}
}

// TestSyncPostcodeImportFailureDoesNotFailPlaceImport covers #1459: a
// country with no GeoNames postal-code dump (or a failed download for one)
// still gets a successful place import — postcode_count just stays 0.
func TestSyncPostcodeImportFailureDoesNotFailPlaceImport(t *testing.T) {
	db := setupDB(t)
	dir := t.TempDir()
	writeDump(t, dir, fixture)
	// Deliberately no writePostcodeDump call — dir/zip/DE.zip doesn't exist.
	base := "file://" + dir + "/"

	Sync(context.Background(), db, http.DefaultClient, base, []string{"DE"}, false)
	st, err := Statuses(db)
	if err != nil || len(st) != 1 || st[0].Status != "ok" || st[0].PlaceCount != 6 || st[0].PostcodeCount != 0 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if res, _ := Search(db, []string{"DE"}, "hamb", 8); len(res) == 0 {
		t.Fatal("place search must still work when postcode import failed")
	}
}
