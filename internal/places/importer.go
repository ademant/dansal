package places

import (
	"archive/zip"
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is GeoNames' per-country dump directory.
const DefaultBaseURL = "https://download.geonames.org/export/dump/"

// ZipBaseURL derives the GeoNames postal-code zip directory (#1459) from the
// populated-place dump directory: on the real server, export/zip/ is a
// sibling of export/dump/, so a baseURL ending in ".../dump/" becomes
// ".../zip/". A baseURL that doesn't end in "dump" (a non-standard mirror,
// or a file:// fixture) gets "zip/" appended as a subdirectory instead —
// still a reasonable "next to the dump" convention, just not a literal
// sibling in that case.
func ZipBaseURL(baseURL string) string {
	trimmed := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "dump")
	return strings.TrimRight(trimmed, "/") + "/zip/"
}

// maxDownload caps a single downloaded file.
const maxDownload = 300 << 20

// skipFeature lists populated-place codes not worth suggesting: historical,
// abandoned, destroyed places and farms.
var skipFeature = map[string]bool{"PPLH": true, "PPLQ": true, "PPLW": true, "PPLCH": true, "PPLF": true}

// ImportStatus is one row of places_import.
type ImportStatus struct {
	Country       string
	Status        string // "running", "ok", "error"
	PlaceCount    int
	PostcodeCount int // #1459: >0 once imported. 0 means never attempted; -1 means attempted but no dump/failed (#1476) — see Sync.
	ImportedAt    time.Time
	Error         string
}

// Statuses returns the import state of every country that has one.
func Statuses(db *sql.DB) ([]ImportStatus, error) {
	rows, err := db.Query("SELECT country, status, place_count, postcode_count, imported_at, error FROM places_import ORDER BY country")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportStatus
	for rows.Next() {
		var s ImportStatus
		var at int64
		if err := rows.Scan(&s.Country, &s.Status, &s.PlaceCount, &s.PostcodeCount, &at, &s.Error); err != nil {
			return nil, err
		}
		if at > 0 {
			s.ImportedAt = time.Unix(at, 0)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

var syncMu sync.Mutex

// Sync brings the place table in line with countries: rows of countries no
// longer listed are deleted, and every listed country without a successful
// import (or every one, when force) is imported from baseURL. A country
// already successfully imported before postal codes existed (#1459) —
// status "ok" but postcode_count still 0, its never-attempted sentinel — gets
// just its postcodes imported, without redownloading/reimporting its places
// (#1476). Runs are serialized; a failure is recorded per country and
// doesn't stop the others.
func Sync(ctx context.Context, db *sql.DB, client *http.Client, baseURL string, countries []string, force bool) {
	syncMu.Lock()
	defer syncMu.Unlock()
	if err := EnsureSchema(db); err != nil {
		log.Printf("places: schema: %v", err)
		return
	}
	keep := map[string]bool{}
	for _, c := range countries {
		keep[c] = true
	}
	if old, err := Statuses(db); err == nil {
		for _, s := range old {
			if !keep[s.Country] {
				db.Exec("DELETE FROM places WHERE country = ?", s.Country)
				db.Exec("DELETE FROM postcodes WHERE country = ?", s.Country)
				db.Exec("DELETE FROM places_import WHERE country = ?", s.Country)
			}
		}
	}

	var todo, postcodesOnly []string
	for _, c := range countries {
		var status string
		var postcodeCount int
		db.QueryRow("SELECT status, postcode_count FROM places_import WHERE country = ?", c).Scan(&status, &postcodeCount)
		switch {
		case force || status != "ok":
			todo = append(todo, c)
		case postcodeCount == 0:
			postcodesOnly = append(postcodesOnly, c)
		}
	}
	if len(todo) == 0 && len(postcodesOnly) == 0 {
		return
	}
	var admin1 map[string]string
	if len(todo) > 0 {
		var err error
		admin1, err = loadAdmin1(ctx, client, baseURL)
		if err != nil {
			log.Printf("places: state names unavailable, importing without them: %v", err)
		}
	}
	for _, c := range todo {
		setStatus(db, c, "running", 0, 0, "")
		n, err := importCountry(ctx, db, client, baseURL, c, admin1)
		if err != nil {
			log.Printf("places: import %s: %v", c, err)
			setStatus(db, c, "error", 0, 0, err.Error())
			continue
		}
		// #1459: postal codes are best-effort — not every country has a
		// GeoNames postal-code dump, and a failure here must not undo the
		// place import that just succeeded. -1 (rather than 0) records that
		// an import was attempted and found nothing, so Sync doesn't keep
		// retrying a country with no dump on every future run (#1476).
		pn, pErr := importPostcodes(ctx, db, client, ZipBaseURL(baseURL), c)
		if pErr != nil {
			log.Printf("places: postcode import %s: %v (place import still ok)", c, pErr)
			pn = -1
		}
		log.Printf("places: imported %s: %d places, %d postcodes", c, n, pn)
		setStatus(db, c, "ok", n, pn, "")
	}
	for _, c := range postcodesOnly {
		pn, pErr := importPostcodes(ctx, db, client, ZipBaseURL(baseURL), c)
		if pErr != nil {
			log.Printf("places: postcode import %s: %v", c, pErr)
			pn = -1
		}
		log.Printf("places: imported %s: %d postcodes (places already ok, unchanged)", c, pn)
		db.Exec("UPDATE places_import SET postcode_count = ? WHERE country = ?", pn, c)
	}
}

func setStatus(db *sql.DB, country, status string, count, postcodeCount int, errMsg string) {
	if status == "ok" {
		db.Exec(`INSERT INTO places_import (country, status, place_count, postcode_count, imported_at, error) VALUES (?, ?, ?, ?, ?, '')
			ON CONFLICT(country) DO UPDATE SET status=excluded.status, place_count=excluded.place_count,
			postcode_count=excluded.postcode_count, imported_at=excluded.imported_at, error=''`,
			country, status, count, postcodeCount, time.Now().Unix())
		return
	}
	// Keep the previous count/date while running or after a failed re-import:
	// the old rows are still in place until a new import commits.
	db.Exec(`INSERT INTO places_import (country, status, error) VALUES (?, ?, ?)
		ON CONFLICT(country) DO UPDATE SET status=excluded.status, error=excluded.error`, country, status, errMsg)
}

// open returns a reader for name under baseURL — http(s) or a file://
// directory (offline installs).
func open(ctx context.Context, client *http.Client, baseURL, name string) (io.ReadCloser, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/" + name)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "file" {
		return os.Open(u.Path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return resp.Body, nil
}

// loadAdmin1 maps "DE.07" → "North Rhine-Westphalia".
func loadAdmin1(ctx context.Context, client *http.Client, baseURL string) (map[string]string, error) {
	rc, err := open(ctx, client, baseURL, "admin1CodesASCII.txt")
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(rc, maxDownload))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) >= 2 {
			m[f[0]] = f[1]
		}
	}
	return m, sc.Err()
}

// downloadCountryTxt downloads {country}.zip from baseURL and returns an
// open reader for the {country}.txt entry inside it — shared by the
// populated-place dump (importCountry) and the postal-code dump
// (importPostcodes, #1459), which are both one zip holding one same-named
// txt file, just under different directories and with different columns.
// The caller must Close the returned reader and call cleanup once done.
func downloadCountryTxt(ctx context.Context, client *http.Client, baseURL, country string) (r io.ReadCloser, cleanup func(), err error) {
	noop := func() {}
	rc, err := open(ctx, client, baseURL, country+".zip")
	if err != nil {
		return nil, noop, err
	}
	tmp, err := os.CreateTemp("", "geonames-*.zip")
	if err != nil {
		rc.Close()
		return nil, noop, err
	}
	cleanup = func() { tmp.Close(); os.Remove(tmp.Name()) }
	n, err := io.Copy(tmp, io.LimitReader(rc, maxDownload+1))
	rc.Close()
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	if n > maxDownload {
		cleanup()
		return nil, noop, fmt.Errorf("%s.zip larger than %d MB", country, maxDownload>>20)
	}
	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	var txt *zip.File
	for _, f := range zr.File {
		if f.Name == country+".txt" {
			txt = f
		}
	}
	if txt == nil {
		cleanup()
		return nil, noop, fmt.Errorf("%s.zip has no %s.txt", country, country)
	}
	r, err = txt.Open()
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	return r, cleanup, nil
}

// importCountry downloads {country}.zip and replaces the country's rows.
func importCountry(ctx context.Context, db *sql.DB, client *http.Client, baseURL, country string, admin1 map[string]string) (int, error) {
	r, cleanup, err := downloadCountryTxt(ctx, client, baseURL, country)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	defer r.Close()
	return load(db, country, r, admin1)
}

// importPostcodes downloads {country}.zip from zipBaseURL (GeoNames'
// postal-code export, #1459) and replaces the country's rows in postcodes.
// Not every country has one — the caller treats a failure as best-effort,
// not fatal to the place import. Unlike the populated-place dump, the
// postal-code dump's admin1 column is already a readable name (not a code
// needing admin1CodesASCII.txt), so no admin1 map is needed here.
func importPostcodes(ctx context.Context, db *sql.DB, client *http.Client, zipBaseURL, country string) (int, error) {
	r, cleanup, err := downloadCountryTxt(ctx, client, zipBaseURL, country)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	defer r.Close()
	return loadPostcodes(db, country, r)
}

// load replaces country's rows with the populated places in r (a GeoNames
// country dump: tab-separated, 19 columns) in one transaction.
func load(db *sql.DB, country string, r io.Reader, admin1 map[string]string) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM places WHERE country = ?", country); err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO places
		(geonameid, country, norm, name, admin1, lat, lng, population, feature_code)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	count := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20) // alternate-name lists make long lines
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 15 || f[6] != "P" || skipFeature[f[7]] || f[8] != country {
			continue
		}
		id, err1 := strconv.ParseInt(f[0], 10, 64)
		lat, err2 := strconv.ParseFloat(f[4], 64)
		lng, err3 := strconv.ParseFloat(f[5], 64)
		if err := errors.Join(err1, err2, err3); err != nil {
			continue
		}
		pop, _ := strconv.ParseInt(f[14], 10, 64)
		region := admin1[country+"."+f[10]]
		for _, v := range variants(f[1], f[2]) {
			if _, err := stmt.Exec(id, country, v, f[1], region, lat, lng, pop, f[7]); err != nil {
				return 0, err
			}
		}
		count++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, fmt.Errorf("no populated places found for %s", country)
	}
	return count, tx.Commit()
}

// companyPostcodeRe matches GeoNames postal-code dump names that are a
// company's own dedicated code (Großkundenpostleitzahl, e.g. "TeamBank AG")
// rather than a settlement, by legal-form suffix (#1476). Word-bounded so it
// doesn't misfire on a place name that merely contains these letters (e.g.
// "Hagen" has no standalone "AG").
var companyPostcodeRe = regexp.MustCompile(`(?i)\b(AG|SE|KG|mbH|GmbH|Bank|Versand|Holding|Werke|Gruppe)\b`)

// loadPostcodes replaces country's rows in postcodes with the GeoNames
// postal-code dump in r (#1459): tab-separated, 12 columns — country code,
// postal code, place name, admin name1, admin code1, admin name2, admin
// code2, admin name3, admin code3, latitude, longitude, accuracy.
//
// Some rows are a company's own dedicated postcode rather than a settlement
// (#1476) — GeoNames' DE dump mixes them in, e.g. "72105 TeamBank AG" next
// to "72108 Rottenburg am Neckar". A row is kept when its name also appears
// as a populated place already imported for this country (the common case —
// the place import normally runs first), or otherwise when it doesn't look
// like a company name: an unmatched name is more likely a small village
// missing from the place dump than something worth losing, so only an
// explicit company-suffix match is dropped.
func loadPostcodes(db *sql.DB, country string, r io.Reader) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM postcodes WHERE country = ?", country); err != nil {
		return 0, err
	}

	placeNames := map[string]bool{}
	if rows, err := tx.Query("SELECT DISTINCT name FROM places WHERE country = ?", country); err == nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				placeNames[Normalize(n)] = true
			}
		}
		rows.Close()
	}

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO postcodes (country, code, name, admin1, lat, lng) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	count := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 11 || f[0] != country {
			continue
		}
		lat, err1 := strconv.ParseFloat(f[9], 64)
		lng, err2 := strconv.ParseFloat(f[10], 64)
		if err := errors.Join(err1, err2); err != nil {
			continue
		}
		code := strings.ToUpper(strings.TrimSpace(f[1]))
		name := strings.TrimSpace(f[2])
		if code == "" || name == "" {
			continue
		}
		if !placeNames[Normalize(name)] && companyPostcodeRe.MatchString(name) {
			continue
		}
		if _, err := stmt.Exec(country, code, name, strings.TrimSpace(f[3]), lat, lng); err != nil {
			return 0, err
		}
		count++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, fmt.Errorf("no postcodes found for %s", country)
	}
	return count, tx.Commit()
}
