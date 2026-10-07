package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// GeoToken (#1314) gates the public geocode-search proxy — a stateless
	// HMAC'd timestamp (newFormToken/validGeoToken, formguard.go), distinct
	// from FormToken above: that one is a one-time token consumed by the
	// real wizard submission, but this page's location-search widget can
	// fire several lookups before that submission happens, so its token
	// must not be invalidated by being used. Unlike FormToken it's set
	// unconditionally, including in ManageToken mode — the location search
	// widget works there too.
	GeoToken string
	Dances   []Dance
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
	// IsNextSuggestion/HasLeftInfo/Left/Cap (#1468): set when the visitor
	// arrived via the done page's "suggest another event" button
	// (?next=1[&left=&cap=]). IsNextSuggestion triggers the sessionStorage
	// restore script (events_suggest.html); HasLeftInfo/Left/Cap show the
	// same remaining-count hint the done page showed, when it applied.
	IsNextSuggestion bool
	HasLeftInfo      bool
	Left             int
	Cap              int
}

type SuggestDoneData struct {
	NeedsReview bool

	// ImageUploadError (#1285): set when the suggestion itself went through
	// fine but the attached image could not be uploaded — the submission is
	// not in error, this is just a heads-up.
	ImageUploadError string

	// HasLeftInfo/Left/Cap (#1468): the per-address remaining-suggestion
	// count, carried from the API's SuggestEvent response through the
	// ?left=&cap= redirect params. HasLeftInfo is false when the API
	// omitted them (no email, or SMTP not configured) -- the "suggest
	// another event" button then shows with no hint, per #1468's spec for
	// "no left param".
	HasLeftInfo bool
	Left        int
	Cap         int
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
		data := SuggestPageData{
			HintSMTP:          cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
			CaptchaSiteKey:    cfg.CaptchaSiteKey,
			FormToken:         tok,
			GeoToken:          newFormToken(),
			Dances:            dances,
			CanUploadImageNow: suggestCanUploadImage(r),
		}
		// #1468: "suggest another event" lands here with ?next=1, optionally
		// carrying the same remaining-count hint shown on the done page
		// (left/cap — small, non-identifying integers, unlike the manage
		// token, so passing them in the URL doesn't repeat that mistake).
		// The actual form-field prefill comes from sessionStorage client-side
		// (events_suggest.html), not from here — nothing else server-side
		// needs to change for ?next=1 itself.
		if r.URL.Query().Get("next") == "1" {
			data.IsNextSuggestion = true
			if left, err := strconv.Atoi(r.URL.Query().Get("left")); err == nil {
				if cap, err2 := strconv.Atoi(r.URL.Query().Get("cap")); err2 == nil {
					data.HasLeftInfo = true
					data.Left = left
					data.Cap = cap
				}
			}
		}
		title := i18n.T(r, "suggest_event_title")
		renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, data))
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
				GeoToken:          newFormToken(),
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
				GeoToken:          newFormToken(),
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
			GeoToken:          newFormToken(),
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
// suggestError re-renders the wizard with the error message and (#1467)
// everything the visitor already entered, read back from the just-rejected
// POST's own r.Form — every suggestSubmitHandler rejection path (pending
// lock, captcha, links, rate limit, the API call itself) calls this after
// guardFormSubmit has parsed the form, so r.Form is always populated here.
func suggestError(w http.ResponseWriter, r *http.Request, cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n, errMsg, ip string) {
	dances, err := client.GetDances(r.Context())
	if err != nil {
		log.Printf("suggest: could not load dances: %v", err)
	}
	pf := wizPrefillFromForm(r)
	prefillJSON := template.JS("{}")
	if b, merr := json.Marshal(pf); merr == nil {
		prefillJSON = template.JS(b)
	} else {
		log.Printf("could not marshal error-prefill JSON: %v", merr)
	}
	prefillTags := make(map[string]bool)
	for _, t := range pf.Tags {
		prefillTags[t] = true
	}
	prefillDanceIDs := make(map[int]bool)
	for _, id := range pf.DanceIDs {
		prefillDanceIDs[id] = true
	}

	title := i18n.T(r, "suggest_event_title")
	renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
		HintSMTP:          cfg.SMTPHost != "" || cfg.SMTPSendmail != "",
		CanUploadImageNow: suggestCanUploadImage(r),
		Error:             errMsg,
		FormToken:         issueFormToken(ip),
		GeoToken:          newFormToken(),
		Dances:            dances,
		PrefillJSON:       prefillJSON,
		PrefillTags:       prefillTags,
		PrefillDanceIDs:   prefillDanceIDs,
	}))
}

func suggestSubmitHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		// #1467: parsed before the rate-limit check (rather than leaving it
		// to guardFormSubmit, below) so every rejection path -- including
		// this one -- has r.Form available for suggestError's prefill.
		// Calling ParseForm again inside guardFormSubmit is a harmless no-op
		// once it has already succeeded once.
		if err := r.ParseForm(); err != nil {
			logFormReject(r, "PARSE_ERROR", ip, err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		key := ip + "|" + r.UserAgent()
		if publicThrottle.isBlocked(key) {
			log.Printf("%s ip=%s path=%s", publicBlock, ip, r.URL.Path)
			suggestError(w, r, cfg, tmpls, client, i18n, i18n.T(r, "suggest_error_rate_limit"), ip)
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

		title := r.FormValue("dansal_title")
		startTime := r.FormValue("start_time")
		location := r.FormValue("location")
		// #1467: scoped to this specific event (normalized title + start
		// time + location) rather than a bare "suggest" literal -- the old
		// scope blocked ANY further suggestion from the same visitor for the
		// whole form-token window, not just a resend of the same one.
		pendingScope := suggestPendingScope(title, startTime, location)

		if hasPendingSubmission(ip, r.UserAgent(), pendingScope) {
			logFormReject(r, "PENDING_SUBMISSION", ip, nil)
			suggestError(w, r, cfg, tmpls, client, i18n, i18n.T(r, "suggest_error_pending"), ip)
			return
		}

		// Captcha check.
		if cfg.CaptchaSiteKey != "" {
			if err := verifyTurnstile(cfg, r.FormValue("cf-turnstile-response")); err != nil {
				suggestError(w, r, cfg, tmpls, client, i18n, i18n.T(r, "suggest_error_captcha"), ip)
				return
			}
		}

		description := r.FormValue("description")

		if strings.Contains(title, "http://") || strings.Contains(title, "https://") ||
			strings.Contains(description, "http://") || strings.Contains(description, "https://") {
			suggestError(w, r, cfg, tmpls, client, i18n, i18n.T(r, "suggest_error_links"), ip)
			return
		}

		tags := r.Form["dansal_tags"]
		musicians := peopleFromForm(r, "dansal_musicians")
		instructors := peopleFromForm(r, "dansal_instructors")

		req := SuggestEventReq{
			Title:       title,
			Description: description,
			StartTime:   startTime,
			EndTime:     r.FormValue("end_time"),
			HasBall:     sliceContains(tags, "bal-folk"),
			HasWorkshop: sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop"),
			HasFestival: sliceContains(tags, "festival"),
			Tags:        tags,
			DanceIDs:    danceIDsFromForm(r),
			URL:         r.FormValue("url"),
			Food:        r.FormValue("food"),
			Drink:       r.FormValue("drink"),
			Location: PreviewLoc{
				Location:  location,
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
			Pricing:       pricingFromForm(r),
			ContactName:   strings.TrimSpace(r.FormValue("contact_name")),
			ContactEmail:  strings.TrimSpace(r.FormValue("contact_email")),
			Musicians:     musicians,
			Instructors:   instructors,
			Timetable:     timetableFromForm(r),
		}

		publicThrottle.record(key)
		setPendingSubmission(ip, r.UserAgent(), pendingScope, stdFormMaxAge(cfg))
		globalEmailSendRate.record()

		result, err := client.SuggestEvent(r.Context(), req, cfg.publicBaseURL(), getBoardSessionToken(r))
		if err != nil {
			clearPendingSubmission(ip, r.UserAgent(), pendingScope)
			log.Printf("dansal-web: suggest submit failed ip_hash=%s err=%v", hashIP(ip), err)
			suggestError(w, r, cfg, tmpls, client, i18n, i18n.T(r, "suggest_error_submit"), ip)
			return
		}
		token := result.Token

		// #1050: an authenticated submitter attaches the image right away via
		// the standing manage token returned by the suggest API. The
		// suggestion itself already went through, so an upload failure rides
		// along as a flash (#1285) rather than blocking the redirect.
		var flash FlashMsg
		if suggestCanUploadImage(r) && token != "" {
			if file, header, ferr := r.FormFile("image"); ferr == nil {
				defer file.Close()
				if data, rerr := io.ReadAll(file); rerr == nil {
					if uerr := client.UploadSuggestManageImage(r.Context(), token, data, header.Filename); uerr != nil {
						log.Printf("suggest: upload image: %v", uerr)
						flash = imageUploadErrorFlash("image", uerr)
					}
				}
			}
		}

		// #1468: thread the per-address remaining count through so the done
		// page can offer a sized "suggest another event" hint/button. Both
		// are omitted together when the cap doesn't apply (no email, or
		// SMTP not configured) -- the done page then shows no hint at all.
		doneURL := "/events/suggest/done"
		if result.Remaining != nil && result.Limit != nil {
			doneURL += fmt.Sprintf("?left=%d&cap=%d", *result.Remaining, *result.Limit)
		}

		if flash.ImageUploadError != "" {
			flashRedirect(w, r, doneURL, newErrorID(), flash)
			return
		}
		http.Redirect(w, r, doneURL, http.StatusSeeOther)
	}
}

func suggestDoneHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := i18n.T(r, "suggest_done_title")
		flash := flashTake(r.URL.Query().Get("msg"))
		data := SuggestDoneData{
			NeedsReview:      r.URL.Query().Get("review") == "1",
			ImageUploadError: flash.ImageUploadError,
		}
		if left, err := strconv.Atoi(r.URL.Query().Get("left")); err == nil {
			if cap, err2 := strconv.Atoi(r.URL.Query().Get("cap")); err2 == nil {
				data.HasLeftInfo = true
				data.Left = left
				data.Cap = cap
			}
		}
		renderTemplate(w, tmpls.suggestDone, tmplData(r, cfg, i18n, title, data))
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
	// Email/SuggesterName (#1467) are only populated by wizPrefillFromForm,
	// for re-rendering a rejected submission with what the visitor already
	// entered — the manage-link/import prefills above never need these,
	// since neither edits someone else's own submitter identity.
	Email         string `json:"email,omitempty"`
	SuggesterName string `json:"suggester_name,omitempty"`
}

// danceIDsFromForm parses the wizard's dance_ids[] checkboxes.
func danceIDsFromForm(r *http.Request) []int {
	var danceIDs []int
	for _, s := range r.Form["dance_ids"] {
		if id, err := strconv.Atoi(s); err == nil {
			danceIDs = append(danceIDs, id)
		}
	}
	return danceIDs
}

// pricingFromForm parses the wizard's pricing_type/pricing_amount/
// pricing_currency/pl_label/pl_amount fields. Shared by suggestSubmitHandler
// (building the API request) and wizPrefillFromForm (#1467, re-rendering a
// rejected submission).
func pricingFromForm(r *http.Request) *Pricing {
	pt := r.FormValue("pricing_type")
	if pt == "" || pt == "none" {
		return nil
	}
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
			return nil
		}
	}
	return p
}

// timetableFromForm parses the wizard's tt_start/tt_end/tt_title/tt_desc/
// tt_room/tt_type row fields. Shared by suggestSubmitHandler (building the
// API request) and wizPrefillFromForm (#1467, re-rendering a rejected
// submission).
func timetableFromForm(r *http.Request) []TimetableEntryReq {
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
	return timetable
}

// wizPrefillFromForm rebuilds a wizPrefill from the submitted form (#1467):
// used by suggestError so a rejected submission (pending lock, captcha,
// links, rate limit, or the API itself) re-renders with everything the
// visitor already entered, instead of a blank wizard.
func wizPrefillFromForm(r *http.Request) wizPrefill {
	return wizPrefill{
		Title:         r.FormValue("dansal_title"),
		Description:   r.FormValue("description"),
		URL:           r.FormValue("url"),
		StartTime:     r.FormValue("start_time"),
		EndTime:       r.FormValue("end_time"),
		Tags:          r.Form["dansal_tags"],
		DanceIDs:      danceIDsFromForm(r),
		Location:      r.FormValue("location"),
		Town:          r.FormValue("town"),
		Country:       r.FormValue("country"),
		Address:       r.FormValue("address"),
		Zipcode:       r.FormValue("zipcode"),
		Lat:           r.FormValue("lat"),
		Lon:           r.FormValue("lon"),
		Food:          r.FormValue("food"),
		Drink:         r.FormValue("drink"),
		Pricing:       pricingFromForm(r),
		ContactName:   strings.TrimSpace(r.FormValue("contact_name")),
		ContactEmail:  strings.TrimSpace(r.FormValue("contact_email")),
		Musicians:     peopleFromForm(r, "dansal_musicians"),
		Instructors:   peopleFromForm(r, "dansal_instructors"),
		Timetable:     timetableFromForm(r),
		Email:         r.FormValue("email"),
		SuggesterName: strings.TrimSpace(r.FormValue("suggester_name")),
	}
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
			GeoToken:          newFormToken(),
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
		musicians := peopleFromForm(r, "dansal_musicians")
		instructors := peopleFromForm(r, "dansal_instructors")

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
				// #1414: the manage link uses the same venue picker, so carry
				// the picked position/OSM identity like the initial submit does.
				Latitude:  parseLatLng(r.FormValue("lat")),
				Longitude: parseLatLng(r.FormValue("lon")),
				OsmID:     parseOsmID(r.FormValue("osm_id")),
				OsmType:   r.FormValue("osm_type"),
			},
			ContactName:  strings.TrimSpace(r.FormValue("contact_name")),
			ContactEmail: strings.TrimSpace(r.FormValue("contact_email")),
			Musicians:    musicians,
			Instructors:  instructors,
			Timetable:    timetable,
		}

		needsReview, err := client.PatchSuggestManageEvent(r.Context(), token, req)
		if err != nil {
			log.Printf("dansal-web: suggest manage patch failed ip_hash=%s err=%v", hashIP(getClientIP(r)), err)
			errKey := "suggest_error_submit"
			if ae, ok := errors.AsType[*apiHTTPError](err); ok && ae.ErrorCode == "start_time_past" {
				errKey = "suggest_date_past" // #1413
			}
			title := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
				Error:       i18n.T(r, errKey),
				ManageToken: token,
			}))
			return
		}
		var flash FlashMsg
		if file, header, ferr := r.FormFile("image"); ferr == nil {
			defer file.Close()
			if data, rerr := io.ReadAll(file); rerr == nil {
				if uerr := client.UploadSuggestManageImage(r.Context(), token, data, header.Filename); uerr != nil {
					log.Printf("suggest manage: upload image: %v", uerr)
					flash = imageUploadErrorFlash("image", uerr)
				}
			}
		}
		dest := "/events/suggest/done"
		if needsReview {
			dest += "?review=1"
		}
		if flash.ImageUploadError != "" {
			flashRedirect(w, r, dest, newErrorID(), flash)
			return
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
