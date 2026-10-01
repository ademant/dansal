package main

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// #1412: batch musician/instructor lookup for the public event-suggest form.
// The form takes a comma-separated list per field; on blur it sends every name
// in one request and gets back, per name, whether it is already known, has
// similar stored names (shown in a "did you mean" dialog), or is new.

const (
	peopleSearchMaxNames   = 20
	peopleSearchMaxNameLen = 200
	peopleSearchMaxCands   = 5
)

// PeopleCandidate is one stored musician/instructor a typed name may refer to.
type PeopleCandidate struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "musician" | "instructor"
}

// PeopleResult is the lookup outcome for one typed name.
type PeopleResult struct {
	Name       string            `json:"name"`
	Status     string            `json:"status"` // "known" | "similar" | "new"
	Match      string            `json:"match,omitempty"`
	Candidates []PeopleCandidate `json:"candidates,omitempty"`
}

// peopleEntry is a stored name prepared for matching. A musician contributes
// its bandname and, when set, its short name as a second key.
type peopleEntry struct {
	name string // display name returned to the client
	kind string
	keys []string // normalized match keys
}

// splitPeopleList splits a free-text list of names on commas, semicolons and
// newlines. Not on "&" / "und" — band names contain those ("Duo X & Y").
// Duplicates (case-insensitive) are dropped, order is preserved.
func splitPeopleList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[strings.ToLower(p)] {
			continue
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	return out
}

// peopleFromForm collects the musician/instructor names of a suggest-form
// submission: the comma-separated text field (#1412) plus any legacy
// one-name-per-value fields from a form rendered before that change.
func peopleFromForm(r *http.Request, field string) []string {
	names := trimmedNonEmpty(r.Form[field])
	for _, n := range splitPeopleList(r.Form.Get(field + "_text")) {
		if !slices.ContainsFunc(names, func(x string) bool { return strings.EqualFold(x, n) }) {
			names = append(names, n)
		}
	}
	return names
}

// peopleTitleTokens are honorifics dropped before matching ("Dr. Roland Vogel").
var peopleTitleTokens = map[string]bool{"dr": true, "prof": true, "dipl": true, "mag": true}

// normalizePersonName lower-cases, strips diacritics, drops a leading role
// prefix ("Chant : Judith Laux"), punctuation and title tokens, and returns
// the remaining words.
func normalizePersonName(s string) []string {
	if i := strings.LastIndex(s, ":"); i >= 0 && strings.TrimSpace(s[i+1:]) != "" {
		s = s[i+1:]
	}
	words := foldWords(s)
	out := words[:0]
	for _, w := range words {
		if !peopleTitleTokens[w] {
			out = append(out, w)
		}
	}
	return out
}

// foldWords lower-cases s, strips diacritics (é → e, ß → ss) and splits it
// into its letter/digit words — the shared base of the people (#1412) and
// venue (#1414) lookups.
func foldWords(s string) []string {
	s = strings.ReplaceAll(strings.ToLower(s), "ß", "ss")
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// combining mark left over from NFD: drop it (é → e)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// levenshtein returns the rune-level edit distance between a and b.
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

// wordsAlike treats two words as the same if equal, or — for longer words —
// one typo apart.
func wordsAlike(a, b string) bool {
	if a == b {
		return true
	}
	return len([]rune(a)) >= 5 && len([]rune(b)) >= 5 && levenshtein(a, b) <= 1
}

// personSimilarity scores how likely typed refers to stored (both normalized
// and space-joined). 0 = identical, higher = weaker; ok is false when they are
// not similar at all.
func personSimilarity(typed, stored string) (score int, ok bool) {
	if typed == "" || stored == "" {
		return 0, false
	}
	if typed == stored {
		return 0, true
	}
	// Whole-string typo tolerance, scaled to length (max 3 edits).
	if d := levenshtein(typed, stored); d <= min(3, max(1, len([]rune(stored))/6)) {
		return d, true
	}
	// Word containment: every word of the shorter name appears (allowing a
	// typo) in the longer one — "Vogel" ~ "Roland Vogel", "Bow" ~ "Duo Bow".
	tw, sw := strings.Fields(typed), strings.Fields(stored)
	short, long := tw, sw
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(strings.Join(short, "")) < 3 {
		return 0, false
	}
	for _, w := range short {
		if !slices.ContainsFunc(long, func(l string) bool { return wordsAlike(w, l) }) {
			return 0, false
		}
	}
	return 4 + len(long) - len(short), true
}

// matchPeople resolves each typed name against the stored entries of the
// requested kind (other-kind entries are offered as candidates only, so a name
// typed into the wrong field is still found).
func matchPeople(names []string, kind string, entries []peopleEntry) []PeopleResult {
	results := make([]PeopleResult, 0, len(names))
	for _, name := range names {
		typed := strings.Join(normalizePersonName(name), " ")
		res := PeopleResult{Name: name, Status: "new"}
		type scored struct {
			c     PeopleCandidate
			score int
		}
		var cands []scored
		for _, e := range entries {
			best, found := 0, false
			for _, k := range e.keys {
				if s, ok := personSimilarity(typed, k); ok && (!found || s < best) {
					best, found = s, true
				}
			}
			if !found {
				continue
			}
			if best == 0 && e.kind == kind {
				res.Status, res.Match, cands = "known", e.name, nil
				break
			}
			cands = append(cands, scored{PeopleCandidate{Name: e.name, Kind: e.kind}, best})
		}
		if res.Status != "known" && len(cands) > 0 {
			slices.SortStableFunc(cands, func(a, b scored) int {
				return cmp.Or(cmp.Compare(a.score, b.score), strings.Compare(a.c.Name, b.c.Name))
			})
			res.Status = "similar"
			// Identical name+kind pairs (duplicate DB rows awaiting a merge)
			// would otherwise show up as several indistinguishable options.
			for _, c := range cands {
				if len(res.Candidates) == peopleSearchMaxCands {
					break
				}
				if !slices.Contains(res.Candidates, c.c) {
					res.Candidates = append(res.Candidates, c.c)
				}
			}
		}
		results = append(results, res)
	}
	return results
}

func peopleEntriesFrom(musicians []Musician, instructors []Instructor) []peopleEntry {
	entries := make([]peopleEntry, 0, len(musicians)+len(instructors))
	for _, m := range musicians {
		if m.Bandname == "" {
			continue
		}
		e := peopleEntry{name: m.Bandname, kind: "musician", keys: []string{strings.Join(normalizePersonName(m.Bandname), " ")}}
		if m.ShortName != "" {
			e.keys = append(e.keys, strings.Join(normalizePersonName(m.ShortName), " "))
		}
		entries = append(entries, e)
	}
	for _, in := range instructors {
		if in.Name == "" {
			continue
		}
		entries = append(entries, peopleEntry{name: in.Name, kind: "instructor", keys: []string{strings.Join(normalizePersonName(in.Name), " ")}})
	}
	return entries
}

// peopleSearchHandler serves GET /search/people?kind=musician|instructor&name=…&name=…
// for the event-suggest form (#1412).
func peopleSearchHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := r.URL.Query().Get("kind")
		if kind != "musician" && kind != "instructor" {
			writeJSONError(w, r, http.StatusBadRequest, "kind must be musician or instructor")
			return
		}
		var names []string
		for _, n := range r.URL.Query()["name"] {
			if n = strings.TrimSpace(n); n != "" && len(n) <= peopleSearchMaxNameLen {
				names = append(names, n)
			}
		}
		if len(names) == 0 || len(names) > peopleSearchMaxNames {
			writeJSONError(w, r, http.StatusBadRequest, "1–20 name parameters required")
			return
		}
		musicians, err := client.GetMusicians(r.Context())
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, "could not load musicians")
			return
		}
		instructors, err := client.GetInstructors(r.Context())
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, "could not load instructors")
			return
		}
		writeJSONResponse(w, http.StatusOK, matchPeople(names, kind, peopleEntriesFrom(musicians, instructors)))
	}
}
