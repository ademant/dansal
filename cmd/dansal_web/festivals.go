package main

import (
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// FestivalsData is the template payload for GET /festivals — a year-scoped
// overview of festival-tagged events, built as a planning tool (#1144): the
// map/calendar/list all share one dataset so an organizer scoping a new
// festival date can see the whole year's landscape at a glance, the same way
// the homepage's map/week/list share one dataset.
type FestivalsData struct {
	Year     int
	PrevYear int
	NextYear int
	// YearEvents are festivals whose start falls in Year, chronological.
	YearEvents []Event
	// FoldedEvents are festivals with no edition in Year: for each location
	// with no edition this year, its most recent past edition is shown
	// instead so the venue doesn't just disappear from the year view
	// (ported from wp-dansal's group_festivals(), see #1144).
	FoldedEvents []Event
	// MapEvents is YearEvents+FoldedEvents, fed to the existing eventsGeoJSON
	// template func so the map reuses the same marker/popup code as the
	// homepage and /search.
	MapEvents []Event
	OrgMap    map[int]Organization
	TagMap    map[string]Tag
}

// groupFoldedFestivals splits all festival-tagged events into those starting
// in the selected year and, for every location with no edition that year, a
// single folded fallback entry: its most recent edition regardless of year.
// Mirrors wp-dansal's group_festivals() (includes/class-wpd-frontend.php),
// grouped by location only for v1 (see #1144 — style-bucket grouping can be
// added later if a venue ever hosts distinct festival series).
func groupFoldedFestivals(all []Event, year int) (yearEvents, folded []Event) {
	type locGroup struct {
		yearEvents  []Event
		latest      *Event // most recent edition across all years
		latestStart time.Time
	}
	groups := map[int]*locGroup{}
	var order []int

	for i := range all {
		e := all[i]
		if e.LocationID == nil {
			continue
		}
		start, err := time.Parse(time.RFC3339, e.StartTime)
		if err != nil {
			continue
		}
		loc := *e.LocationID
		g, ok := groups[loc]
		if !ok {
			g = &locGroup{}
			groups[loc] = g
			order = append(order, loc)
		}
		if start.UTC().Year() == year {
			g.yearEvents = append(g.yearEvents, e)
		}
		if g.latest == nil || start.After(g.latestStart) {
			cp := e
			g.latest = &cp
			g.latestStart = start
		}
	}

	for _, loc := range order {
		g := groups[loc]
		if len(g.yearEvents) > 0 {
			yearEvents = append(yearEvents, g.yearEvents...)
			continue
		}
		if g.latest != nil {
			folded = append(folded, *g.latest)
		}
	}

	sort.Slice(yearEvents, func(i, j int) bool { return yearEvents[i].StartTime < yearEvents[j].StartTime })
	sort.Slice(folded, func(i, j int) bool { return folded[i].StartTime > folded[j].StartTime })
	return yearEvents, folded
}

// festivalsPageHandler serves GET /festivals?year=YYYY — a year-scoped
// festival overview (map, 12-month calendar, list) — see #1144.
func festivalsPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year := time.Now().UTC().Year()
		if v := r.URL.Query().Get("year"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 1970 && n <= 2100 {
				year = n
			}
		}

		// Fetch the whole instance's festival history (bounded by
		// apiListLimit like other whole-table lists), not just the selected
		// year, so groupFoldedFestivals can find each venue's latest edition
		// even when it falls outside Year.
		params := url.Values{}
		params.Set("tag", "festival")
		params.Set("is_published", "true")
		params.Set("include_past", "true")
		params.Set("limit", strconv.Itoa(apiListLimit))

		var all []Event
		var orgs []Organization
		var tagMap map[string]Tag
		err := fetchParallel(
			func() error {
				var err error
				all, _, err = client.GetEventsFilteredWithTotal(r.Context(), params)
				return err
			},
			func() error {
				var err error
				orgs, err = client.GetOrganizations(r.Context())
				if err != nil {
					log.Printf("festivals: could not load organizations: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				tagMap, err = client.GetTagMap(r.Context())
				if err != nil {
					log.Printf("festivals: could not load tag map: %v", err)
				}
				return nil
			},
		)
		if err != nil {
			logHTTPError(w, r, "could not load festivals", http.StatusBadGateway)
			return
		}

		yearEvents, folded := groupFoldedFestivals(all, year)
		mapEvents := make([]Event, 0, len(yearEvents)+len(folded))
		mapEvents = append(mapEvents, yearEvents...)
		mapEvents = append(mapEvents, folded...)

		title := i18n.T(r, "festivals_title")
		renderTemplate(w, tmpls.festivals, tmplData(r, cfg, i18n, title, FestivalsData{
			Year:         year,
			PrevYear:     year - 1,
			NextYear:     year + 1,
			YearEvents:   yearEvents,
			FoldedEvents: folded,
			MapEvents:    mapEvents,
			OrgMap:       orgMapByID(orgs),
			TagMap:       tagMap,
		}))
	}
}
