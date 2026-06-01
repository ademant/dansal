package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type FeedLocation struct {
	Idx             int
	Name            string // original location.location from the feed
	Display         string // "Name, Town Country" for the UI
	MatchedDBLocID   int    // non-zero when a DB location with the same name exists
	MatchedDBLocName string // display name of the matched DB location
}

type AdminImportEventsData struct {
	PreviewEvents  []PreviewEvent
	PreviewJSON    []string
	Error          string
	FeedURL        string
	FeedType       string
	Orgs           []Organization
	Locations      []Location
	UniqueFeedLocs []FeedLocation
}

// ── Import events ─────────────────────────────────────────────────────────────

func adminImportEventsPageHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		title := i18n.T(r, "admin_import_title")
		renderTemplate(w, tmpls.adminEventsImport, tmplData(r, cfg, i18n, title, AdminImportEventsData{}))
	}
}

func adminImportEventsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}

		renderErr := func(msg, feedURL, feedType string) {
			title := i18n.T(r, "admin_import_title")
			renderTemplate(w, tmpls.adminEventsImport, tmplData(r, cfg, i18n, title, AdminImportEventsData{
				Error:   msg,
				FeedURL: feedURL,
				FeedType: feedType,
			}))
		}

		if err := r.ParseMultipartForm(10 << 20); err != nil {
			renderErr("invalid form", "", "ical")
			return
		}

		feedURL := r.FormValue("url")
		feedType := r.FormValue("type")
		if feedType == "" {
			feedType = "ical"
		}
		// Normalise legacy type names to the unified "json" type.
		if feedType == "folkdance-json" || feedType == "gancio-json" {
			feedType = "json"
		}
		orgID := r.FormValue("organization_id")

		// Build a new multipart body to forward to the API.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("type", feedType)
		if orgID != "" {
			mw.WriteField("organization_id", orgID)
		}

		if feedURL != "" {
			mw.WriteField("url", feedURL)
		} else {
			file, hdr, err := r.FormFile("file")
			if err != nil {
				renderErr(i18n.T(r, "admin_import_file_label")+": required", feedURL, feedType)
				return
			}
			defer file.Close()
			part, _ := mw.CreateFormFile("file", hdr.Filename)
			io.Copy(part, file)
		}
		mw.Close()

		token := getSessionToken(r)
		events, err := client.PreviewEvents(r.Context(), &buf, mw.FormDataContentType(), token)
		if err != nil {
			renderErr(err.Error(), feedURL, feedType)
			return
		}

		if len(events) == 0 {
			renderErr(i18n.T(r, "admin_import_none_found"), feedURL, feedType)
			return
		}

		if len(events) == 1 {
			e := events[0]
			q := url.Values{}
			q.Set("title", e.Title)
			if e.Description != "" {
				q.Set("description", e.Description)
			}
			if e.URL != "" {
				q.Set("url", e.URL)
			}
			if t, err := time.Parse(time.RFC3339, e.StartTime); err == nil {
				q.Set("date", t.Format("2006-01-02"))
				q.Set("start_time", t.Format("15:04"))
			}
			if e.EndTime != "" {
				if t, err := time.Parse(time.RFC3339, e.EndTime); err == nil {
					q.Set("end_date", t.Format("2006-01-02"))
					q.Set("end_time", t.Format("15:04"))
				}
			}
			if e.Location.Location != "" {
				q.Set("location", e.Location.Location)
			}
			if e.Location.Town != "" {
				q.Set("town", e.Location.Town)
			}
			if e.Location.Country != "" {
				q.Set("country", e.Location.Country)
			}
			for _, tag := range e.Tags {
				q.Add("tags", tag)
			}
			http.Redirect(w, r, "/admin/events/new?"+q.Encode(), http.StatusSeeOther)
			return
		}

		previewJSON := make([]string, len(events))
		for i, e := range events {
			b, _ := json.Marshal(e)
			previewJSON[i] = string(b)
		}

		orgs, _ := client.GetOrganizations(r.Context())
		locs, _ := client.GetLocations(r.Context())

		// Build a name→location map so we can auto-match feed locations to
		// existing DB locations without the admin having to pick them manually.
		locByName := make(map[string]Location, len(locs))
		for _, l := range locs {
			locByName[l.Location] = l
		}

		seen := map[string]bool{}
		var uniqLocs []FeedLocation
		for _, e := range events {
			if e.Location.Location != "" && !seen[e.Location.Location] {
				seen[e.Location.Location] = true
				display := e.Location.Location
				if e.Location.Town != "" {
					display += ", " + e.Location.Town
				}
				if e.Location.Country != "" {
					display += " " + e.Location.Country
				}
				fl := FeedLocation{
					Idx:     len(uniqLocs),
					Name:    e.Location.Location,
					Display: display,
				}
				if dbLoc, ok := locByName[e.Location.Location]; ok {
					fl.MatchedDBLocID = dbLoc.ID
					fl.MatchedDBLocName = dbLoc.Location
					if dbLoc.Town != "" {
						fl.MatchedDBLocName += ", " + dbLoc.Town
					}
				} else if e.Location.Address != "" {
					composite := e.Location.Location + " - " + e.Location.Address
					if dbLoc, ok := locByName[composite]; ok {
						fl.MatchedDBLocID = dbLoc.ID
						fl.MatchedDBLocName = dbLoc.Location
						if dbLoc.Town != "" {
							fl.MatchedDBLocName += ", " + dbLoc.Town
						}
					}
				}
				uniqLocs = append(uniqLocs, fl)
			}
		}

		title := i18n.T(r, "admin_import_title")
		renderTemplate(w, tmpls.adminEventsImport, tmplData(r, cfg, i18n, title, AdminImportEventsData{
			PreviewEvents:  events,
			PreviewJSON:    previewJSON,
			FeedURL:        feedURL,
			FeedType:       feedType,
			Orgs:           orgs,
			Locations:      locs,
			UniqueFeedLocs: uniqLocs,
		}))
	}
}

func adminImportConfirmHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Build location name override map: feed loc name → DB location.
		var locByID map[int]Location
		feedLocOverride := map[string]Location{}
		for i := 0; ; i++ {
			feedName := r.FormValue("loc_feed_" + strconv.Itoa(i))
			if feedName == "" {
				break
			}
			dbLocIDStr := r.FormValue("loc_map_" + strconv.Itoa(i))
			if dbLocIDStr == "" {
				continue
			}
			dbLocID, err := strconv.Atoi(dbLocIDStr)
			if err != nil || dbLocID == 0 {
				continue
			}
			if locByID == nil {
				locs, _ := client.GetLocations(r.Context())
				locByID = make(map[int]Location, len(locs))
				for _, l := range locs {
					locByID[l.ID] = l
				}
			}
			if dbLoc, found := locByID[dbLocID]; found {
				feedLocOverride[feedName] = dbLoc
			}
		}

		// Parse org override.
		orgID, _ := strconv.Atoi(r.FormValue("org_id"))

		token := getSessionToken(r)
		createdAt := time.Now().UTC().Format(time.RFC3339)

		var selected []json.RawMessage
		for i := 0; ; i++ {
			vals := r.Form["event_"+strconv.Itoa(i)]
			if len(vals) == 0 {
				break
			}
			if len(r.Form["sel_"+strconv.Itoa(i)]) == 0 {
				continue
			}
			raw := vals[0]
			if orgID > 0 || len(feedLocOverride) > 0 {
				var ev map[string]any
				if err := json.Unmarshal([]byte(raw), &ev); err == nil {
					if orgID > 0 {
						ev["organization_id"] = orgID
					}
					if len(feedLocOverride) > 0 {
						if locField, ok := ev["location"].(map[string]any); ok {
							feedLocName, _ := locField["location"].(string)
							if dbLoc, mapped := feedLocOverride[feedLocName]; mapped {
								locField["location"] = dbLoc.Location
								locField["address"] = dbLoc.Address
								locField["zipcode"] = dbLoc.Zipcode
								locField["town"] = dbLoc.Town
								locField["country"] = dbLoc.Country
								// Keep the feed's coordinates when present — they may be
								// newer than what's in the DB. Fall back to DB values only
								// when the feed doesn't supply any geodata.
								_, hasLat := locField["latitude"]
								_, hasLon := locField["longitude"]
								if !hasLat && !hasLon {
									delete(locField, "latitude")
									delete(locField, "longitude")
									if dbLoc.Latitude != nil {
										locField["latitude"] = *dbLoc.Latitude
									}
									if dbLoc.Longitude != nil {
										locField["longitude"] = *dbLoc.Longitude
									}
								}
							}
						}
					}
					if b, err := json.Marshal(ev); err == nil {
						raw = string(b)
					}
				}
			}
			selected = append(selected, json.RawMessage(raw))
		}

		if len(selected) > 0 {
			client.CreateEventBatch(r.Context(), selected, token)
		}

		feedURL := r.FormValue("feed_url")
		feedType := r.FormValue("feed_type")
		if feedURL != "" {
			q := url.Values{}
			q.Set("url", feedURL)
			if feedType != "" {
				q.Set("type", feedType)
			}
			if orgID > 0 {
				q.Set("org_id", strconv.Itoa(orgID))
			}
			q.Set("created_after", createdAt)
			http.Redirect(w, r, "/admin/fetchurls/new?"+q.Encode(), http.StatusSeeOther)
			return
		}

		q := url.Values{}
		q.Set("created_after", createdAt)
		q.Set("include_past", "1")
		http.Redirect(w, r, "/admin/events?"+q.Encode(), http.StatusSeeOther)
	}
}
