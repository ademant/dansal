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
