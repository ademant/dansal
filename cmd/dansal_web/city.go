package main

// City hub pages (#965): /cities (directory) and /city/{slug} (hub page with
// map + event list, "show past events" on demand).

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/ademant/dansal/internal/strutil"
)

// ── Slug helpers ──────────────────────────────────────────────────────────────

// townSlug is shared with the API (strutil.TownSlug) so /city/{slug} routes
// match the slugs the API serves in its /api/v1/locations/cities list (#1035).
func townSlug(town string) string {
	return strutil.TownSlug(town)
}

// ── Template data ─────────────────────────────────────────────────────────────

type CitiesData struct {
	Cities  []City
	MapJSON template.JS // compact JSON for map markers, colored by event count client-side (#981)
}

// cityMapPin is the trimmed shape sent to the browser for the /cities map —
// short key names, only what's needed to place a marker and link/label it.
type cityMapPin struct {
	Town  string  `json:"town"`
	Slug  string  `json:"slug"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Count int     `json:"count"`
}

func citiesMapJSON(cities []City) template.JS {
	var pins []cityMapPin
	for _, c := range cities {
		if c.Latitude == nil || c.Longitude == nil {
			continue
		}
		pins = append(pins, cityMapPin{
			Town: c.Town, Slug: c.Slug, Lat: *c.Latitude, Lng: *c.Longitude, Count: c.EventCount,
		})
	}
	if pins == nil {
		return template.JS("[]")
	}
	b, _ := json.Marshal(pins)
	return template.JS(b)
}

type CityData struct {
	City            City
	Events          []Event
	GeoJSON         template.JS // compact JSON for map markers
	IncludePast     bool
	PastEvents      []Event     // #1506: most recent past events, shown directly when there's nothing upcoming
	PastEventsTotal int         // #1506: total past events ever held in this town (X-Total-Count)
	NearbyHint      *NearbyHint // #1506: "no upcoming events here, but N within X km" fallback
}

type cityGeoEvent struct {
	ID    int     `json:"id"`
	Title string  `json:"title"`
	Venue string  `json:"venue"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
}

func cityEventsGeoJSON(events []Event) template.JS {
	seen := map[string]bool{}
	var points []cityGeoEvent
	for _, e := range events {
		if e.Location == nil || e.Location.Latitude == nil || e.Location.Longitude == nil {
			continue
		}
		key := fmt.Sprintf("%.6f,%.6f", *e.Location.Latitude, *e.Location.Longitude)
		if seen[key] {
			continue
		}
		seen[key] = true
		points = append(points, cityGeoEvent{
			ID:    e.ID,
			Title: e.Title,
			Venue: e.Location.Location,
			Lat:   *e.Location.Latitude,
			Lng:   *e.Location.Longitude,
		})
	}
	if points == nil {
		return template.JS("[]")
	}
	b, _ := json.Marshal(points)
	return template.JS(b)
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func citiesHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cities, err := client.GetCities(r.Context(), false)
		if err != nil {
			http.Error(w, "could not load cities", http.StatusBadGateway)
			return
		}
		title := i18n.T(r, "cities_title")
		td := tmplData(r, cfg, i18n, title, CitiesData{Cities: cities, MapJSON: citiesMapJSON(cities)})
		td.MetaDescription = metaDesc(title, metaDescMaxLen)
		renderTemplate(w, tmpls.cities, td)
	}
}

func cityHubHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")

		// Resolve slug → town name via the full city list, including towns
		// with no upcoming event (#1506) — otherwise a town whose only
		// events are in the past 404s even though it has a venue and history.
		cities, err := client.GetCities(r.Context(), true)
		if err != nil {
			http.Error(w, "could not load cities", http.StatusBadGateway)
			return
		}
		var city City
		for _, c := range cities {
			if townSlug(c.Town) == slug {
				city = c
				break
			}
		}
		if city.Town == "" {
			http.NotFound(w, r)
			return
		}

		includePast := r.URL.Query().Get("include_past") == "true"

		filter := EventFilter{IsPublished: true, Town: city.Town, IncludePast: includePast}
		events, err := client.GetEventsFiltered(r.Context(), filter.Values())
		if err != nil {
			logHTTPError(w, r, "could not load city events", http.StatusBadGateway)
			return
		}
		if events == nil {
			events = []Event{}
		}

		// #1506: no upcoming events — load the town's recent past events
		// directly (like location.html) and offer a nearby-radius fallback
		// (#1436's helper) instead of a dead end.
		var pastEvents []Event
		var pastTotal int
		var hint *NearbyHint
		if len(events) == 0 && !includePast {
			var pErr error
			pastEvents, pastTotal, pErr = client.GetPastEventsByTownWithTotal(r.Context(), city.Town, locationPastEventsLimit)
			if pErr != nil {
				log.Printf("city %q: could not load past events: %v", city.Town, pErr)
			}
			if city.Latitude != nil && city.Longitude != nil {
				hint = nearbyHint(r.Context(), client, *city.Latitude, *city.Longitude, city.Town)
			}
		}

		title := i18n.T(r, "city_title_prefix") + city.Town
		td := tmplData(r, cfg, i18n, title, CityData{
			City:            city,
			Events:          events,
			GeoJSON:         cityEventsGeoJSON(events),
			IncludePast:     includePast,
			PastEvents:      pastEvents,
			PastEventsTotal: pastTotal,
			NearbyHint:      hint,
		})
		td.MetaDescription = metaDesc(title, metaDescMaxLen)
		renderTemplate(w, tmpls.city, td)
	}
}

// cityPastEventsHandler serves GET /city/{slug}/past-events — returns
// server-rendered HTML rows for past events (called by JS on demand).
func cityPastEventsHandler(tmpls *Templates, i18n *I18n, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")

		cities, err := client.GetCities(r.Context(), true)
		if err != nil {
			http.Error(w, "could not load cities", http.StatusBadGateway)
			return
		}
		var town string
		for _, c := range cities {
			if townSlug(c.Town) == slug {
				town = c.Town
				break
			}
		}
		if town == "" {
			http.NotFound(w, r)
			return
		}

		filter := EventFilter{
			IsPublished: true,
			Town:        town,
			IncludePast: true,
			// Only past events: end_time before now (approximate via start_time_before).
			// Actual filtering happens client-side or via a future API param.
		}
		events, err := client.GetEventsFiltered(r.Context(), filter.Values())
		if err != nil {
			http.Error(w, "could not load city events", http.StatusBadGateway)
			return
		}

		// Render as JSON array for the JS to consume.
		type pastEvent struct {
			ID        int    `json:"id"`
			Title     string `json:"title"`
			StartTime string `json:"start_time"`
			EndTime   string `json:"end_time"`
			Venue     string `json:"venue"`
			Town      string `json:"town"`
			URL       string `json:"url"`
			Cancelled bool   `json:"is_cancelled"`
		}
		var past []pastEvent
		for _, e := range events {
			var venue, town string
			if e.Location != nil {
				venue = e.Location.Location
				town = e.Location.Town
			}
			past = append(past, pastEvent{
				ID:        e.ID,
				Title:     e.Title,
				StartTime: e.StartTime,
				EndTime:   e.EndTime,
				Venue:     venue,
				Town:      town,
				URL:       fmt.Sprintf("/events/%d", e.ID),
				Cancelled: e.IsCancelled,
			})
		}
		if past == nil {
			past = []pastEvent{}
		}
		writeJSONResponse(w, http.StatusOK, past)
	}
}
