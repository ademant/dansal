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
CREATE TABLE IF NOT EXISTS places_import (
	country     TEXT    PRIMARY KEY,
	status      TEXT    NOT NULL DEFAULT '',
	place_count INTEGER NOT NULL DEFAULT 0,
	imported_at INTEGER NOT NULL DEFAULT 0,
	error       TEXT    NOT NULL DEFAULT ''
)`

// EnsureSchema creates the tables if missing.
func EnsureSchema(db *sql.DB) error {
	_, err := db.Exec(Schema)
	return err
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
