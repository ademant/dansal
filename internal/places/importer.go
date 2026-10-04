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
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is GeoNames' per-country dump directory.
const DefaultBaseURL = "https://download.geonames.org/export/dump/"

// maxDownload caps a single downloaded file.
const maxDownload = 300 << 20

// skipFeature lists populated-place codes not worth suggesting: historical,
// abandoned, destroyed places and farms.
var skipFeature = map[string]bool{"PPLH": true, "PPLQ": true, "PPLW": true, "PPLCH": true, "PPLF": true}

// ImportStatus is one row of places_import.
type ImportStatus struct {
	Country    string
	Status     string // "running", "ok", "error"
	PlaceCount int
	ImportedAt time.Time
	Error      string
}

// Statuses returns the import state of every country that has one.
func Statuses(db *sql.DB) ([]ImportStatus, error) {
	rows, err := db.Query("SELECT country, status, place_count, imported_at, error FROM places_import ORDER BY country")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportStatus
	for rows.Next() {
		var s ImportStatus
		var at int64
		if err := rows.Scan(&s.Country, &s.Status, &s.PlaceCount, &at, &s.Error); err != nil {
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
// import (or every one, when force) is imported from baseURL. Runs are
// serialized; a failure is recorded per country and doesn't stop the others.
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
				db.Exec("DELETE FROM places_import WHERE country = ?", s.Country)
			}
		}
	}

	var todo []string
	for _, c := range countries {
		var status string
		db.QueryRow("SELECT status FROM places_import WHERE country = ?", c).Scan(&status)
		if force || status != "ok" {
			todo = append(todo, c)
		}
	}
	if len(todo) == 0 {
		return
	}
	admin1, err := loadAdmin1(ctx, client, baseURL)
	if err != nil {
		log.Printf("places: state names unavailable, importing without them: %v", err)
	}
	for _, c := range todo {
		setStatus(db, c, "running", 0, "")
		n, err := importCountry(ctx, db, client, baseURL, c, admin1)
		if err != nil {
			log.Printf("places: import %s: %v", c, err)
			setStatus(db, c, "error", 0, err.Error())
			continue
		}
		log.Printf("places: imported %s: %d places", c, n)
		setStatus(db, c, "ok", n, "")
	}
}

func setStatus(db *sql.DB, country, status string, count int, errMsg string) {
	if status == "ok" {
		db.Exec(`INSERT INTO places_import (country, status, place_count, imported_at, error) VALUES (?, ?, ?, ?, '')
			ON CONFLICT(country) DO UPDATE SET status=excluded.status, place_count=excluded.place_count,
			imported_at=excluded.imported_at, error=''`, country, status, count, time.Now().Unix())
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

// importCountry downloads {country}.zip and replaces the country's rows.
func importCountry(ctx context.Context, db *sql.DB, client *http.Client, baseURL, country string, admin1 map[string]string) (int, error) {
	rc, err := open(ctx, client, baseURL, country+".zip")
	if err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp("", "geonames-*.zip")
	if err != nil {
		rc.Close()
		return 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, io.LimitReader(rc, maxDownload+1))
	rc.Close()
	if err != nil {
		return 0, err
	}
	if n > maxDownload {
		return 0, fmt.Errorf("%s.zip larger than %d MB", country, maxDownload>>20)
	}
	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return 0, err
	}
	var txt *zip.File
	for _, f := range zr.File {
		if f.Name == country+".txt" {
			txt = f
		}
	}
	if txt == nil {
		return 0, fmt.Errorf("%s.zip has no %s.txt", country, country)
	}
	r, err := txt.Open()
	if err != nil {
		return 0, err
	}
	defer r.Close()
	return load(db, country, r, admin1)
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
