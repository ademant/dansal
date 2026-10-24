// Package orgmatch finds existing organisations that a new-organisation
// registration probably duplicates (#1648). It is pure: no database, no HTTP.
//
// Signals, from strongest to weakest:
//   - website host and path equal (scheme, "www." and a trailing slash ignored).
//     Strong on its own. Path matters because several WordPress instances can
//     share one host, each on its own path.
//   - website host equal, path different. Weak; offered only with a name signal.
//   - normalised name similar (small edit distance), or the ActivityPub name
//     equal. Weak; offered only with a host signal. A name alone is not offered:
//     common words make "Tanzhaus Nord" and "Tanzhaus Süd" look alike.
//
// Hosts of shared platforms (facebook.com, linktr.ee, gmail.com, …) say nothing
// about who runs the organisation, so they never count as a host signal.
package orgmatch

import (
	"net/url"
	"strings"
	"unicode"
)

// Org is the part of an existing organisation the matcher needs.
type Org struct {
	ID        int
	Name      string
	ActorName string
	Website   string
}

// Request is what the applicant typed for the new organisation.
type Request struct {
	Name      string
	ActorName string
	Website   string
}

// Offer is an existing organisation worth offering instead of a new one.
type Offer struct {
	Org    Org
	Strong bool // host and path equal: offer on this alone
}

// maxNameDistance is the edit distance up to which two normalised names count
// as similar, and minNameLen the shorter name length below which the name
// signal is ignored (short names are too easily alike).
const (
	maxNameDistance = 2
	minNameLen      = 8
)

// platformSuffixes are hosts shared by many unrelated people or organisations.
var platformSuffixes = []string{
	"facebook.com", "fb.com", "instagram.com", "linktr.ee", "x.com", "twitter.com",
	"youtube.com", "youtu.be", "tiktok.com", "linkedin.com", "bsky.app",
	"wordpress.com", "wixsite.com", "jimdo.com", "jimdosite.com", "sites.google.com",
	"github.io", "mastodon.social", "eventbrite.com", "eventbrite.de", "meetup.com",
	"gmail.com", "gmx.de", "gmx.net", "web.de", "t-online.de", "yahoo.com",
	"hotmail.com", "outlook.com", "icloud.com",
}

// legalTokens are removed from names: legal form and filler words.
var legalTokens = map[string]bool{
	"ev": true, "e": true, "v": true, "verein": true, "gmbh": true, "ggmbh": true,
	"ug": true, "gbr": true, "eg": true, "ag": true, "asbl": true, "association": true,
}

// diacritics maps accented letters to their base letters.
var diacritics = map[rune]string{
	'ä': "a", 'ö': "o", 'ü': "u", 'ß': "ss", 'à': "a", 'á': "a", 'â': "a", 'å': "a",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ì': "i", 'í': "i", 'î': "i", 'ï': "i",
	'ò': "o", 'ó': "o", 'ô': "o", 'ø': "o", 'ù': "u", 'ú': "u", 'û': "u", 'ñ': "n",
	'ç': "c", 'ł': "l", 'ś': "s", 'ć': "c", 'ž': "z", 'š': "s", 'ř': "r", 'ě': "e",
	'ů': "u", 'ą': "a", 'ę': "e", 'ő': "o", 'ű': "u", 'ğ': "g", 'ı': "i", 'ş': "s",
}

// NormalizeName reduces an organisation name to comparable letters: lower case,
// accents removed, legal-form words dropped, no spaces or punctuation.
func NormalizeName(s string) string {
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tok := cur.String()
			if !legalTokens[tok] {
				tokens = append(tokens, tok)
			}
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		if rep, ok := diacritics[r]; ok {
			cur.WriteString(rep)
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return strings.Join(tokens, "")
}

// NameSimilar reports whether two names are close enough to count as the same
// organisation: after normalisation, within maxNameDistance edits, and the
// shorter one at least minNameLen long.
func NameSimilar(a, b string) bool {
	na, nb := NormalizeName(a), NormalizeName(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	short := len([]rune(na))
	if l := len([]rune(nb)); l < short {
		short = l
	}
	if short < minNameLen {
		return false
	}
	return levenshtein(na, nb) <= maxNameDistance
}

// SiteKey returns the host and path of a website for comparison. ok is false
// when the value is empty, unparseable, or on a shared platform.
func SiteKey(raw string) (host, path string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	host = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == "" || isPlatform(host) {
		return "", "", false
	}
	path = strings.ToLower(strings.TrimRight(u.Path, "/"))
	return host, path, true
}

func isPlatform(host string) bool {
	for _, p := range platformSuffixes {
		if host == p || strings.HasSuffix(host, "."+p) {
			return true
		}
	}
	return false
}

// Find returns the organisations worth offering instead of creating a new one,
// strong matches first, in the order given within each group.
func Find(req Request, orgs []Org) []Offer {
	reqHost, reqPath, reqSite := SiteKey(req.Website)
	var strong, weak []Offer
	for _, o := range orgs {
		orgHost, orgPath, orgSite := SiteKey(o.Website)
		hostEqual := reqSite && orgSite && reqHost == orgHost
		if hostEqual && reqPath == orgPath {
			strong = append(strong, Offer{Org: o, Strong: true})
			continue
		}
		nameMatch := NameSimilar(req.Name, o.Name) ||
			(req.ActorName != "" && strings.EqualFold(req.ActorName, o.ActorName))
		if hostEqual && nameMatch {
			weak = append(weak, Offer{Org: o})
		}
	}
	return append(strong, weak...)
}

// levenshtein is the edit distance between two strings, in runes.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
