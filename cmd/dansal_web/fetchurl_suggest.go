package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// FetchSuggestPageData is the template data for the public "suggest a feed"
// wizard (#1333 phase 1) — one page/template covering all three steps (blank
// form, preview + location mapping, and re-displaying either on error),
// mirroring SuggestPageData's own single-template-multiple-states shape.
type FetchSuggestPageData struct {
	Error     string
	FormToken string
	Orgs      []Organization

	// Fields carried through from the previous step so the form doesn't lose
	// what the visitor already typed.
	Email           string
	OrgChoice       string // "existing" | "new"
	OrgID           int
	OrgName         string
	OrgActorName    string
	OrgDescription  string
	OrgWebsite      string
	OrgContactEmail string
	FeedURL         string
	FeedType        string

	// Set once the preview step succeeds; presence of PreviewEvents drives
	// the template into showing the location-mapping + confirm section.
	PreviewEvents  []PreviewEvent
	EventCount     int
	UniqueFeedLocs []FeedLocation
	Locations      []Location
}

func fetchSuggestPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		orgs, _ := client.GetOrganizations(r.Context())
		sort.Slice(orgs, func(i, j int) bool { return strings.ToLower(orgs[i].Name) < strings.ToLower(orgs[j].Name) })
		title := i18n.T(r, "fetch_suggest_title")
		renderTemplate(w, tmpls.suggestFetch, tmplData(r, cfg, i18n, title, FetchSuggestPageData{
			Orgs:      orgs,
			OrgChoice: "existing",
			FormToken: issueFormToken(ip),
		}))
	}
}

// fetchSuggestFormFields reads the org/email/feed fields shared by every
// re-render of the wizard (error re-display, preview result, confirm step).
type fetchSuggestFormFields struct {
	Email           string
	OrgChoice       string
	OrgID           int
	OrgName         string
	OrgActorName    string
	OrgDescription  string
	OrgWebsite      string
	OrgContactEmail string
	FeedURL         string
	FeedType        string
}

func readFetchSuggestFormFields(r *http.Request) fetchSuggestFormFields {
	orgID, _ := strconv.Atoi(r.FormValue("org_id"))
	return fetchSuggestFormFields{
		Email:           strings.TrimSpace(r.FormValue("email")),
		OrgChoice:       r.FormValue("org_choice"),
		OrgID:           orgID,
		OrgName:         strings.TrimSpace(r.FormValue("org_name")),
		OrgActorName:    strings.TrimSpace(r.FormValue("org_actor_name")),
		OrgDescription:  strings.TrimSpace(r.FormValue("org_description")),
		OrgWebsite:      strings.TrimSpace(r.FormValue("org_website")),
		OrgContactEmail: strings.TrimSpace(r.FormValue("org_contact_email")),
		FeedURL:         strings.TrimSpace(r.FormValue("feed_url")),
		FeedType:        r.FormValue("feed_type"),
	}
}

// buildUniqueFeedLocs auto-matches each unique feed location name against the
// DB's locations (and their aliases) — the exact same locByName-with-aliases
// approach adminImportEventsHandler already uses, just driven by the
// anonymous submitter here instead of an authenticated admin (#1333 phase 1).
func buildUniqueFeedLocs(events []PreviewEvent, locs []Location) []FeedLocation {
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
	var uniq []FeedLocation
	for _, e := range events {
		name := e.Location.Location
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		display := name
		if e.Location.Town != "" {
			display += ", " + e.Location.Town
		}
		if e.Location.Country != "" {
			display += " " + e.Location.Country
		}
		fl := FeedLocation{Idx: len(uniq), Name: name, Display: display}
		if dbLoc, ok := locByName[name]; ok {
			fl.MatchedDBLocID = dbLoc.ID
			fl.MatchedDBLocName = locationDisplayName(dbLoc)
		}
		uniq = append(uniq, fl)
	}
	return uniq
}

func fetchSuggestPreviewPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)

		renderErr := func(msg string, fields fetchSuggestFormFields) {
			orgs, _ := client.GetOrganizations(r.Context())
			sort.Slice(orgs, func(i, j int) bool { return strings.ToLower(orgs[i].Name) < strings.ToLower(orgs[j].Name) })
			title := i18n.T(r, "fetch_suggest_title")
			renderTemplate(w, tmpls.suggestFetch, tmplData(r, cfg, i18n, title, FetchSuggestPageData{
				Error:           msg,
				Orgs:            orgs,
				FormToken:       issueFormToken(ip),
				Email:           fields.Email,
				OrgChoice:       fields.OrgChoice,
				OrgID:           fields.OrgID,
				OrgName:         fields.OrgName,
				OrgActorName:    fields.OrgActorName,
				OrgDescription:  fields.OrgDescription,
				OrgWebsite:      fields.OrgWebsite,
				OrgContactEmail: fields.OrgContactEmail,
				FeedURL:         fields.FeedURL,
				FeedType:        fields.FeedType,
			}))
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fields := readFetchSuggestFormFields(r)

		key := ip + "|" + r.UserAgent()
		if publicThrottle.isBlocked(key) {
			renderErr(i18n.T(r, "suggest_error_rate_limit"), fields)
			return
		}
		publicThrottle.record(key)

		if fields.FeedURL == "" {
			renderErr(i18n.T(r, "fetch_suggest_error_url_required"), fields)
			return
		}

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("url", fields.FeedURL)
		if fields.FeedType != "" {
			mw.WriteField("type", fields.FeedType)
		}
		mw.Close()

		events, err := client.SuggestFetchPreview(r.Context(), &buf, mw.FormDataContentType())
		if err != nil {
			msg := apiErrUserMessage(err)
			if msg == "" {
				msg = i18n.T(r, "suggest_error_parse")
			}
			renderErr(msg, fields)
			return
		}

		locs, _ := client.GetLocations(r.Context())
		orgs, _ := client.GetOrganizations(r.Context())
		sort.Slice(orgs, func(i, j int) bool { return strings.ToLower(orgs[i].Name) < strings.ToLower(orgs[j].Name) })
		sort.Slice(locs, func(i, j int) bool {
			return strings.ToLower(locationDisplayName(locs[i])) < strings.ToLower(locationDisplayName(locs[j]))
		})

		eventCount := len(events)
		sampleEvents := events
		const maxSample = 10
		if len(sampleEvents) > maxSample {
			sampleEvents = sampleEvents[:maxSample]
		}

		title := i18n.T(r, "fetch_suggest_title")
		renderTemplate(w, tmpls.suggestFetch, tmplData(r, cfg, i18n, title, FetchSuggestPageData{
			Orgs:            orgs,
			Locations:       locs,
			FormToken:       issueFormToken(ip),
			Email:           fields.Email,
			OrgChoice:       fields.OrgChoice,
			OrgID:           fields.OrgID,
			OrgName:         fields.OrgName,
			OrgActorName:    fields.OrgActorName,
			OrgDescription:  fields.OrgDescription,
			OrgWebsite:      fields.OrgWebsite,
			OrgContactEmail: fields.OrgContactEmail,
			FeedURL:         fields.FeedURL,
			FeedType:        fields.FeedType,
			PreviewEvents:   sampleEvents,
			EventCount:      eventCount,
			UniqueFeedLocs:  buildUniqueFeedLocs(events, locs),
		}))
	}
}

func fetchSuggestSubmitPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)

		renderErr := func(msg string, fields fetchSuggestFormFields) {
			orgs, _ := client.GetOrganizations(r.Context())
			sort.Slice(orgs, func(i, j int) bool { return strings.ToLower(orgs[i].Name) < strings.ToLower(orgs[j].Name) })
			title := i18n.T(r, "fetch_suggest_title")
			renderTemplate(w, tmpls.suggestFetch, tmplData(r, cfg, i18n, title, FetchSuggestPageData{
				Error:           msg,
				Orgs:            orgs,
				FormToken:       issueFormToken(ip),
				Email:           fields.Email,
				OrgChoice:       fields.OrgChoice,
				OrgID:           fields.OrgID,
				OrgName:         fields.OrgName,
				OrgActorName:    fields.OrgActorName,
				OrgDescription:  fields.OrgDescription,
				OrgWebsite:      fields.OrgWebsite,
				OrgContactEmail: fields.OrgContactEmail,
				FeedURL:         fields.FeedURL,
				FeedType:        fields.FeedType,
			}))
		}

		key := ip + "|" + r.UserAgent()
		if publicThrottle.isBlocked(key) {
			renderErr(i18n.T(r, "suggest_error_rate_limit"), fetchSuggestFormFields{})
			return
		}

		switch guardFormSubmit(w, r, cfg, ip) {
		case formGuardParseError:
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		case formGuardHoneypot, formGuardBadToken:
			// Silent "success" so a bot can't tell its submission was
			// dropped — same choice suggestSubmitHandler makes.
			http.Redirect(w, r, "/feeds/suggest/done", http.StatusSeeOther)
			return
		}
		publicThrottle.record(key)

		fields := readFetchSuggestFormFields(r)
		if fields.Email == "" {
			renderErr(i18n.T(r, "fetch_suggest_error_email_required"), fields)
			return
		}
		if fields.FeedURL == "" {
			renderErr(i18n.T(r, "fetch_suggest_error_url_required"), fields)
			return
		}

		req := FetchSuggestionReq{
			Email:           fields.Email,
			Phone2:          r.FormValue(honeypotField),
			FeedURL:         fields.FeedURL,
			FeedType:        fields.FeedType,
			OrgActorName:    fields.OrgActorName,
			OrgDescription:  fields.OrgDescription,
			OrgWebsite:      fields.OrgWebsite,
			OrgContactEmail: fields.OrgContactEmail,
		}
		if fields.OrgChoice == "new" {
			req.OrgName = fields.OrgName
			if req.OrgName == "" {
				renderErr(i18n.T(r, "fetch_suggest_error_org_required"), fields)
				return
			}
		} else {
			if fields.OrgID == 0 {
				renderErr(i18n.T(r, "fetch_suggest_error_org_required"), fields)
				return
			}
			orgID := fields.OrgID
			req.OrgID = &orgID
		}

		// loc_feed_N / loc_map_N mirror adminImportConfirmHandler's own
		// naming convention; loc_new_*_N carries manually-entered details
		// for a feed location the submitter chose not to map to an existing
		// one (#1333: "locations can be extended with needed info").
		for i := 0; ; i++ {
			feedName := r.FormValue("loc_feed_" + strconv.Itoa(i))
			if feedName == "" {
				break
			}
			mapping := FetchSuggestionLocationMapping{FeedName: feedName}
			if dbLocIDStr := r.FormValue("loc_map_" + strconv.Itoa(i)); dbLocIDStr != "" {
				if dbLocID, err := strconv.Atoi(dbLocIDStr); err == nil && dbLocID > 0 {
					mapping.MatchedLocationID = &dbLocID
				}
			}
			if mapping.MatchedLocationID == nil {
				newName := strings.TrimSpace(r.FormValue("loc_new_name_" + strconv.Itoa(i)))
				if newName != "" {
					mapping.NewLocation = &FetchSuggestionNewLocation{
						Location: newName,
						Address:  strings.TrimSpace(r.FormValue("loc_new_address_" + strconv.Itoa(i))),
						Zipcode:  strings.TrimSpace(r.FormValue("loc_new_zipcode_" + strconv.Itoa(i))),
						Town:     strings.TrimSpace(r.FormValue("loc_new_town_" + strconv.Itoa(i))),
						Country:  strings.TrimSpace(r.FormValue("loc_new_country_" + strconv.Itoa(i))),
					}
				}
			}
			req.LocationMappings = append(req.LocationMappings, mapping)
		}

		if err := client.SubmitFetchSuggestion(r.Context(), req); err != nil {
			msg := apiErrUserMessage(err)
			if msg == "" {
				msg = i18n.T(r, "suggest_error_parse")
			}
			renderErr(msg, fields)
			return
		}

		http.Redirect(w, r, "/feeds/suggest/done", http.StatusSeeOther)
	}
}

func fetchSuggestDoneHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := i18n.T(r, "fetch_suggest_done_title")
		renderTemplate(w, tmpls.suggestFetchDone, tmplData(r, cfg, i18n, title, nil))
	}
}
