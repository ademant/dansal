package places

import (
	"database/sql"
	"sort"
	"strings"
	"unicode/utf8"
)

// Result is one place suggestion.
type Result struct {
	Name    string  `json:"name"`
	Region  string  `json:"region,omitempty"`
	Country string  `json:"country"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`

	geonameid  int64
	population int64
	feature    string
	dist       int
}

// MinQueryLen is the shortest query Search answers.
const MinQueryLen = 3

// typoCandidateLimit caps the rows read for the typo pass (all places of the
// configured countries sharing the query's first letter).
const typoCandidateLimit = 50000

// maxTypos is the edit distance tolerated for a query of n letters.
func maxTypos(n int) int {
	switch {
	case n < 4:
		return 0
	case n < 8:
		return 1
	default:
		return 2
	}
}

// Search returns up to limit places of the given countries whose name starts
// with q, or — when there are fewer exact hits — starts with something within
// a small edit distance of q ("hamurg" → Hamburg). Ranked by distance, then
// population, then place type. All work happens in SQLite plus a per-request
// pass over the candidates; nothing is cached in memory.
func Search(db *sql.DB, countries []string, q string, limit int) ([]Result, error) {
	nq := Normalize(q)
	if utf8.RuneCountInString(nq) < MinQueryLen || len(countries) == 0 || limit <= 0 {
		return nil, nil
	}
	best := map[int64]*Result{}

	// Exact prefixes: an index range scan on (country, norm), largest places
	// first — a short prefix like "neu" matches thousands.
	if err := scanRange(db, countries, nq, "population DESC", 500, func(r *Result, n string) {
		r.dist = 0
		keepBest(best, r)
	}); err != nil {
		return nil, err
	}

	if d := maxTypos(utf8.RuneCountInString(nq)); d > 0 && len(best) < limit {
		first, _ := utf8.DecodeRuneInString(nq)
		qr := []rune(nq)
		if err := scanRange(db, countries, string(first), "", typoCandidateLimit, func(r *Result, n string) {
			if dist := prefixDistance(qr, []rune(n), d); dist <= d {
				r.dist = dist
				keepBest(best, r)
			}
		}); err != nil {
			return nil, err
		}
	}

	out := make([]Result, 0, len(best))
	for _, r := range best {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.dist != b.dist {
			return a.dist < b.dist
		}
		if a.population != b.population {
			return a.population > b.population
		}
		if ra, rb := featureRank(a.feature), featureRank(b.feature); ra != rb {
			return ra < rb
		}
		return a.Name < b.Name
	})
	// Same name in the same state can't be told apart in a suggestion list
	// (GeoNames has e.g. several "Eggenberg, Bavaria"); keep the best-ranked.
	seen := map[string]bool{}
	deduped := out[:0]
	for _, r := range out {
		if k := r.Name + "\x00" + r.Region + "\x00" + r.Country; !seen[k] {
			seen[k] = true
			deduped = append(deduped, r)
		}
	}
	out = deduped
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SearchPostcodes returns up to limit postcodes of the given countries whose
// code starts with q (#1459) — a plain prefix scan, no typo tolerance:
// postcodes are short enough that a near-enough match would be noise, not
// help, unlike a misspelled town name. Result.Name is "<code> <place>" (e.g.
// "72108 Rottenburg am Neckar") so the existing geocodeResult rendering on
// the /search page needs no changes to show it.
func SearchPostcodes(db *sql.DB, countries []string, q string, limit int) ([]Result, error) {
	code := strings.ToUpper(strings.TrimSpace(q))
	if code == "" || len(countries) == 0 || limit <= 0 {
		return nil, nil
	}
	args := make([]any, 0, len(countries)+3)
	for _, c := range countries {
		args = append(args, c)
	}
	args = append(args, code, code+"\U0010FFFF", limit)
	rows, err := db.Query(`SELECT code, name, admin1, country, lat, lng
		FROM postcodes WHERE country IN (?`+strings.Repeat(",?", len(countries)-1)+`)
		AND code >= ? AND code < ? ORDER BY code LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []Result
	for rows.Next() {
		var code, name, admin1, country string
		var lat, lng float64
		if err := rows.Scan(&code, &name, &admin1, &country, &lat, &lng); err != nil {
			return nil, err
		}
		label := code + " " + name
		if seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, Result{Name: label, Region: admin1, Country: country, Lat: lat, Lng: lng})
	}
	return out, rows.Err()
}

func keepBest(best map[int64]*Result, r *Result) {
	if old, ok := best[r.geonameid]; !ok || r.dist < old.dist {
		best[r.geonameid] = r
	}
}

// scanRange reads the places whose norm starts with prefix.
// orderBy is a fixed literal from Search, never request input.
func scanRange(db *sql.DB, countries []string, prefix, orderBy string, limit int, fn func(*Result, string)) error {
	args := make([]any, 0, len(countries)+3)
	for _, c := range countries {
		args = append(args, c)
	}
	args = append(args, prefix, prefix+"\U0010FFFF", limit)
	rows, err := db.Query(`SELECT geonameid, norm, name, admin1, country, lat, lng, population, feature_code
		FROM places WHERE country IN (?`+strings.Repeat(",?", len(countries)-1)+`)
		AND norm >= ? AND norm < ?`+orderClause(orderBy)+` LIMIT ?`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r Result
		var n string
		if err := rows.Scan(&r.geonameid, &n, &r.Name, &r.Region, &r.Country, &r.Lat, &r.Lng, &r.population, &r.feature); err != nil {
			return err
		}
		fn(&r, n)
	}
	return rows.Err()
}

// featureRank orders GeoNames populated-place codes from capital down to
// sections and localities; used only to break population ties (many small
// places have population 0 in GeoNames).
func featureRank(code string) int {
	switch code {
	case "PPLC":
		return 0
	case "PPLA":
		return 1
	case "PPLA2":
		return 2
	case "PPLA3":
		return 3
	case "PPLA4":
		return 4
	case "PPL", "PPLG", "PPLS":
		return 5
	case "PPLX":
		return 6
	default:
		return 7
	}
}

// prefixDistance is the smallest optimal-string-alignment distance (edits
// plus adjacent swaps) between q and any prefix of s, or maxd+1 when it
// exceeds maxd. Only prefixes within maxd of q's length can qualify, so s is
// cut there.
func prefixDistance(q, s []rune, maxd int) int {
	n := len(q)
	if m := n + maxd; len(s) > m {
		s = s[:m]
	}
	m := len(s)
	prev2 := make([]int, m+1)
	prev := make([]int, m+1)
	cur := make([]int, m+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= n; i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= m; j++ {
			cost := 1
			if q[i-1] == s[j-1] {
				cost = 0
			}
			v := min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && q[i-1] == s[j-2] && q[i-2] == s[j-1] {
				v = min(v, prev2[j-2]+1)
			}
			cur[j] = v
			rowMin = min(rowMin, v)
		}
		if rowMin > maxd {
			return maxd + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	best := maxd + 1
	for j := max(0, n-maxd); j <= m; j++ {
		best = min(best, prev[j])
	}
	return best
}

func orderClause(orderBy string) string {
	if orderBy == "" {
		return ""
	}
	return " ORDER BY " + orderBy
}
