// Package places holds the local place-name table behind the /search city
// type-ahead (#1429): populated places imported from GeoNames per configured
// country into web.db, and searched there on demand — prefix match plus
// on-the-fly typo tolerance, no in-memory index. dansal-webmin imports
// (Sync), dansal-web searches (Search).
package places

import (
	"database/sql"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Schema creates the place tables. Both binaries run it: dansal-web as a
// web.db migration, dansal-webmin before an import (it may start first).
//
// postcodes (#1459) is a separate table from places rather than a marker row
// there: GeoNames' postal-code dump is a different shape (no geonameid,
// population, or feature_code — just code/name/admin1/lat/lng), and keeping
// it separate means places.Search's existing ranking/typo logic can't be
// affected by it.
const Schema = `CREATE TABLE IF NOT EXISTS places (
	geonameid    INTEGER NOT NULL,
	country      TEXT    NOT NULL,
	norm         TEXT    NOT NULL,
	name         TEXT    NOT NULL,
	admin1       TEXT    NOT NULL DEFAULT '',
	lat          REAL    NOT NULL,
	lng          REAL    NOT NULL,
	population   INTEGER NOT NULL DEFAULT 0,
	feature_code TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (country, norm, geonameid)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS postcodes (
	country TEXT NOT NULL,
	code    TEXT NOT NULL,
	name    TEXT NOT NULL,
	admin1  TEXT NOT NULL DEFAULT '',
	lat     REAL NOT NULL,
	lng     REAL NOT NULL,
	PRIMARY KEY (country, code, name)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS places_import (
	country        TEXT    PRIMARY KEY,
	status         TEXT    NOT NULL DEFAULT '',
	place_count    INTEGER NOT NULL DEFAULT 0,
	postcode_count INTEGER NOT NULL DEFAULT 0,
	imported_at    INTEGER NOT NULL DEFAULT 0,
	error          TEXT    NOT NULL DEFAULT ''
)`

// EnsureSchema creates the tables if missing.
func EnsureSchema(db *sql.DB) error {
	if _, err := db.Exec(Schema); err != nil {
		return err
	}
	// Safety net (#1459): places_import predates postcode_count (#1429) — on
	// a DB created before this column existed, CREATE TABLE IF NOT EXISTS
	// above is a no-op, so add it explicitly. Self-healing and idempotent:
	// a no-op once the column is there.
	var n int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('places_import') WHERE name='postcode_count'").Scan(&n)
	if n == 0 {
		if _, err := db.Exec("ALTER TABLE places_import ADD COLUMN postcode_count INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	return nil
}

var countryRe = regexp.MustCompile(`^[A-Z]{2}$`)

// ParseCountries reads a "DE, at ch" style list into upper-case ISO 3166-1
// alpha-2 codes, dropping invalid entries and duplicates (order kept).
func ParseCountries(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToUpper(s), func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	}) {
		if countryRe.MatchString(f) && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// postcodeShapeRe matches a query shaped like a postal code: 3-10 characters
// of letters/digits/space/hyphen. Combined with "contains a digit" in
// IsPostcodeQuery, this covers DE/AT/CH's all-digit codes as well as mixed
// alphanumeric shapes (NL "1234 AB", UK "SW1A 1AA") without matching a plain
// town name (letters only never has a digit).
var postcodeShapeRe = regexp.MustCompile(`^[A-Za-z0-9 -]{3,10}$`)

// IsPostcodeQuery reports whether q looks like a postal code rather than a
// place name (#1459) — used to route /search's city type-ahead (and the
// Nominatim Enter-key fallback) to postcode matching instead of name search.
func IsPostcodeQuery(q string) bool {
	q = strings.TrimSpace(q)
	if !postcodeShapeRe.MatchString(q) {
		return false
	}
	for _, r := range q {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

var stripMarks = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// special folds letters that NFD doesn't decompose into base + mark.
var special = strings.NewReplacer("ß", "ss", "ẞ", "ss", "æ", "ae", "Æ", "ae", "œ", "oe", "Œ", "oe",
	"ø", "o", "Ø", "o", "ł", "l", "Ł", "l", "đ", "d", "Đ", "d", "þ", "th", "ı", "i")

// expand spells German umlauts the way people type them without the keys.
var expand = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "ae", "Ö", "oe", "Ü", "ue")

// Normalize is the search form of a name or query: lower case, diacritics
// removed (ü→u), punctuation and hyphens as single spaces.
func Normalize(s string) string {
	s = special.Replace(s)
	if t, _, err := transform.String(stripMarks, s); err == nil {
		s = t
	}
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		} else {
			space = true
		}
	}
	return b.String()
}

// variants are the normalized forms stored for one place, so a query in any
// of the common spellings hits it: folded (munchen), umlauts expanded
// (muenchen) and GeoNames' own ASCII name.
func variants(name, ascii string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range []string{Normalize(name), Normalize(expand.Replace(name)), Normalize(ascii)} {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
