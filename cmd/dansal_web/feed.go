package main

import (
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

// feedEventICSHandler serves a single event as an iCal download.
func feedEventICSHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		event, err := client.GetEvent(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		cal := ics.NewCalendar()
		cal.SetMethod(ics.MethodPublish)
		feedAddEventToCalendar(cal, cfg.Domain, event)
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="event-%d.ics"`, id))
		w.Write([]byte(cal.Serialize()))
	}
}

// feedMainHandler serves all upcoming events. Uses GetAllFutureEvents rather
// than GetEvents(ctx, "") so subscribers get every future event, not just the
// index page's first 100 (see #650/#651).
func feedMainHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		events, err := client.GetAllFutureEvents(r.Context())
		if err != nil {
			http.Error(w, "could not load events", http.StatusBadGateway)
			return
		}
		if cfg.ShowFederatedEvents {
			if fes, err := listFederatedEvents(db); err == nil {
				for _, fe := range fes {
					events = append(events, federatedEventAsEvent(fe))
				}
			}
		}
		serveEventFeed(w, r, cfg, cfg.Domain+" events", events)
	}
}

func federatedEventAsEvent(fe FederatedEvent) Event {
	ev := Event{
		ID:          int(fe.ID),
		Title:       fe.Name,
		StartTime:   fe.StartTime,
		EndTime:     fe.EndTime,
		URL:         fe.URL,
		IsPublished: true,
		SourceURL:   fe.URL,
	}
	if fe.LocationName != "" {
		ev.Location = &Location{Location: fe.LocationName}
	}
	return ev
}

// feedOrgHandler serves events for one organisation, identified by its AP slug.
func feedOrgHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		} else if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		org, err := client.GetOrganization(r.Context(), actor.OrgID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		events, _ := client.GetEventsByOrg(r.Context(), actor.OrgID)
		serveEventFeed(w, r, cfg, org.Name, events)
	}
}

// feedMusicianHandler serves events for one musician, identified by slug.
func feedMusicianHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		musicians, err := client.GetMusicians(r.Context())
		if err != nil {
			http.Error(w, "could not load musicians", http.StatusBadGateway)
			return
		}
		var found *Musician
		for i := range musicians {
			if orgSlug(musicians[i].Bandname) == slug {
				found = &musicians[i]
				break
			}
		}
		if found == nil {
			http.NotFound(w, r)
			return
		}
		events, _ := client.GetPublicEventsByMusician(r.Context(), found.ID)
		serveEventFeed(w, r, cfg, found.Bandname, events)
	}
}

// feedLocationHandler serves events at one location, identified by slug.
func feedLocationHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		locs, err := client.GetLocations(r.Context())
		if err != nil {
			http.Error(w, "could not load locations", http.StatusBadGateway)
			return
		}
		var found *Location
		for i := range locs {
			if orgSlug(locs[i].Location) == slug {
				found = &locs[i]
				break
			}
		}
		if found == nil {
			http.NotFound(w, r)
			return
		}
		all, _ := client.GetEvents(r.Context(), "")
		var events []Event
		for _, e := range all {
			if e.Location != nil && e.Location.Location == found.Location {
				events = append(events, e)
			}
		}
		label := found.Location
		if found.Town != "" {
			label += ", " + found.Town
		}
		serveEventFeed(w, r, cfg, label, events)
	}
}

// feedTypeHandler serves events filtered by tag slug via the API.
func feedTypeHandler(cfg *Config, client *DansalClient, feedType string) http.HandlerFunc {
	tagMap := map[string]string{
		"ball":     "bal-folk",
		"workshop": "workshop",
		"festival": "festival",
	}
	return func(w http.ResponseWriter, r *http.Request) {
		tag := tagMap[feedType]
		events, _ := client.GetEvents(r.Context(), "?tag="+tag)
		if events == nil {
			events = []Event{}
		}
		serveEventFeed(w, r, cfg, feedType+" events", events)
	}
}

// serveEventFeed dispatches to the right format renderer based on the {format} route variable.
func serveEventFeed(w http.ResponseWriter, r *http.Request, cfg *Config, title string, events []Event) {
	if events == nil {
		events = []Event{}
	}
	selfURL := "https://" + cfg.Domain + r.URL.Path
	switch r.PathValue("format") {
	case "ical", "ics":
		serveICalFeed(w, cfg, events)
	case "json":
		serveJSONFeed(w, events)
	case "rss":
		serveRSSFeed(w, cfg, title, selfURL, events)
	default:
		http.NotFound(w, r)
	}
}

// serveICalFeed writes a text/calendar (iCal) response.
func serveICalFeed(w http.ResponseWriter, cfg *Config, events []Event) {
	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	cal.SetName(cfg.Domain)
	for _, e := range events {
		feedAddEventToCalendar(cal, cfg.Domain, e)
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="events.ics"`)
	w.Write([]byte(cal.Serialize()))
}

func feedAddEventToCalendar(cal *ics.Calendar, domain string, e Event) {
	vevent := cal.AddEvent(fmt.Sprintf("event-%d@%s", e.ID, domain))
	vevent.SetSummary(e.Title)
	if e.Description != "" {
		vevent.SetDescription(e.Description)
	}
	tStart, startOK := time.Parse(time.RFC3339, e.StartTime)
	tEnd, endOK := time.Parse(time.RFC3339, e.EndTime)
	if startOK == nil && endOK == nil &&
		tStart.Format("20060102") != tEnd.Format("20060102") {
		// Multi-day event: use all-day DATE values so every calendar app
		// shows the full span. DTEND is exclusive, so add one day.
		vevent.SetProperty(ics.ComponentPropertyDtStart,
			tStart.Format("20060102"), ics.WithValue("DATE"))
		vevent.SetProperty(ics.ComponentPropertyDtEnd,
			tEnd.AddDate(0, 0, 1).Format("20060102"), ics.WithValue("DATE"))
	} else {
		if startOK == nil {
			vevent.SetProperty(ics.ComponentPropertyDtStart, tStart.UTC().Format("20060102T150405Z"))
		}
		if endOK == nil {
			vevent.SetProperty(ics.ComponentPropertyDtEnd, tEnd.UTC().Format("20060102T150405Z"))
		}
	}
	if l := e.Location; l != nil {
		loc := l.Location
		if loc == "" {
			loc = l.Town
		}
		if loc != "" {
			vevent.SetLocation(loc)
		}
		if l.Latitude != nil && l.Longitude != nil {
			ics.SetGeo(vevent, *l.Latitude, *l.Longitude)
		}
	}
	if e.URL != "" {
		vevent.SetProperty(ics.ComponentPropertyUrl, e.URL)
	}
	if len(e.Tags) > 0 {
		vevent.SetProperty(ics.ComponentPropertyCategories, strings.Join(e.Tags, ","))
	}
}

// serveJSONFeed writes events as an application/json array.
func serveJSONFeed(w http.ResponseWriter, events []Event) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(events)
}

// RSS 2.0 output types (local to dansal_web, no conflict with dansal's input types).
type feedRSSRoot struct {
	XMLName xml.Name       `xml:"rss"`
	Version string         `xml:"version,attr"`
	Georss  string         `xml:"xmlns:georss,attr"`
	Channel feedRSSChannel `xml:"channel"`
}

type feedRSSChannel struct {
	Title       string        `xml:"title"`
	Link        string        `xml:"link"`
	Description string        `xml:"description"`
	Items       []feedRSSItem `xml:"item"`
}

type feedRSSItem struct {
	Title      string   `xml:"title"`
	Link       string   `xml:"link"`
	GUID       string   `xml:"guid"`
	PubDate    string   `xml:"pubDate"`
	Desc       string   `xml:"description,omitempty"`
	EventStart string   `xml:"eventStart,omitempty"`
	EventEnd   string   `xml:"eventEnd,omitempty"`
	Location   string   `xml:"location,omitempty"`
	GeoPoint   string   `xml:"georss:point,omitempty"`
	Categories []string `xml:"category"`
}

// serveRSSFeed writes an RSS 2.0 feed with eventStart/eventEnd extension elements.
func serveRSSFeed(w http.ResponseWriter, cfg *Config, title, selfURL string, events []Event) {
	items := make([]feedRSSItem, 0, len(events))
	for _, e := range events {
		link := e.URL
		if link == "" {
			link = fmt.Sprintf("https://%s/events/%d", cfg.Domain, e.ID)
		}
		var pubDate string
		if t, err := time.Parse(time.RFC3339, e.StartTime); err == nil {
			pubDate = t.UTC().Format(time.RFC1123Z)
		}
		var loc, geoPoint string
		if l := e.Location; l != nil {
			loc = l.Location
			if loc == "" {
				loc = l.Town
			}
			if l.Latitude != nil && l.Longitude != nil {
				geoPoint = fmt.Sprintf("%v %v", *l.Latitude, *l.Longitude)
			}
		}
		items = append(items, feedRSSItem{
			Title:      e.Title,
			Link:       link,
			GUID:       fmt.Sprintf("https://%s/events/%d", cfg.Domain, e.ID),
			PubDate:    pubDate,
			Desc:       e.Description,
			EventStart: e.StartTime,
			EventEnd:   e.EndTime,
			Location:   loc,
			GeoPoint:   geoPoint,
			Categories: e.Tags,
		})
	}

	root := feedRSSRoot{
		Version: "2.0",
		Georss:  "http://www.georss.org/georss",
		Channel: feedRSSChannel{
			Title:       title,
			Link:        selfURL,
			Description: title,
			Items:       items,
		},
	}

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(root)
}

// feedURL builds the canonical feed URL for a given path and format extension.
func feedURL(cfg *Config, path, format string) string {
	return "https://" + cfg.Domain + path + "/events." + format
}

// feedRouter is an HTTP middleware that intercepts GET requests whose paths match
// feed or ICS URL patterns that Go's net/http ServeMux rejects at startup because
// the wildcard is not the whole path segment (e.g. "{id}.ics", "events.{format}").
func feedRouter(cfg *Config, db *sql.DB, client *DansalClient) func(http.Handler) http.Handler {
	icsH := feedEventICSHandler(cfg, client)
	mainH := feedMainHandler(cfg, db, client)
	orgH := feedOrgHandler(cfg, db, client)
	musicianH := feedMusicianHandler(cfg, client)
	locationH := feedLocationHandler(cfg, client)
	ballH := feedTypeHandler(cfg, client, "ball")
	workshopH := feedTypeHandler(cfg, client, "workshop")
	festivalH := feedTypeHandler(cfg, client, "festival")

	const evDot = "/events."

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			p := r.URL.Path
			switch {
			case strings.HasPrefix(p, "/events/") && strings.HasSuffix(p, ".ics"):
				r.SetPathValue("id", strings.TrimSuffix(strings.TrimPrefix(p, "/events/"), ".ics"))
				icsH.ServeHTTP(w, r)
			case strings.HasPrefix(p, "/feed/org/"):
				rest := strings.TrimPrefix(p, "/feed/org/")
				if i := strings.LastIndex(rest, evDot); i >= 0 {
					r.SetPathValue("slug", rest[:i])
					r.SetPathValue("format", rest[i+len(evDot):])
					orgH.ServeHTTP(w, r)
				} else {
					next.ServeHTTP(w, r)
				}
			case strings.HasPrefix(p, "/feed/musician/"):
				rest := strings.TrimPrefix(p, "/feed/musician/")
				if i := strings.LastIndex(rest, evDot); i >= 0 {
					r.SetPathValue("slug", rest[:i])
					r.SetPathValue("format", rest[i+len(evDot):])
					musicianH.ServeHTTP(w, r)
				} else {
					next.ServeHTTP(w, r)
				}
			case strings.HasPrefix(p, "/feed/location/"):
				rest := strings.TrimPrefix(p, "/feed/location/")
				if i := strings.LastIndex(rest, evDot); i >= 0 {
					r.SetPathValue("slug", rest[:i])
					r.SetPathValue("format", rest[i+len(evDot):])
					locationH.ServeHTTP(w, r)
				} else {
					next.ServeHTTP(w, r)
				}
			case strings.HasPrefix(p, "/feed/ball/events."):
				r.SetPathValue("format", strings.TrimPrefix(p, "/feed/ball/events."))
				ballH.ServeHTTP(w, r)
			case strings.HasPrefix(p, "/feed/workshop/events."):
				r.SetPathValue("format", strings.TrimPrefix(p, "/feed/workshop/events."))
				workshopH.ServeHTTP(w, r)
			case strings.HasPrefix(p, "/feed/festival/events."):
				r.SetPathValue("format", strings.TrimPrefix(p, "/feed/festival/events."))
				festivalH.ServeHTTP(w, r)
			case strings.HasPrefix(p, "/feed/events."):
				r.SetPathValue("format", strings.TrimPrefix(p, "/feed/events."))
				mainH.ServeHTTP(w, r)
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}
