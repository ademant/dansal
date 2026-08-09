package main

import (
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

type SuggestPageData struct {
	HintSMTP       bool // SMTP configured → show email verification hint
	PreviewEvents  []PreviewEvent
	PreviewJSON    []string
	Error          string
	CaptchaSiteKey string
	GroupedTags    []TagGroup
	FormToken      string
	Dances         []Dance
	// ManageToken/PrefillJSON/PrefillTags/PrefillDanceIDs are set when the
	// wizard is loaded via the #928 magic link (/events/suggest/manage/{token}),
	// pre-filling the same form instead of a separate simpler edit page.
	ManageToken       string
	PrefillJSON       template.JS
	PrefillTags       map[string]bool // set of tags to pre-check in the template
	PrefillDanceIDs   map[int]bool    // set of dance IDs to pre-check in the template
	ExistingImageURL  string          // current event image URL, shown as preview in manage mode
	IsImportMode      bool            // true when returning from import: wizard is pre-filled
	ImportAllPrefills []string        // each imported event as wizPrefill JSON (for picker)
	// CanUploadImageNow is true when the submitter is logged in (#1050): the
	// image can be attached on the initial submit via the standing manage token
	// instead of only after email verification.
	CanUploadImageNow bool
}

type SuggestDoneData struct {
	NeedsReview bool
}
type SuggestVerifiedData struct {
	Error string
}

// suggestCanUploadImage reports whether the current visitor may attach an image
// on the initial suggest submit (#1050): only authenticated users, since the
// anonymous flow has no verified identity to attribute the upload to. The image
// itself is always uploaded through the standing manage token, which is only
// returned in the API response the server can see.
func suggestCanUploadImage(r *http.Request) bool {
	return getSessionUser(r) != nil
}

func suggestPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		if tokenThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/events/suggest", tokenBlock, ip)
			http.Error(w, i18n.T(r, "form_token_cap_error"), http.StatusTooManyRequests)
			return
		}
		applyEmailBackpressure(r.Context(), globalEmailSendRate, w)
		if r.Context().Err() != nil {
			return
		}
		tok := issueFormToken(ip)
		if tok == "" {
			http.Error(w, i18n.T(r, "form_token_cap_error"), http.StatusServiceUnavailable)
			return
		}
		tokenThrottle.record(ip)
		dances, err := client.GetDances(r.Context())
		if err != nil {
			log.Printf("suggest: could not load dances: %v", err)
		}
		title := i18n.T(r, "suggest_event_title")
		renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
			HintSMTP:          cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
			CaptchaSiteKey:    cfg.CaptchaSiteKey,
			FormToken:         tok,
			Dances:            dances,
			CanUploadImageNow: suggestCanUploadImage(r),
		}))
	}
}

func suggestPreviewHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		key := ip + "|" + r.UserAgent()
		if publicThrottle.isBlocked(key) {
			log.Printf("%s ip=%s path=%s", publicBlock, ip, r.URL.Path)
			title := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
				HintSMTP:          cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
				CanUploadImageNow: suggestCanUploadImage(r),
				Error:             i18n.T(r, "suggest_error_rate_limit"),
				FormToken:         issueFormToken(ip),
			}))
			return
		}
		publicThrottle.record(key)

		events, err := client.SuggestEventPreview(r.Context(), r.Body, r.Header.Get("Content-Type"))
		if err != nil {
			title := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
				HintSMTP:          cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
				CanUploadImageNow: suggestCanUploadImage(r),
				Error:             i18n.T(r, "suggest_error_parse"),
				FormToken:         issueFormToken(ip),
			}))
			return
		}

		// Convert each parsed event to wizPrefill format for the wizard.
		prefills := make([]wizPrefill, len(events))
		for i, e := range events {
			pf := wizPrefill{
				Title:       e.Title,
				Description: e.Description,
				URL:         e.URL,
				StartTime:   e.StartTime,
				EndTime:     e.EndTime,
				Tags:        e.Tags,
				Location:    e.Location.Location,
				Town:        e.Location.Town,
				Country:     e.Location.Country,
				Address:     e.Location.Address,
				Zipcode:     e.Location.Zipcode,
				Pricing:     e.Pricing,
			}
			if e.Location.Latitude != nil {
				pf.Lat = strconv.FormatFloat(*e.Location.Latitude, 'f', 7, 64)
			}
			if e.Location.Longitude != nil {
				pf.Lon = strconv.FormatFloat(*e.Location.Longitude, 'f', 7, 64)
			}
			prefills[i] = pf
		}

		importAllPrefills := make([]string, len(prefills))
		for i, pf := range prefills {
			if b, err := json.Marshal(pf); err == nil {
				importAllPrefills[i] = string(b)
			} else {
				log.Printf("could not marshal import prefill: %v", err)
			}
		}

		var prefillJSON template.JS
		prefillTags := make(map[string]bool)
		if len(prefills) > 0 {
			if b, err := json.Marshal(prefills[0]); err == nil {
				prefillJSON = template.JS(b)
			} else {
				log.Printf("could not marshal prefill JSON: %v", err)
			}
			for _, t := range prefills[0].Tags {
				prefillTags[t] = true
			}
		}

		dances, err := client.GetDances(r.Context())
		if err != nil {
			log.Printf("suggest: could not load dances: %v", err)
		}
		title := i18n.T(r, "suggest_event_title")
		renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
			HintSMTP:          cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
			CanUploadImageNow: suggestCanUploadImage(r),
			PreviewEvents:     events,
			CaptchaSiteKey:    cfg.CaptchaSiteKey,
			FormToken:         issueFormToken(ip),
			Dances:            dances,
			IsImportMode:      true,
			PrefillJSON:       prefillJSON,
			PrefillTags:       prefillTags,
			ImportAllPrefills: importAllPrefills,
		}))
	}
}

// trimmedNonEmpty trims each string in vals and drops any that become empty.
func trimmedNonEmpty(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// suggestError re-renders the suggest form with an error message. Shared by
// the throttle, pending, captcha, link, and submit-failure paths — each used
// to repeat the same 8-line template block.
func suggestError(w http.ResponseWriter, r *http.Request, cfg *Config, tmpls *Templates, i18n *I18n, errMsg, ip string) {
	title := i18n.T(r, "suggest_event_title")
	renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
		HintSMTP:          cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
		CanUploadImageNow: suggestCanUploadImage(r),
		Error:             errMsg,
		FormToken:         issueFormToken(ip),
	}))
}

func suggestSubmitHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		key := ip + "|" + r.UserAgent()
		if publicThrottle.isBlocked(key) {
			log.Printf("%s ip=%s path=%s", publicBlock, ip, r.URL.Path)
			suggestError(w, r, cfg, tmpls, i18n, i18n.T(r, "suggest_error_rate_limit"), ip)
			return
		}

		switch guardFormSubmit(w, r, cfg, ip) {
		case formGuardParseError:
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		case formGuardHoneypot, formGuardBadToken:
			w.WriteHeader(http.StatusAccepted)
			return
		}

		if hasPendingSubmission(ip, r.UserAgent()) {
			suggestError(w, r, cfg, tmpls, i18n, i18n.T(r, "suggest_error_rate_limit"), ip)
			return
		}

		// Captcha check.
		if cfg.CaptchaSiteKey != "" {
			if err := verifyTurnstile(cfg, r.FormValue("cf-turnstile-response")); err != nil {
				suggestError(w, r, cfg, tmpls, i18n, i18n.T(r, "suggest_error_captcha"), ip)
				return
			}
		}

		title := r.FormValue("dansal_title")
		description := r.FormValue("description")

		if strings.Contains(title, "http://") || strings.Contains(title, "https://") ||
			strings.Contains(description, "http://") || strings.Contains(description, "https://") {
			suggestError(w, r, cfg, tmpls, i18n, i18n.T(r, "suggest_error_links"), ip)
			return
		}

		tags := r.Form["dansal_tags"]
		var danceIDs []int
		for _, s := range r.Form["dance_ids"] {
			if id, err := strconv.Atoi(s); err == nil {
				danceIDs = append(danceIDs, id)
			}
		}

		var pricing *Pricing
		if pt := r.FormValue("pricing_type"); pt != "" && pt != "none" {
			p := &Pricing{Type: pt}
			switch pt {
			case "single", "donation":
				if amt := r.FormValue("pricing_amount"); amt != "" {
					if f, err := strconv.ParseFloat(amt, 64); err == nil {
						p.Amount = f
					}
				}
				p.Currency = strings.TrimSpace(r.FormValue("pricing_currency"))
			case "multiple":
				labels := r.Form["pl_label"]
				amounts := r.Form["pl_amount"]
				for i, lbl := range labels {
					lbl = strings.TrimSpace(lbl)
					if lbl == "" {
						continue
					}
					var amt float64
					if i < len(amounts) {
						if f, err := strconv.ParseFloat(strings.TrimSpace(amounts[i]), 64); err == nil {
							amt = f
						}
					}
					p.Prices = append(p.Prices, Price{Label: lbl, Amount: amt})
				}
				if len(p.Prices) == 0 {
					p = nil
				}
			}
			pricing = p
		}

		musicians := trimmedNonEmpty(r.Form["dansal_musicians"])
		instructors := trimmedNonEmpty(r.Form["dansal_instructors"])

		starts := r.Form["tt_start"]
		ends := r.Form["tt_end"]
		titles := r.Form["tt_title"]
		descs := r.Form["tt_desc"]
		rooms := r.Form["tt_room"]
		ttTypes := r.Form["tt_type"]
		var timetable []TimetableEntryReq
		for i, s := range starts {
			s = strings.TrimSpace(s)
			if i >= len(titles) {
				break
			}
			t := strings.TrimSpace(titles[i])
			if s == "" && t == "" {
				continue
			}
			entry := TimetableEntryReq{StartTime: s, Title: t}
			if i < len(ends) {
				entry.EndTime = strings.TrimSpace(ends[i])
			}
			if i < len(descs) {
				entry.Description = strings.TrimSpace(descs[i])
			}
			if i < len(rooms) {
				entry.Room = strings.TrimSpace(rooms[i])
			}
			if i < len(ttTypes) {
				entry.EntryType = ttTypes[i]
			}
			timetable = append(timetable, entry)
		}

		req := SuggestEventReq{
			Title:       title,
			Description: description,
			StartTime:   r.FormValue("start_time"),
			EndTime:     r.FormValue("end_time"),
			HasBall:     sliceContains(tags, "bal-folk"),
			HasWorkshop: sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop"),
			HasFestival: sliceContains(tags, "festival"),
			Tags:        tags,
			DanceIDs:    danceIDs,
			URL:         r.FormValue("url"),
			Food:        r.FormValue("food"),
			Drink:       r.FormValue("drink"),
			Location: PreviewLoc{
				Location:  r.FormValue("location"),
				Town:      r.FormValue("town"),
				Country:   r.FormValue("country"),
				Address:   r.FormValue("address"),
				Zipcode:   r.FormValue("zipcode"),
				Latitude:  parseLatLng(r.FormValue("lat")),
				Longitude: parseLatLng(r.FormValue("lon")),
				OsmID:     parseOsmID(r.FormValue("osm_id")),
				OsmType:   r.FormValue("osm_type"),
			},
			Email:         r.FormValue("email"),
			SuggesterName: strings.TrimSpace(r.FormValue("suggester_name")),
			Phone2:        r.FormValue("dansal_phone2"),
			Pricing:       pricing,
			ContactName:   strings.TrimSpace(r.FormValue("contact_name")),
			ContactEmail:  strings.TrimSpace(r.FormValue("contact_email")),
			Musicians:     musicians,
			Instructors:   instructors,
			Timetable:     timetable,
		}

		publicThrottle.record(key)
		setPendingSubmission(ip, r.UserAgent(), stdFormMaxAge(cfg))
		globalEmailSendRate.record()

		token, err := client.SuggestEvent(r.Context(), req, cfg.publicBaseURL(), getBoardSessionToken(r))
		if err != nil {
			clearPendingSubmission(ip, r.UserAgent())
			suggestError(w, r, cfg, tmpls, i18n, i18n.T(r, "suggest_error_submit"), ip)
			return
		}

		// #1050: an authenticated submitter attaches the image right away via
		// the standing manage token returned by the suggest API. Errors are
		// logged but never block the redirect.
		if suggestCanUploadImage(r) && token != "" {
			if file, header, ferr := r.FormFile("image"); ferr == nil {
				defer file.Close()
				if data, rerr := io.ReadAll(file); rerr == nil {
					if uerr := client.UploadSuggestManageImage(r.Context(), token, data, header.Filename); uerr != nil {
						log.Printf("suggest: upload image: %v", uerr)
					}
				}
			}
		}

		http.Redirect(w, r, "/events/suggest/done", http.StatusSeeOther)
	}
}

func suggestDoneHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := i18n.T(r, "suggest_done_title")
		renderTemplate(w, tmpls.suggestDone, tmplData(r, cfg, i18n, title, SuggestDoneData{
			NeedsReview: r.URL.Query().Get("review") == "1",
		}))
	}
}

// wizPrefill is the JSON shape embedded into the page for the wizard's
// client-side prefill script (#928 magic-link edit).
type wizPrefill struct {
	Title        string              `json:"title"`
	Description  string              `json:"description"`
	URL          string              `json:"url"`
	StartTime    string              `json:"start_time"`
	EndTime      string              `json:"end_time"`
	Tags         []string            `json:"tags"`
	DanceIDs     []int               `json:"dance_ids,omitempty"`
	Location     string              `json:"location"`
	Town         string              `json:"town"`
	Country      string              `json:"country"`
	Address      string              `json:"address"`
	Zipcode      string              `json:"zipcode"`
	Lat          string              `json:"lat,omitempty"`
	Lon          string              `json:"lon,omitempty"`
	Food         string              `json:"food"`
	Drink        string              `json:"drink"`
	Pricing      *Pricing            `json:"pricing,omitempty"`
	ContactName  string              `json:"contact_name"`
	ContactEmail string              `json:"contact_email"`
	Musicians    []string            `json:"musicians"`
	Instructors  []string            `json:"instructors"`
	Timetable    []TimetableEntryReq `json:"timetable,omitempty"`
}

func suggestManagePageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		token := r.PathValue("token")
		ev, err := client.GetSuggestManageEvent(r.Context(), token)
		if err != nil {
			title := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
				Error: i18n.T(r, "suggest_error_token"),
			}))
			return
		}

		ip := getClientIP(r)
		dances, err := client.GetDances(r.Context())
		if err != nil {
			log.Printf("suggest: could not load dances: %v", err)
		}

		// Build a name→ID map so we can pre-check dance checkboxes both
		// server-side (PrefillDanceIDs) and via the JS wizard (pf.DanceIDs).
		// The event API only returns dance names; the form uses IDs.
		prefillDanceIDs := make(map[int]bool)
		if len(ev.DanceNames) > 0 {
			nameToID := make(map[string]int, len(dances))
			for _, d := range dances {
				nameToID[d.Name] = d.ID
			}
			for _, name := range ev.DanceNames {
				if id, ok := nameToID[name]; ok {
					prefillDanceIDs[id] = true
				}
			}
		}

		pf := wizPrefill{
			Title: ev.Title, Description: ev.Description, URL: ev.URL,
			StartTime: ev.StartTime, EndTime: ev.EndTime, Tags: ev.Tags,
			Food: ev.Food, Drink: ev.Drink, Pricing: ev.Pricing,
			ContactName: ev.ContactName, ContactEmail: ev.ContactEmail,
		}
		if ev.Location != nil {
			pf.Location = ev.Location.Location
			pf.Town = ev.Location.Town
			pf.Country = ev.Location.Country
			pf.Address = ev.Location.Address
			pf.Zipcode = ev.Location.Zipcode
		}
		for _, m := range ev.Musicians {
			pf.Musicians = append(pf.Musicians, m.Bandname)
		}
		for _, ins := range ev.Instructors {
			pf.Instructors = append(pf.Instructors, ins.Name)
		}
		for _, te := range ev.Timetable {
			pf.Timetable = append(pf.Timetable, TimetableEntryReq{
				StartTime:   te.StartTime,
				EndTime:     te.EndTime,
				Title:       te.Title,
				Description: te.Description,
				Room:        te.Room,
				EntryType:   te.EntryType,
			})
		}
		for id := range prefillDanceIDs {
			pf.DanceIDs = append(pf.DanceIDs, id)
		}
		b, err := json.Marshal(pf)
		if err != nil {
			log.Printf("could not marshal manage prefill: %v", err)
			b = []byte("{}")
		}
		prefillTags := make(map[string]bool, len(pf.Tags))
		for _, t := range pf.Tags {
			prefillTags[t] = true
		}

		title := i18n.T(r, "suggest_event_title")
		renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
			HintSMTP:          cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
			CanUploadImageNow: suggestCanUploadImage(r),
			FormToken:         issueFormToken(ip),
			Dances:            dances,
			ManageToken:       token,
			PrefillJSON:       template.JS(b),
			PrefillTags:       prefillTags,
			PrefillDanceIDs:   prefillDanceIDs,
			ExistingImageURL:  ev.ImageURL,
		}))
	}
}

func suggestManageSubmitHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		token := r.PathValue("token")
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if r.FormValue("dansal_phone2") != "" {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		tags := r.Form["dansal_tags"]
		var danceIDs []int
		for _, s := range r.Form["dance_ids"] {
			if id, err := strconv.Atoi(s); err == nil {
				danceIDs = append(danceIDs, id)
			}
		}
		musicians := trimmedNonEmpty(r.Form["dansal_musicians"])
		instructors := trimmedNonEmpty(r.Form["dansal_instructors"])

		starts := r.Form["tt_start"]
		ends := r.Form["tt_end"]
		ttTitles := r.Form["tt_title"]
		descs := r.Form["tt_desc"]
		rooms := r.Form["tt_room"]
		ttTypes := r.Form["tt_type"]
		var timetable []TimetableEntryReq
		for i, s := range starts {
			s = strings.TrimSpace(s)
			if i >= len(ttTitles) {
				break
			}
			t := strings.TrimSpace(ttTitles[i])
			if s == "" && t == "" {
				continue
			}
			entry := TimetableEntryReq{StartTime: s, Title: t}
			if i < len(ends) {
				entry.EndTime = strings.TrimSpace(ends[i])
			}
			if i < len(descs) {
				entry.Description = strings.TrimSpace(descs[i])
			}
			if i < len(rooms) {
				entry.Room = strings.TrimSpace(rooms[i])
			}
			if i < len(ttTypes) {
				entry.EntryType = ttTypes[i]
			}
			timetable = append(timetable, entry)
		}

		var pricing *Pricing
		if pt := r.FormValue("pricing_type"); pt != "" && pt != "none" {
			p := &Pricing{Type: pt}
			switch pt {
			case "single", "donation":
				if amt := r.FormValue("pricing_amount"); amt != "" {
					if f, err2 := strconv.ParseFloat(amt, 64); err2 == nil {
						p.Amount = f
					}
				}
				p.Currency = strings.TrimSpace(r.FormValue("pricing_currency"))
			case "multiple":
				for i, lbl := range r.Form["pl_label"] {
					lbl = strings.TrimSpace(lbl)
					if lbl == "" {
						continue
					}
					var amt float64
					if i < len(r.Form["pl_amount"]) {
						if f, err2 := strconv.ParseFloat(strings.TrimSpace(r.Form["pl_amount"][i]), 64); err2 == nil {
							amt = f
						}
					}
					p.Prices = append(p.Prices, Price{Label: lbl, Amount: amt})
				}
				if len(p.Prices) == 0 {
					p = nil
				}
			}
			pricing = p
		}

		req := SuggestEventReq{
			Title:       r.FormValue("dansal_title"),
			Description: r.FormValue("description"),
			StartTime:   r.FormValue("start_time"),
			EndTime:     r.FormValue("end_time"),
			HasBall:     sliceContains(tags, "bal-folk"),
			HasWorkshop: sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop"),
			HasFestival: sliceContains(tags, "festival"),
			Tags:        tags,
			DanceIDs:    danceIDs,
			URL:         r.FormValue("url"),
			Food:        r.FormValue("food"),
			Drink:       r.FormValue("drink"),
			Pricing:     pricing,
			Location: PreviewLoc{
				Location: r.FormValue("location"),
				Town:     r.FormValue("town"),
				Country:  r.FormValue("country"),
				Address:  r.FormValue("address"),
				Zipcode:  r.FormValue("zipcode"),
			},
			ContactName:  strings.TrimSpace(r.FormValue("contact_name")),
			ContactEmail: strings.TrimSpace(r.FormValue("contact_email")),
			Musicians:    musicians,
			Instructors:  instructors,
			Timetable:    timetable,
		}

		needsReview, err := client.PatchSuggestManageEvent(r.Context(), token, req)
		if err != nil {
			title := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
				Error:       i18n.T(r, "suggest_error_submit"),
				ManageToken: token,
			}))
			return
		}
		if file, header, ferr := r.FormFile("image"); ferr == nil {
			defer file.Close()
			if data, rerr := io.ReadAll(file); rerr == nil {
				if uerr := client.UploadSuggestManageImage(r.Context(), token, data, header.Filename); uerr != nil {
					log.Printf("suggest manage: upload image: %v", uerr)
				}
			}
		}
		dest := "/events/suggest/done"
		if needsReview {
			dest += "?review=1"
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
	}
}

func suggestVerifyHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		errKey := ""
		if err := client.VerifySuggestion(r.Context(), token); err != nil {
			errKey = "suggest_error_token"
		}
		title := i18n.T(r, "suggest_verified_title")
		renderTemplate(w, tmpls.suggestVerified, tmplData(r, cfg, i18n, title, SuggestVerifiedData{Error: errKey}))
	}
}
