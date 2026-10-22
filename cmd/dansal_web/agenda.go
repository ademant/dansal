package main

import (
	"net/http"
	"sort"
	"time"
)

// The agenda (#1580): a printable, day-grouped view of the next seven days.
// It shares the organisation and location filtering with /embed/events, so the
// two can't drift apart.

// agendaWindowDays is how many days the agenda shows, today included.
const agendaWindowDays = 7

// filterEventsByOrgLoc keeps the events of the given organisations and
// locations. A nil map means "no filter on that dimension".
func filterEventsByOrgLoc(events []Event, orgFilter, locFilter map[int]bool) []Event {
	if orgFilter == nil && locFilter == nil {
		return events
	}
	var out []Event
	for _, e := range events {
		if orgFilter != nil && (e.OrganizationID == nil || !orgFilter[*e.OrganizationID]) {
			continue
		}
		if locFilter != nil && (e.LocationID == nil || !locFilter[*e.LocationID]) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// agendaEntry is one event line in the agenda.
type agendaEntry struct {
	ID          int
	Title       string
	TimeLabel   string
	Where       string
	Organizer   string
	IsCancelled bool
}

// agendaDay groups the entries of one local calendar day.
type agendaDay struct {
	Label   string
	Date    string
	Entries []agendaEntry
}

// agendaDays builds the day-grouped agenda for the window that starts today.
// Cancelled events are left out; entries are sorted by start time within each
// day. Times are read in the server's local zone, which is the instance zone.
func agendaDays(events []Event, orgNames map[int]string, now time.Time, lang, dateFormat string) []agendaDay {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := today.AddDate(0, 0, agendaWindowDays)

	type timed struct {
		ev Event
		t  time.Time
	}
	var inWindow []timed
	for _, e := range events {
		if e.IsCancelled {
			continue
		}
		t, ok := parseTime(e.StartTime)
		if !ok {
			continue
		}
		t = t.In(now.Location())
		if t.Before(today) || !t.Before(end) {
			continue
		}
		inWindow = append(inWindow, timed{ev: e, t: t})
	}
	sort.SliceStable(inWindow, func(i, j int) bool { return inWindow[i].t.Before(inWindow[j].t) })

	var days []agendaDay
	index := map[string]int{}
	for _, item := range inWindow {
		date := item.t.Format("2006-01-02")
		i, ok := index[date]
		if !ok {
			i = len(days)
			index[date] = i
			days = append(days, agendaDay{
				Label: formatDateStr(lang, dateFormat, item.t.Format(time.RFC3339)),
				Date:  date,
			})
		}
		where := ""
		if item.ev.Location != nil {
			where = item.ev.Location.Location
			if item.ev.Location.Town != "" && item.ev.Location.Town != where {
				where = where + ", " + item.ev.Location.Town
			}
		}
		org := ""
		if item.ev.OrganizationID != nil {
			org = orgNames[*item.ev.OrganizationID]
		}
		days[i].Entries = append(days[i].Entries, agendaEntry{
			ID:        item.ev.ID,
			Title:     item.ev.Title,
			TimeLabel: item.t.Format("15:04"),
			Where:     where,
			Organizer: org,
		})
	}
	return days
}

// agendaPageData is the template data for /agenda.
type agendaPageData struct {
	Days []agendaDay
}

// agendaHandler serves GET /agenda. The filters are org (slug, repeatable),
// location (id, repeatable) and tag (one slug), so a filtered agenda can be
// shared as a link.
func agendaHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allEvents, err := client.GetEvents(r.Context(), "")
		if err != nil {
			logHTTPError(w, r, "agenda: could not load events", http.StatusBadGateway)
			return
		}
		allOrgs, err := client.GetOrganizations(r.Context())
		if err != nil {
			logHTTPError(w, r, "agenda: could not load organisations", http.StatusBadGateway)
			return
		}
		q := r.URL.Query()
		events := filterEventsByOrgLoc(allEvents, resolveOrgSlugs(allOrgs, q["org"]), resolveLocationIDs(q["location"]))
		if tag := q.Get("tag"); tag != "" {
			var tagged []Event
			for _, e := range events {
				for _, t := range e.Tags {
					if t == tag {
						tagged = append(tagged, e)
						break
					}
				}
			}
			events = tagged
		}

		orgNames := make(map[int]string, len(allOrgs))
		for _, o := range allOrgs {
			orgNames[o.ID] = o.Name
		}
		lang := i18n.detectLang(r)
		days := agendaDays(events, orgNames, time.Now(), lang, siteCfg.DateFormat())
		title := i18n.T(r, "agenda_title")
		renderTemplate(w, tmpls.agenda, tmplData(r, cfg, i18n, title, agendaPageData{Days: days}))
	}
}
