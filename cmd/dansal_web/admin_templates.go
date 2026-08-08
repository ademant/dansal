package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// ── Event Templates ───────────────────────────────────────────────────────────

type AdminTemplatesData struct {
	Templates []EventTemplate
	OrgMap    map[int]Organization
	PinnedIDs map[int]bool
}

func adminTemplatesHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		ts, _ := listTemplates(db, su.ID, orgIDsForTemplates(r, client, su))
		orgs, _ := client.GetOrganizations(r.Context())
		orgMap := buildOrgMap(orgs)
		pinnedIDs, _ := listPinnedTemplateIDs(db, su.ID)
		title := i18n.T(r, "admin_templates_title")
		renderTemplate(w, tmpls.adminTemplates, tmplData(r, cfg, i18n, title, AdminTemplatesData{
			Templates: ts,
			OrgMap:    orgMap,
			PinnedIDs: pinnedIDs,
		}))
	}
}

// saveEventAsTemplate builds a template from ev's current field values and
// persists it. Shared by adminTemplateSaveHandler (event.html's standalone
// "save as template" action, operating on an already-fully-saved event) and
// the unified event form's intent=save-template path (which saves the main
// form first, then calls this against the freshly-saved event).
func saveEventAsTemplate(db *sql.DB, userID int, orgID *int, name string, ev Event) (int64, error) {
	data, _ := json.Marshal(templateDataFromEvent(ev))
	return saveTemplate(db, userID, orgID, nil, nil, name, string(data))
}

func adminTemplateSaveHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		ev, err := client.GetEvent(r.Context(), eventID)
		if err != nil {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		var orgID *int
		if v := strings.TrimSpace(r.FormValue("org_id")); v != "" {
			if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 {
				orgID = &n
			}
		}
		wantsJSON := r.Header.Get("Accept") == "application/json"
		jsonResp := func(status int, ok bool, errMsg string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if ok {
				json.NewEncoder(w).Encode(map[string]any{"ok": true})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": errMsg})
			}
		}
		if _, err := saveEventAsTemplate(db, su.ID, orgID, name, ev); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				if wantsJSON {
					jsonResp(http.StatusConflict, false, "a template with that name already exists")
				} else {
					http.Error(w, "a template with that name already exists", http.StatusConflict)
				}
			} else {
				if wantsJSON {
					jsonResp(http.StatusInternalServerError, false, "save failed")
				} else {
					http.Error(w, "save failed", http.StatusInternalServerError)
				}
			}
			return
		}
		if wantsJSON {
			jsonResp(http.StatusOK, true, "")
			return
		}
		http.Redirect(w, r, "/admin/templates", http.StatusSeeOther)
	}
}

func adminTemplateDeleteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := deleteTemplate(db, id, su.ID, su.Role == "admin"); err != nil {
			log.Printf("delete template %d: %v", id, err)
		}
		http.Redirect(w, r, "/admin/templates", http.StatusSeeOther)
	}
}

// templatePinRedirectTarget lets both /admin/templates and the dashboard's
// "Your Presets" section post to the same pin/unpin endpoints and land back
// where the user started.
func templatePinRedirectTarget(r *http.Request) string {
	if r.URL.Query().Get("from") == "dashboard" {
		return "/dashboard"
	}
	return "/admin/templates"
}

func adminTemplatePinHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := pinTemplate(db, su.ID, id); err != nil {
			log.Printf("pin template %d: %v", id, err)
		}
		http.Redirect(w, r, templatePinRedirectTarget(r), http.StatusSeeOther)
	}
}

func adminTemplateUnpinHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := unpinTemplate(db, su.ID, id); err != nil {
			log.Printf("unpin template %d: %v", id, err)
		}
		http.Redirect(w, r, templatePinRedirectTarget(r), http.StatusSeeOther)
	}
}

// canAccessTemplate reports whether su may read/apply t: its owner, a member
// of the org it's scoped to, or an admin. Mirrors the owner-or-org-member
// rule listTemplates already uses to decide what a user sees in the picker;
// enforced here too since ids are sequential and enumerable (#1001).
func canAccessTemplate(t EventTemplate, su *SessionUser, orgIDs []int) bool {
	if su.Role == "admin" || t.UserID == su.ID {
		return true
	}
	if t.OrgID != nil {
		for _, oid := range orgIDs {
			if oid == *t.OrgID {
				return true
			}
		}
	}
	return false
}

func adminTemplateDataHandler(db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		t, err := getTemplate(db, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !canAccessTemplate(t, su, memberOrgIDs(r, client, su)) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(t.Data))
	}
}

type AdminTemplateAssignData struct {
	EventIDs  []int
	Templates []EventTemplate
}

func adminTemplateAssignPageHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		var eventIDs []int
		for _, v := range r.URL.Query()["event_ids"] {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				eventIDs = append(eventIDs, n)
			}
		}
		if len(eventIDs) == 0 {
			http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
			return
		}
		ts, _ := listTemplates(db, su.ID, orgIDsForTemplates(r, client, su))
		title := i18n.T(r, "admin_template_assign_title")
		renderTemplate(w, tmpls.adminTemplateAssign, tmplData(r, cfg, i18n, title, AdminTemplateAssignData{
			EventIDs:  eventIDs,
			Templates: ts,
		}))
	}
}

func adminTemplateAssignApplyHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var eventIDs []int
		for _, v := range r.Form["event_ids"] {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				eventIDs = append(eventIDs, n)
			}
		}
		if len(eventIDs) == 0 {
			http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
			return
		}
		templateID, err := strconv.Atoi(r.FormValue("template_id"))
		if err != nil || templateID == 0 {
			http.Error(w, "template_id required", http.StatusBadRequest)
			return
		}
		tpl, err := getTemplate(db, templateID)
		if err != nil {
			http.Error(w, "template not found", http.StatusNotFound)
			return
		}
		if !canAccessTemplate(tpl, su, memberOrgIDs(r, client, su)) {
			http.Error(w, "template not found", http.StatusNotFound)
			return
		}
		var td templateEventData
		if err := json.Unmarshal([]byte(tpl.Data), &td); err != nil {
			http.Error(w, "bad template data", http.StatusInternalServerError)
			return
		}
		fields := make(map[string]bool)
		for _, f := range r.Form["fields"] {
			fields[f] = true
		}

		// Pre-fetch locations if needed for template location resolution.
		var tplLocations []Location
		if fields["loc"] && td.LocID > 0 {
			tplLocations = client.FetchRefBundle(r.Context()).Locations
		}

		token := getSessionToken(r)
		for _, evID := range eventIDs {
			ev, err := client.GetEvent(r.Context(), evID)
			if err != nil {
				log.Printf("template assign get event %d: %v", evID, err)
				continue
			}
			var danceIDs []int
			drows, err := db.QueryContext(r.Context(), "SELECT dance_id FROM event_dances WHERE event_id = ?", evID)
			if err != nil {
				log.Printf("template assign event_dances query %d: %v", evID, err)
				continue
			}
			for drows.Next() {
				var did int
				if err := drows.Scan(&did); err != nil {
					drows.Close()
					log.Printf("template assign event_dances scan %d: %v", evID, err)
					continue
				}
				danceIDs = append(danceIDs, did)
			}
			drows.Close()
			var musicianIDs []int
			for _, m := range ev.Musicians {
				musicianIDs = append(musicianIDs, m.ID)
			}
			req := EventUpdateReq{
				Title:              ev.Title,
				Description:        ev.Description,
				StartTime:          ev.StartTime,
				EndTime:            ev.EndTime,
				HasBall:            ev.HasBall,
				HasWorkshop:        ev.HasWorkshop,
				HasFestival:        ev.HasFestival,
				WorkshopDifficulty: ev.WorkshopDifficulty,
				IsCancelled:        ev.IsCancelled,
				IsPublished:        ev.IsPublished,
				BookingURL:         ev.BookingURL,
				Availability:       ev.Availability,
				TicketsTotal:       ev.TicketsTotal,
				BookingEnabled:     ev.BookingEnabled,
				Food:               ev.Food,
				Drink:              ev.Drink,
				Attributes:         ev.Attributes,
				ContactName:        ev.ContactName,
				ContactEmail:       ev.ContactEmail,
				Tags:               ev.Tags,
				URL:                ev.URL,
				OrganizationID:     ev.OrganizationID,
				Pricing:            ev.Pricing,
				LocationID:         ev.LocationID,
				Musicians:          musicianIDs,
				Dances:             danceIDs,
			}
			applyTemplateFields(&req, &td, fields, tplLocations)
			if _, err := client.UpdateEvent(r.Context(), evID, req, token); err != nil {
				log.Printf("template assign update event %d: %v", evID, err)
			}
			if fields["timetable"] && len(td.Timetable) > 0 {
				var ttEntries []TimetableEntryReq
				for _, e := range td.Timetable {
					ttEntries = append(ttEntries, TimetableEntryReq{
						StartTime:    e.StartTime,
						EndTime:      e.EndTime,
						Title:        e.Title,
						Description:  e.Description,
						Room:         e.Room,
						EntryDate:    e.EntryDate,
						LocationID:   e.LocationID,
						MusicianID:   e.MusicianID,
						InstructorID: e.InstructorID,
					})
				}
				if err := client.ReplaceTimetable(r.Context(), evID, ttEntries, token); err != nil {
					log.Printf("template assign timetable event %d: %v", evID, err)
				}
			}
		}
		http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
	}
}

type templateEventData struct {
	URL                string           `json:"url"`
	BookingURL         string           `json:"booking_url"`
	StartTime          string           `json:"start_time"`
	EndTime            string           `json:"end_time"`
	HasBall            bool             `json:"has_ball"`
	HasWorkshop        bool             `json:"has_workshop"`
	HasFestival        bool             `json:"has_festival"`
	WorkshopDifficulty string           `json:"workshop_difficulty"`
	OrgID              int              `json:"org_id"`
	LocID              int              `json:"loc_id"`
	PricingType        string           `json:"pricing_type"`
	PricingAmount      float64          `json:"pricing_amount"`
	PricingCurrency    string           `json:"pricing_currency"`
	PricingLines       []Price          `json:"pricing_lines"`
	Tags               []string         `json:"tags"`
	DanceIDs           []int            `json:"dance_ids"`
	Food               string           `json:"food"`
	Drink              string           `json:"drink"`
	FloorCondition     string           `json:"floor_condition"`
	Attributes         map[string]bool  `json:"attributes,omitempty"`
	ContactName        string           `json:"contact_name,omitempty"`
	ContactEmail       string           `json:"contact_email,omitempty"`
	TicketsTotal       int              `json:"tickets_total"`
	BookingEnabled     bool             `json:"booking_enabled"`
	Timetable          []TimetableEntry `json:"timetable"`
}

func templateDataFromEvent(ev Event) templateEventData {
	d := templateEventData{
		URL:                ev.URL,
		BookingURL:         ev.BookingURL,
		StartTime:          ev.StartTime,
		EndTime:            ev.EndTime,
		HasBall:            ev.HasBall,
		HasWorkshop:        ev.HasWorkshop,
		HasFestival:        ev.HasFestival,
		WorkshopDifficulty: ev.WorkshopDifficulty,
		Tags:               ev.Tags,
		Food:               ev.Food,
		Drink:              ev.Drink,
		FloorCondition:     ev.FloorCondition,
		Attributes:         ev.Attributes,
		ContactName:        ev.ContactName,
		ContactEmail:       ev.ContactEmail,
		TicketsTotal:       ev.TicketsTotal,
		BookingEnabled:     ev.BookingEnabled,
		Timetable:          ev.Timetable,
	}
	if ev.OrganizationID != nil {
		d.OrgID = *ev.OrganizationID
	}
	if ev.LocationID != nil {
		d.LocID = *ev.LocationID
	}
	if p := ev.Pricing; p != nil {
		d.PricingType = p.Type
		d.PricingAmount = p.Amount
		d.PricingCurrency = p.Currency
		d.PricingLines = p.Prices
	}
	return d
}
