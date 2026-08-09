// Package strutil holds small string and timestamp helpers shared across the
// dansal binaries (API, web, webmin). Each helper was copy-pasted between
// packages and had started to drift before being extracted (#1035).
package strutil

import (
	"regexp"
	"strings"
	"time"
)

// TownSlug converts a city name to a URL-safe slug with common European
// character transliteration (Köln → koeln, München → munchen, etc.).
func TownSlug(town string) string {
	s := townReplacer.Replace(strings.ToLower(town))
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
		} else if !prevHyphen {
			b.WriteRune('-')
			prevHyphen = true
		}
	}
	return multiHyphen.ReplaceAllString(strings.Trim(b.String(), "-"), "-")
}

var townReplacer = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	"Ä", "ae", "Ö", "oe", "Ü", "ue",
	"à", "a", "á", "a", "â", "a", "ã", "a", "å", "a", "æ", "ae",
	"è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ø", "o",
	"ù", "u", "ú", "u", "û", "u",
	"ý", "y",
	"ç", "c", "ć", "c", "č", "c",
	"ñ", "n", "ń", "n",
	"ž", "z", "ź", "z", "ż", "z",
	"š", "s", "ś", "s",
	"ł", "l", "ľ", "l",
	"ď", "d", "đ", "d",
	"ť", "t",
	"ř", "r",
)

var multiHyphen = regexp.MustCompile(`-{2,}`)

// TimeLayouts lists every timestamp layout accepted across the codebase, in
// priority order. It is the union of the web and API layout sets, so parsing
// never diverges between binaries (#1035).
var TimeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// ParseTime parses s against TimeLayouts. Naive layouts carry no zone and
// parse as UTC. Returns ok=false when no layout matches.
func ParseTime(s string) (time.Time, bool) {
	for _, layout := range TimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
