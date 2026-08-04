package main

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	// ManageToken/PrefillJSON are set when the wizard is loaded via the
	// #928 magic link (/events/suggest/manage/{token}), pre-filling the
	// same form instead of a separate simpler edit page.
	ManageToken string
	PrefillJSON template.JS
}

type SuggestDoneData struct {
	NeedsReview bool
}
type SuggestVerifiedData struct {
	Error string
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
		dances, _ := client.GetDances(r.Context())
		title := i18n.T(r, "suggest_event_title")
		renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
			HintSMTP:       cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
			CaptchaSiteKey: cfg.CaptchaSiteKey,
			FormToken:      tok,
			Dances:         dances,
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
				HintSMTP:  cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
				Error:     i18n.T(r, "suggest_error_rate_limit"),
				FormToken: issueFormToken(ip),
			}))
			return
		}
		publicThrottle.record(key)

		events, err := client.SuggestEventPreview(r.Context(), r.Body, r.Header.Get("Content-Type"))
		if err != nil {
			title := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
				HintSMTP:  cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
				Error:     i18n.T(r, "suggest_error_parse"),
				FormToken: issueFormToken(ip),
			}))
			return
		}

		previewJSON := make([]string, len(events))
		for i, e := range events {
			b, _ := json.Marshal(e)
			previewJSON[i] = string(b)
		}

		dances, _ := client.GetDances(r.Context())
		title := i18n.T(r, "suggest_event_title")
		renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
			HintSMTP:       cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
			PreviewEvents:  events,
			PreviewJSON:    previewJSON,
			CaptchaSiteKey: cfg.CaptchaSiteKey,
			FormToken:      issueFormToken(ip),
			Dances:         dances,
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
			title := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
				HintSMTP:  cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
				Error:     i18n.T(r, "suggest_error_rate_limit"),
				FormToken: issueFormToken(ip),
			}))
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if r.FormValue("dansal_phone2") != "" || !consumeFormToken(r.FormValue("_form_token"), ip, time.Second, stdFormMaxAge(cfg), cfg.FormTokenBindIP) {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		if hasPendingSubmission(ip, r.UserAgent()) {
			pageTitle := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, pageTitle, SuggestPageData{
				HintSMTP:  cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
				Error:     i18n.T(r, "suggest_error_rate_limit"),
				FormToken: issueFormToken(ip),
			}))
			return
		}

		// Captcha check.
		if cfg.CaptchaSiteKey != "" {
			if err := verifyTurnstile(cfg, r.FormValue("cf-turnstile-response")); err != nil {
				title := i18n.T(r, "suggest_event_title")
				renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
					HintSMTP:  cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
					Error:     i18n.T(r, "suggest_error_captcha"),
					FormToken: issueFormToken(ip),
				}))
				return
			}
		}

		title := r.FormValue("dansal_title")
		description := r.FormValue("description")

		if strings.Contains(title, "http://") || strings.Contains(title, "https://") ||
			strings.Contains(description, "http://") || strings.Contains(description, "https://") {
			pageTitle := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, pageTitle, SuggestPageData{
				HintSMTP:  cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
				Error:     i18n.T(r, "suggest_error_links"),
				FormToken: issueFormToken(ip),
			}))
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

		if err := client.SuggestEvent(r.Context(), req, cfg.publicBaseURL()); err != nil {
			clearPendingSubmission(ip, r.UserAgent())
			pageTitle := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, pageTitle, SuggestPageData{
				HintSMTP:  cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
				Error:     i18n.T(r, "suggest_error_submit"),
				FormToken: issueFormToken(ip),
			}))
			return
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
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	URL          string   `json:"url"`
	StartTime    string   `json:"start_time"`
	EndTime      string   `json:"end_time"`
	Tags         []string `json:"tags"`
	Location     string   `json:"location"`
	Town         string   `json:"town"`
	Country      string   `json:"country"`
	Address      string   `json:"address"`
	Zipcode      string   `json:"zipcode"`
	Lat          string   `json:"lat,omitempty"`
	Lon          string   `json:"lon,omitempty"`
	Food         string   `json:"food"`
	Drink        string   `json:"drink"`
	Pricing      *Pricing `json:"pricing,omitempty"`
	ContactName  string   `json:"contact_name"`
	ContactEmail string   `json:"contact_email"`
	Musicians    []string `json:"musicians"`
	Instructors  []string `json:"instructors"`
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
		b, _ := json.Marshal(pf)

		ip := getClientIP(r)
		dances, _ := client.GetDances(r.Context())
		title := i18n.T(r, "suggest_event_title")
		renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
			HintSMTP:    cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
			FormToken:   issueFormToken(ip),
			Dances:      dances,
			ManageToken: token,
			PrefillJSON: template.JS(b),
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
		if err := r.ParseForm(); err != nil {
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
