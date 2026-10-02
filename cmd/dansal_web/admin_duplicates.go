package main

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// #1427: comparison page for a flagged possible-duplicate pair —
// /admin/duplicates/{id} shows the event next to its candidate
// (duplicate_of_id) and offers Merge (choose the survivor), Accept (both are
// real), Save (date/time or venue/room fixed so they no longer collide) and
// Delete. The edit form's "check duplicate status" dialog uses the JSON
// status/clear endpoints below. Whether a pair still collides is always
// decided by the API (cmd/dansal duplicates.go pairCollision).

// DupSide is one column (A or B) of the comparison.
type DupSide struct {
	Key          string // "a" / "b"
	Event        Event
	Date         string // start date, YYYY-MM-DD (instance local, as the API sends it)
	Start, End   string // HH:MM
	EndDate      string
	BuildingID   int // top-level venue the event is at (0 = none)
	BuildingName string
	LocationID   int // the event's own location (building or room)
	Rooms        []Location
	OrgName      string
	SeriesName   string
	SourceLabel  string
	SourceLink   string
	Excerpt      string
}

type AdminDuplicateData struct {
	A, B DupSide
	// Reason flags from the API's check: Time is the 3h precondition, Venue /
	// TitleReason the additional match that makes it a collision.
	Time, Venue, TitleReason bool
	Still                    bool // a save just happened but they still collide
	Others                   []DuplicateConflict
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func dupSide(key string, e Event, locs []Location, orgName, seriesName string) DupSide {
	s := DupSide{Key: key, Event: e, OrgName: orgName, SeriesName: seriesName, Excerpt: excerpt(e.Description, 300)}
	if t, ok := parseTime(e.StartTime); ok {
		s.Date, s.Start = t.Format("2006-01-02"), t.Format("15:04")
	}
	if t, ok := parseTime(e.EndTime); ok {
		s.EndDate, s.End = t.Format("2006-01-02"), t.Format("15:04")
	}
	if e.LocationID != nil {
		s.LocationID = *e.LocationID
		s.BuildingID = s.LocationID
		for _, l := range locs {
			if l.ID == s.LocationID && l.ParentID != nil {
				s.BuildingID = *l.ParentID
			}
		}
		for _, l := range locs {
			if l.ID == s.BuildingID {
				s.BuildingName = cmp.Or(l.ShortName, l.Location)
				if l.Town != "" {
					s.BuildingName += ", " + l.Town
				}
			}
			if l.ParentID != nil && *l.ParentID == s.BuildingID {
				s.Rooms = append(s.Rooms, l)
			}
		}
		slices.SortFunc(s.Rooms, func(a, b Location) int { return strings.Compare(a.Location, b.Location) })
	}
	switch {
	case e.FetchSourceID > 0:
		s.SourceLabel = fmt.Sprintf("feed #%d", e.FetchSourceID)
		s.SourceLink = fmt.Sprintf("/admin/fetchurls/%d/edit", e.FetchSourceID)
	case e.Source != "":
		s.SourceLabel = e.Source
	}
	return s
}

func adminDuplicatePageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, r); !ok {
			return
		}
		id, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		ctx, token := r.Context(), getSessionToken(r)
		a, err := client.GetEventAuthed(ctx, id, token)
		if errors.Is(err, errNotFound) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			http.Error(w, "could not load event", http.StatusBadGateway)
			return
		}
		editURL := fmt.Sprintf("/admin/events/%d/edit", id)
		if !a.NeedsDuplicateReview || a.DuplicateOfID == nil {
			http.Redirect(w, r, editURL, http.StatusSeeOther)
			return
		}
		b, err := client.GetEventAuthed(ctx, *a.DuplicateOfID, token)
		if err != nil {
			// Partner gone: the edit form's check dialog offers "Cleaned".
			http.Redirect(w, r, editURL, http.StatusSeeOther)
			return
		}
		check, _ := client.GetDuplicateCheck(ctx, id, token)
		locs, _ := client.GetLocations(ctx)
		orgs, _ := client.GetOrganizations(ctx)
		orgName := func(e Event) string {
			if e.OrganizationID != nil {
				for _, o := range orgs {
					if o.ID == *e.OrganizationID {
						return o.Name
					}
				}
			}
			return ""
		}
		seriesName := func(e Event) string {
			if e.SeriesID != nil {
				if s, err := client.GetSeriesByID(ctx, *e.SeriesID, token); err == nil {
					return s.Title
				}
			}
			return ""
		}
		data := AdminDuplicateData{
			A:      dupSide("a", a, locs, orgName(a), seriesName(a)),
			B:      dupSide("b", b, locs, orgName(b), seriesName(b)),
			Still:  r.URL.Query().Get("result") == "still",
			Others: check.Others,
		}
		for _, reason := range check.Reasons {
			switch reason {
			case "time":
				data.Time = true
			case "venue":
				data.Venue = true
			case "title", "similar_title":
				data.TitleReason = true
			}
		}
		renderTemplate(w, tmpls.adminDuplicate, tmplData(r, cfg, i18n, i18n.T(r, "dup_page_title"), data))
	}
}

// shiftDate moves an ISO date by the same number of days the start date moved
// (keeps a multi-day event's length when its start date is corrected).
func shiftDate(endDate, oldStart, newStart string) string {
	o, err1 := time.Parse("2006-01-02", oldStart)
	n, err2 := time.Parse("2006-01-02", newStart)
	e, err3 := time.Parse("2006-01-02", endDate)
	if err1 != nil || err2 != nil || err3 != nil {
		return newStart
	}
	return e.Add(n.Sub(o)).Format("2006-01-02")
}

func addDaysISO(iso string, n int) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// POST /admin/duplicates/{id}/save — applies the changed date/time and
// venue/room of either side, then asks the API whether the pair still
// collides (the PATCH already cleared the flags if not).
func adminDuplicateSaveHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, r); !ok {
			return
		}
		id, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ctx, token := r.Context(), getSessionToken(r)
		for _, s := range []string{"a", "b"} {
			var evID int
			if _, err := fmt.Sscan(r.FormValue(s+"_id"), &evID); err != nil || evID <= 0 {
				continue
			}
			f := func(k string) string { return strings.TrimSpace(r.FormValue(s + "_" + k)) }
			if f("date") != f("odate") || f("start") != f("ostart") || f("end") != f("oend") {
				start := f("date") + "T" + f("start") + ":00"
				// Keep the event's length in days; a same-day end before the
				// start time means it ends after midnight.
				endDate := f("date")
				if f("oenddate") != "" {
					endDate = shiftDate(f("oenddate"), f("odate"), f("date"))
				}
				if endDate == f("date") && f("end") != "" && f("end") <= f("start") {
					endDate = addDaysISO(f("date"), 1)
				}
				end := ""
				if f("end") != "" {
					end = endDate + "T" + f("end") + ":00"
				}
				if err := client.PatchEventTimes(ctx, evID, start, end, token); err != nil {
					http.Error(w, "could not save date/time: "+err.Error(), http.StatusBadGateway)
					return
				}
			}
			if f("loc") != f("oloc") {
				var loc int
				if _, err := fmt.Sscan(f("loc"), &loc); err == nil && loc > 0 {
					if err := client.PatchEventLocation(ctx, evID, loc, token); err != nil {
						http.Error(w, "could not save venue: "+err.Error(), http.StatusBadGateway)
						return
					}
				}
			}
		}
		client.invalidateEvents()
		check, err := client.GetDuplicateCheck(ctx, id, token)
		if err == nil && check.Flagged {
			http.Redirect(w, r, fmt.Sprintf("/admin/duplicates/%d?result=still", id), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/events?flagged=1", http.StatusSeeOther)
	}
}

// POST /admin/duplicates/{id}/accept — both are real, different events.
func adminDuplicateAcceptHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, r); !ok {
			return
		}
		id, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		if _, err := client.ResolveDuplicate(r.Context(), id, "accept", getSessionToken(r)); err != nil {
			http.Error(w, "could not accept: "+err.Error(), http.StatusBadGateway)
			return
		}
		client.invalidateEvents()
		http.Redirect(w, r, "/admin/events?flagged=1", http.StatusSeeOther)
	}
}

// GET /admin/duplicates/{id}/status — JSON for the edit form's check dialog.
func adminDuplicateStatusHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, r); !ok {
			return
		}
		id, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		check, err := client.GetDuplicateCheck(r.Context(), id, getSessionToken(r))
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, "could not check")
			return
		}
		writeJSONResponse(w, http.StatusOK, check)
	}
}

// POST /admin/duplicates/{id}/clear — "Cleaned": clears the pair only if it
// really no longer collides (409 + current check otherwise).
func adminDuplicateClearHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, r); !ok {
			return
		}
		id, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		check, err := client.ResolveDuplicate(r.Context(), id, "resolved", getSessionToken(r))
		if errors.Is(err, errDuplicateStillColliding) {
			writeJSONResponse(w, http.StatusConflict, check)
			return
		} else if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, "could not clear")
			return
		}
		client.invalidateEvents()
		writeJSONResponse(w, http.StatusOK, map[string]bool{"cleared": true})
	}
}
