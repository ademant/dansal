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
// character transliteration (Köln → koeln, München → munchen, etc.). Kept as
// its own named entry point (rather than just calling Slugify directly)
// because /city/{slug} routes must match exactly between dansal and the web
// frontend — the name documents that shared-contract purpose, even though
// the algorithm itself is fully generic.
func TownSlug(town string) string {
	return Slugify(town)
}

// Slugify converts arbitrary text to a URL-safe slug: lowercase, common
// European character transliteration (ä→ae, ö→oe, é→e, …), non-alphanumerics
// collapsed to single hyphens, trimmed. The general-purpose entry point —
// TownSlug is a thin wrapper kept for its own documented contract (see
// above); any other caller needing a slug should use this directly rather
// than reimplementing the same shape without transliteration (#1250, the
// second time this exact drift happened after #1035 first extracted it).
func Slugify(s string) string {
	t := townReplacer.Replace(strings.ToLower(s))
	var b strings.Builder
	prevHyphen := false
	for _, r := range t {
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
