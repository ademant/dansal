package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const fdpFeedURL = "https://folkdance.page/index.json?styles=balfolk"

// ── folkdance.page feed types ─────────────────────────────────────────────────

type FDPFeed struct {
	Events []FDPEvent `json:"events"`
}

type FDPEvent struct {
	Name      string   `json:"name"`
	Links     []string `json:"links"`
	Start     string   `json:"start"`
	StartDate string   `json:"start_date"`
	City      string   `json:"city"`
	Country   string   `json:"country"`
	Bands     []string `json:"bands"`
	Price     string   `json:"price"`
	Cancelled bool     `json:"cancelled"`
}

// ── city alias DB helpers ─────────────────────────────────────────────────────

type CityAlias struct {
	ID        int
	Alias     string
	Canonical string
}

func listCityAliases(db *sql.DB) ([]CityAlias, error) {
	rows, err := db.Query("SELECT id, alias, canonical FROM city_aliases ORDER BY alias COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CityAlias
	for rows.Next() {
		var a CityAlias
		if err := rows.Scan(&a.ID, &a.Alias, &a.Canonical); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// aliasMap returns a lowercase-keyed alias→canonical map.
func aliasMap(aliases []CityAlias) map[string]string {
	m := make(map[string]string, len(aliases))
	for _, a := range aliases {
		m[strings.ToLower(a.Alias)] = a.Canonical
	}
	return m
}

func translateCity(city string, m map[string]string) string {
	if c, ok := m[strings.ToLower(city)]; ok {
		return c
	}
	return city
}

// ── normalisation ─────────────────────────────────────────────────────────────

// normalizeForMatch strips common European accents, lowercases s, and
// equates "&" with "and" so enrichment matching handles both spellings.
func normalizeForMatch(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.Join(strings.Fields(s), " ") // collapse extra spaces
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case 'à', 'á', 'â', 'ä', 'ã', 'å', 'ā', 'ă':
			b.WriteByte('a')
		case 'è', 'é', 'ê', 'ë', 'ě', 'ē':
			b.WriteByte('e')
		case 'ì', 'í', 'î', 'ï':
			b.WriteByte('i')
		case 'ò', 'ó', 'ô', 'ö', 'õ':
			b.WriteByte('o')
		case 'ù', 'ú', 'û', 'ü':
			b.WriteByte('u')
		case 'ñ', 'ń':
			b.WriteByte('n')
		case 'ç', 'č':
			b.WriteByte('c')
		case 'ß':
			b.WriteString("ss")
		case 'š':
			b.WriteByte('s')
		case 'ž':
			b.WriteByte('z')
		case 'ř':
			b.WriteByte('r')
		case 'ý', 'ÿ':
			b.WriteByte('y')
		default:
			if unicode.IsPrint(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// titleSimilarity returns a token-overlap score [0..1] between two event titles.
func titleSimilarity(a, b string) float64 {
	tokA := strings.Fields(normalizeForMatch(a))
	tokB := strings.Fields(normalizeForMatch(b))
	if len(tokA) == 0 || len(tokB) == 0 {
		return 0
	}
	setA := make(map[string]bool, len(tokA))
	for _, t := range tokA {
		setA[t] = true
	}
	shared := 0
	for _, t := range tokB {
		if setA[t] {
			shared++
		}
	}
	denom := len(tokA)
	if len(tokB) > denom {
		denom = len(tokB)
	}
	return float64(shared) / float64(denom)
}

// ── price parsing ─────────────────────────────────────────────────────────────

func parseFDPPrice(s string) *Pricing {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	switch strings.ToLower(s) {
	case "free":
		return &Pricing{Type: "free"}
	case "donation":
		return &Pricing{Type: "donation"}
	}
	currency := ""
	switch {
	case strings.ContainsRune(s, '€'):
		currency = "EUR"
	case strings.ContainsRune(s, '£'):
		currency = "GBP"
	case strings.ContainsRune(s, '$'):
		currency = "USD"
	default:
		return nil
	}
	// Strip everything except digits, dot, dash.
	var raw strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' {
			raw.WriteRune(r)
		}
	}
	parts := strings.SplitN(raw.String(), "-", 2)
	switch len(parts) {
	case 1:
		if v, err := strconv.ParseFloat(parts[0], 64); err == nil && v >= 0 {
			return &Pricing{Type: "single", Amount: v, Currency: currency}
		}
	case 2:
		lo, e1 := strconv.ParseFloat(parts[0], 64)
		hi, e2 := strconv.ParseFloat(parts[1], 64)
		if e1 == nil && e2 == nil && lo >= 0 && hi > 0 {
			return &Pricing{
				Type:     "multiple",
				Currency: currency,
				Prices:   []Price{{Label: "reduced", Amount: lo}, {Label: "normal", Amount: hi}},
			}
		}
	}
	return nil
}

// ── feed fetching ─────────────────────────────────────────────────────────────

func fetchFDPFeed(r *http.Request) ([]FDPEvent, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, fdpFeedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "dansal-enrich/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("folkdance.page returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var feed FDPFeed
	if err := json.Unmarshal(body, &feed); err != nil {
		return nil, err
	}
	return feed.Events, nil
}

// ── matching ──────────────────────────────────────────────────────────────────

// fdpStartUnix returns the Unix timestamp for an fd.p event, or 0 on failure.
func fdpStartUnix(ev FDPEvent) int64 {
	if ev.Start != "" {
		if t, err := time.Parse(time.RFC3339, ev.Start); err == nil {
			return t.Unix()
		}
	}
	if ev.StartDate != "" {
		if t, err := time.Parse("2006-01-02", ev.StartDate); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// eventStartUnix parses the ISO start_time from the dansal event list response.
func eventStartUnix(startTime string) int64 {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, startTime); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// ── template data ─────────────────────────────────────────────────────────────

type MusicianOption struct {
	Name       string
	SelectedID int // 0 = create new
}

type EnrichRow struct {
	FDPEvent        FDPEvent
	Event           Event
	StartDate       string // "2026-06-27" pre-formatted
	MatchType       string // "url" or "city+date"
	Musicians       []MusicianOption
	HasPricing      bool
	PricingStr      string
	ProposedPricing *Pricing
}

type AdminEnrichData struct {
	CityAliases  []CityAlias
	AllMusicians []Musician
	Rows         []EnrichRow
	Error        string
	FeedFetched  bool
}

// ── handlers ──────────────────────────────────────────────────────────────────

func adminEnrichPageHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		aliases, err := listCityAliases(db)
		if err != nil {
			log.Printf("admin enrich: could not load city aliases: %v", err)
		}
		renderTemplate(w, tmpls.adminEnrich, tmplData(r, cfg, i18n, "Enrich from folkdance.page", AdminEnrichData{
			CityAliases: aliases,
		}))
	}
}

func adminEnrichPreviewHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}

		aliases, err := listCityAliases(db)
		if err != nil {
			log.Printf("admin enrich: could not load city aliases: %v", err)
		}
		cityMap := aliasMap(aliases)
		musicians, err := client.GetMusicians(r.Context())
		if err != nil {
			log.Printf("admin enrich: could not load musicians: %v", err)
		}

		// Pre-build normalized musician name → ID map.
		normMusician := make(map[string]int, len(musicians))
		for _, m := range musicians {
			normMusician[normalizeForMatch(m.Bandname)] = m.ID
		}

		renderErr := func(msg string) {
			renderTemplate(w, tmpls.adminEnrich, tmplData(r, cfg, i18n, "Enrich from folkdance.page", AdminEnrichData{
				CityAliases:  aliases,
				AllMusicians: musicians,
				Error:        msg,
				FeedFetched:  true,
			}))
		}

		fdpEvents, err := fetchFDPFeed(r)
		if err != nil {
			renderErr("Failed to fetch feed: " + err.Error())
			return
		}

		// Fetch all published dansal events for matching.
		dansalEvents, err := client.GetEventsFiltered(r.Context(), urlValues("is_published", "true", "include_past", "true", "limit", "5000"))
		if err != nil {
			renderErr("Failed to fetch dansal events: " + err.Error())
			return
		}

		// Build lookup maps for fast matching.
		byURL := make(map[string]Event, len(dansalEvents))
		for _, ev := range dansalEvents {
			if ev.URL != "" {
				byURL[ev.URL] = ev
			}
		}

		var rows []EnrichRow
		seen := make(map[int]bool) // avoid duplicate dansal events in result

		for _, fdp := range fdpEvents {
			if len(fdp.Bands) == 0 && fdp.Price == "" {
				continue // nothing to enrich
			}
			var matched *Event
			matchType := ""

			// Tier 1: URL match.
			for _, link := range fdp.Links {
				if ev, ok := byURL[link]; ok {
					cp := ev
					matched = &cp
					matchType = "url"
					break
				}
			}

			// Tier 2: city + date match.
			if matched == nil {
				fdpStart := fdpStartUnix(fdp)
				if fdpStart == 0 {
					continue // can't match without a start time
				}
				canonical := translateCity(fdp.City, cityMap)
				normCity := normalizeForMatch(canonical)

				var candidates []Event
				for _, ev := range dansalEvents {
					if ev.Location == nil {
						continue
					}
					if normalizeForMatch(ev.Location.Town) != normCity {
						continue
					}
					evStart := eventStartUnix(ev.StartTime)
					if evStart == 0 {
						continue
					}
					diff := evStart - fdpStart
					if diff < 0 {
						diff = -diff
					}
					if diff <= fdpMatchWindow { // ±3 hours
						candidates = append(candidates, ev)
					}
				}
				if len(candidates) == 0 {
					continue
				}
				if len(candidates) == 1 {
					cp := candidates[0]
					matched = &cp
					matchType = "city+date"
				} else {
					// Multiple candidates: pick the one with highest title similarity.
					best := candidates[0]
					bestScore := titleSimilarity(fdp.Name, candidates[0].Title)
					for _, c := range candidates[1:] {
						if s := titleSimilarity(fdp.Name, c.Title); s > bestScore {
							bestScore = s
							best = c
						}
					}
					cp := best
					matched = &cp
					matchType = "city+date"
				}
			}

			if matched == nil || seen[matched.ID] {
				continue
			}
			seen[matched.ID] = true

			// Build musician options.
			var opts []MusicianOption
			for _, band := range fdp.Bands {
				id := normMusician[normalizeForMatch(band)]
				opts = append(opts, MusicianOption{Name: band, SelectedID: id})
			}

			// Pricing.
			proposed := parseFDPPrice(fdp.Price)
			hasPricing := matched.Pricing != nil

			startDate := ""
			if matched.StartTime != "" && len(matched.StartTime) >= 10 {
				startDate = matched.StartTime[:10]
			}

			rows = append(rows, EnrichRow{
				FDPEvent:        fdp,
				Event:           *matched,
				StartDate:       startDate,
				MatchType:       matchType,
				Musicians:       opts,
				HasPricing:      hasPricing,
				PricingStr:      fdp.Price,
				ProposedPricing: proposed,
			})
		}

		renderTemplate(w, tmpls.adminEnrich, tmplData(r, cfg, i18n, "Enrich from folkdance.page", AdminEnrichData{
			CityAliases:  aliases,
			AllMusicians: musicians,
			Rows:         rows,
			FeedFetched:  true,
		}))
	}
}

func adminEnrichApplyHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		ctx := r.Context()

		// Cache of newly created musicians (name → id) to avoid duplicates.
		created := make(map[string]int)

		for _, eventIDStr := range r.Form["include"] {
			eventID, err := strconv.Atoi(eventIDStr)
			if err != nil || eventID <= 0 {
				continue
			}

			var musicianIDs []int
			for _, val := range r.Form["musician_"+eventIDStr] {
				val = strings.TrimSpace(val)
				if val == "" {
					continue
				}
				if strings.HasPrefix(val, "new:") {
					name := strings.TrimPrefix(val, "new:")
					name = strings.TrimSpace(name)
					if name == "" {
						continue
					}
					if id, ok := created[name]; ok {
						musicianIDs = append(musicianIDs, id)
						continue
					}
					m, err := client.CreateMusician(ctx, Musician{Bandname: name}, token)
					if err != nil {
						log.Printf("enrich: create musician %q: %v", name, err)
						continue
					}
					created[name] = m.ID
					musicianIDs = append(musicianIDs, m.ID)
				} else {
					if id, err := strconv.Atoi(val); err == nil && id > 0 {
						musicianIDs = append(musicianIDs, id)
					}
				}
			}

			var pricing *Pricing
			if r.FormValue("apply_pricing_"+eventIDStr) == "1" {
				if priceStr := r.FormValue("fdp_price_" + eventIDStr); priceStr != "" {
					pricing = parseFDPPrice(priceStr)
				}
			}

			if len(musicianIDs) == 0 && pricing == nil {
				continue
			}

			req := EnrichEventReq{AddMusicianIDs: musicianIDs, Pricing: pricing}
			if err := client.EnrichEvent(ctx, eventID, req, token); err != nil {
				log.Printf("enrich event %d: %v", eventID, err)
			}
		}

		http.Redirect(w, r, "/admin/enrich?applied=1", http.StatusSeeOther)
	}
}

// adminEnrichAliasNewHandler adds a city alias to web.db.
func adminEnrichAliasNewHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		alias := strings.TrimSpace(r.FormValue("alias"))
		canonical := strings.TrimSpace(r.FormValue("canonical"))
		if alias == "" || canonical == "" {
			http.Redirect(w, r, "/admin/enrich", http.StatusSeeOther)
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO city_aliases(alias, canonical) VALUES(?,?)", alias, canonical); err != nil {
			log.Printf("could not insert city alias %q→%q: %v", alias, canonical, err)
		}
		http.Redirect(w, r, "/admin/enrich", http.StatusSeeOther)
	}
}

// adminEnrichAliasDeleteHandler removes a city alias from web.db.
func adminEnrichAliasDeleteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Redirect(w, r, "/admin/enrich", http.StatusSeeOther)
			return
		}
		if _, err := db.Exec("DELETE FROM city_aliases WHERE id=?", id); err != nil {
			log.Printf("could not delete city alias %d: %v", id, err)
		}
		http.Redirect(w, r, "/admin/enrich", http.StatusSeeOther)
	}
}

// urlValues builds a url.Values from alternating key/value strings.
func urlValues(pairs ...string) map[string][]string {
	m := make(map[string][]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = []string{pairs[i+1]}
	}
	return m
}
