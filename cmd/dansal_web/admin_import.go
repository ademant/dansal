package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type FeedLocation struct {
	Idx              int
	Name             string // original location.location from the feed
	Display          string // "Name, Town Country" for the UI
	MatchedDBLocID   int    // non-zero when a DB location with the same name exists
	MatchedDBLocName string // display name of the matched DB location
}

// FeedCategory is one unique raw feed category string (e.g. iCal CATEGORIES,
// RSS/Atom <category>) found across the previewed events, with the tag it
// already resolves to (if any) so the import UI can offer a mapping picker
// for the rest — mirrors FeedLocation (#1093).
type FeedCategory struct {
	Idx            int
	Name           string // raw feed category string
	MatchedTagSlug string // non-empty when it's already a known slug or has an alias
	MatchedTagName string // display name of the matched tag
}

type AdminImportEventsData struct {
	PreviewEvents        []PreviewEvent
	PreviewJSON          []string
	Error                string
	FeedURL              string
	FeedType             string
	Orgs                 []Organization
	Locations            []Location
	UniqueFeedLocs       []FeedLocation
	Tags                 []Tag
	UniqueFeedCategories []FeedCategory
	SelectedOrgID        int
	Templates            []EventTemplate
	SelectedTemplateID   int
}

// ── Import events ─────────────────────────────────────────────────────────────

func adminImportEventsPageHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		selectedOrgID, _ := strconv.Atoi(r.URL.Query().Get("org_id"))
		title := i18n.T(r, "admin_import_title")
		renderTemplate(w, tmpls.adminEventsImport, tmplData(r, cfg, i18n, title, AdminImportEventsData{
			SelectedOrgID: selectedOrgID,
		}))
	}
}

func adminImportEventsHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}

		renderErr := func(msg, feedURL, feedType string) {
			title := i18n.T(r, "admin_import_title")
			renderTemplate(w, tmpls.adminEventsImport, tmplData(r, cfg, i18n, title, AdminImportEventsData{
				Error:    msg,
				FeedURL:  feedURL,
				FeedType: feedType,
			}))
		}

		if err := r.ParseMultipartForm(maxMultipartSize); err != nil {
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

		if su := getSessionUser(r); su != nil && su.Role != "admin" && orgID == "" {
			renderErr(i18n.T(r, "admin_import_org_required"), feedURL, feedType)
			return
		}

		// Build a new multipart body to forward to the API.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("type", feedType)
		if orgID != "" {
			mw.WriteField("organization_id", orgID)
		}

		source := feedURL
		var size int64
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
			source = hdr.Filename
			size = hdr.Size
		}
		mw.Close()

		token := getSessionToken(r)
		events, err := client.PreviewEvents(r.Context(), &buf, mw.FormDataContentType(), token)
		if err != nil {
			log.Printf("import: source=%q size=%d type=%s result=error caller=%d err=%v", source, size, feedType, su.ID, err)
			renderErr(err.Error(), feedURL, feedType)
			return
		}
		log.Printf("import: source=%q size=%d type=%s result=ok events=%d caller=%d", source, size, feedType, len(events), su.ID)

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
			if e.Location.Address != "" {
				q.Set("address", e.Location.Address)
			}
			if e.Location.Zipcode != "" {
				q.Set("zipcode", e.Location.Zipcode)
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

		selectedOrgID, _ := strconv.Atoi(orgID)

		orgs, _ := client.GetOrganizations(r.Context())
		locs, _ := client.GetLocations(r.Context())
		// Template picker (#1084): the choice made here isn't applied to the
		// previewed events themselves, only carried through to pre-select the
		// same template on the "create fetchurl" step below, since that's the
		// only place a template on a URL-based import currently means anything
		// (a template stored on a recurring fetch source, applied to every
		// future fetch — see fetchurl.go's TemplateID/TemplateMode).
		templates, err := listTemplates(db, su.ID, orgIDsForTemplates(r, client, su))
		if err != nil {
			log.Printf("admin import: could not load templates: %v", err)
		}

		// Build a name→location map so we can auto-match feed locations to
		// existing DB locations without the admin having to pick them manually.
		locByName := make(map[string]Location, len(locs))
		for _, l := range locs {
			locByName[l.Location] = l
			for _, alias := range l.Aliases {
				if _, exists := locByName[alias]; !exists {
					locByName[alias] = l
				}
			}
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

		// Build the category mapping table (#1093): one row per unique raw
		// feed category, auto-matched when it's already a known tag slug or
		// has an admin-configured alias, otherwise offered a picker.
		tags, err := client.GetTags(r.Context())
		if err != nil {
			log.Printf("admin import: could not load tags: %v", err)
		}
		catAliases, err := client.GetCategoryAliases(r.Context())
		if err != nil {
			log.Printf("admin import: could not load category aliases: %v", err)
		}
		tagBySlug := make(map[string]Tag, len(tags))
		for _, t := range tags {
			tagBySlug[t.Slug] = t
		}
		aliasByCategory := make(map[string]string, len(catAliases))
		for _, a := range catAliases {
			aliasByCategory[a.Category] = a.TagSlug
		}

		seenCat := map[string]bool{}
		var uniqCats []FeedCategory
		for _, e := range events {
			for _, cat := range e.Tags {
				if cat == "" || seenCat[cat] {
					continue
				}
				seenCat[cat] = true
				fc := FeedCategory{Idx: len(uniqCats), Name: cat}
				if t, ok := tagBySlug[cat]; ok {
					fc.MatchedTagSlug = cat
					fc.MatchedTagName = t.Name
				} else if slug, ok := aliasByCategory[cat]; ok {
					fc.MatchedTagSlug = slug
					if t, ok := tagBySlug[slug]; ok {
						fc.MatchedTagName = t.Name
					}
				}
				uniqCats = append(uniqCats, fc)
			}
		}

		title := i18n.T(r, "admin_import_title")
		renderTemplate(w, tmpls.adminEventsImport, tmplData(r, cfg, i18n, title, AdminImportEventsData{
			PreviewEvents:        events,
			PreviewJSON:          previewJSON,
			FeedURL:              feedURL,
			FeedType:             feedType,
			Orgs:                 orgs,
			Locations:            locs,
			UniqueFeedLocs:       uniqLocs,
			Tags:                 tags,
			UniqueFeedCategories: uniqCats,
			SelectedOrgID:        selectedOrgID,
			Templates:            templates,
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
		locs, _ := client.GetLocations(r.Context())
		locByID := make(map[int]Location, len(locs))
		for _, l := range locs {
			locByID[l.ID] = l
		}
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
			if dbLoc, found := locByID[dbLocID]; found {
				feedLocOverride[feedName] = dbLoc
			}
		}

		// Build category mapping override: raw feed category → tag slug (#1093).
		// Every unique category shown on the preview page submits a
		// cat_feed_N/cat_map_N pair — already-resolved ones (exact slug match
		// or existing alias) carry their resolved slug as a hidden field, so
		// this map covers all of them, not just newly-picked ones.
		catOverride := map[string]string{}
		for i := 0; ; i++ {
			feedCat := r.FormValue("cat_feed_" + strconv.Itoa(i))
			if feedCat == "" {
				break
			}
			slug := r.FormValue("cat_map_" + strconv.Itoa(i))
			if slug != "" {
				catOverride[feedCat] = slug
			}
		}

		// Parse org override.
		orgID, _ := strconv.Atoi(r.FormValue("org_id"))

		token := getSessionToken(r)

		// For each manually-mapped feed location, persist the feed name as an
		// alias on the DB location so future imports auto-match without manual
		// intervention.
		for feedName, dbLoc := range feedLocOverride {
			if feedName == dbLoc.Location {
				continue
			}
			already := false
			for _, a := range dbLoc.Aliases {
				if a == feedName {
					already = true
					break
				}
			}
			if already {
				continue
			}
			patched := dbLoc
			patched.Aliases = append(append([]string{}, dbLoc.Aliases...), feedName)
			client.UpdateLocation(r.Context(), dbLoc.ID, patched, token)
		}

		// For each newly-picked category mapping, persist it as a
		// category_alias so future imports of this (or any) feed using the
		// same raw category text auto-resolve without manual intervention.
		for feedCat, slug := range catOverride {
			if feedCat == slug {
				continue
			}
			client.CreateCategoryAlias(r.Context(), feedCat, slug, token)
		}

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
			if orgID > 0 || len(feedLocOverride) > 0 || len(catOverride) > 0 {
				var ev map[string]any
				if err := json.Unmarshal([]byte(raw), &ev); err == nil {
					if orgID > 0 {
						ev["organization_id"] = orgID
					}
					if len(catOverride) > 0 {
						if tagsField, ok := ev["tags"].([]any); ok {
							newTags := make([]any, 0, len(tagsField))
							seenSlug := map[string]bool{}
							for _, tv := range tagsField {
								t, _ := tv.(string)
								slug, mapped := catOverride[t]
								// Unmapped feed categories (admin left the picker on
								// "ignore") are dropped, mirroring resolveFeedTags'
								// behaviour for feed taxonomy with no known match (#923).
								if !mapped || slug == "" || seenSlug[slug] {
									continue
								}
								seenSlug[slug] = true
								newTags = append(newTags, slug)
							}
							ev["tags"] = newTags
						}
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
			if created, err := client.CreateEventBatch(r.Context(), selected, token); err == nil {
				var ids []int
				for _, e := range created {
					if e.IsPublished {
						ids = append(ids, e.ID)
					}
				}
				go notifyIndexNow(cfg.publicBaseURL(), siteCfg.IndexNowKey(), ids)
			}
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
			// #1084: carry the template picked on the import preview page
			// through so it's pre-selected on the "create fetchurl" step,
			// where it's actually stored (on the new fetch source).
			if tplID, err := strconv.Atoi(r.FormValue("template_id")); err == nil && tplID > 0 {
				q.Set("template_id", strconv.Itoa(tplID))
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
