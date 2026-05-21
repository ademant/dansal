package main

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
)

type TemplateData struct {
	Title        string
	Domain       string
	SiteName     string // display name; defaults to Domain when empty
	User         *SessionUser
	Strings      I18nStrings
	LangCode     string
	Languages    []LangOption
	Contact      string
	ImpressumURL string
	Data         any
	BannerHeight int
	LogoHeight   int
	DarkMode     string // "auto", "light", or "dark"
	AppVersion   string
	AppBuildTime string
}

func tmplData(r *http.Request, cfg *Config, i18n *I18n, title string, data any) TemplateData {
	lang := i18n.detectLang(r)
	contact := cfg.ContactOverride
	if contact == "" {
		contact = cfg.pagesContent.ContactText(lang)
	}
	impressumURL := ""
	if cfg.pagesContent.ImpressumText(lang) != "" {
		impressumURL = "/impressum"
	}
	isMain := r.URL.Path == "/"
	bannerHeight := cfg.BannerHeightSub
	logoHeight := cfg.LogoHeightSub
	if isMain {
		bannerHeight = cfg.BannerHeightMain
		logoHeight = cfg.LogoHeightMain
	}
	siteName := cfg.SiteName
	if siteName == "" {
		siteName = cfg.Domain
	}
	return TemplateData{
		Title:        title,
		Domain:       cfg.Domain,
		SiteName:     siteName,
		User:         getSessionUser(r),
		Strings:      i18n.Strings(lang),
		LangCode:     lang,
		Languages:    i18n.Options(lang),
		Contact:      contact,
		ImpressumURL: impressumURL,
		Data:         data,
		BannerHeight: bannerHeight,
		LogoHeight:   logoHeight,
		DarkMode:     cfg.DarkMode,
		AppVersion:   Version,
		AppBuildTime: BuildTime,
	}
}

type IndexData struct {
	Events          []Event
	OrgMap          map[int]Organization
	FederatedEvents []FederatedEvent
	Dances          []Dance
}

type EventData struct {
	Event          Event
	Org            *Organization
	OrgSlug        string
	ContactPosts   []ContactPost
	CanManageBoard bool
	BoardPosted    bool
	BoardContacted bool
	BoardError     string
	BookingOK      bool
	BookingError   string
}

type OrgData struct {
	Org            Organization
	UpcomingEvents []Event
	PastEvents     []Event
	AllEvents      []Event
	Musicians      []Musician
	Slug           string
	Handle         string
	FollowerCount  int
}

type OrgListItem struct {
	Org           Organization
	Slug          string
	EventCount    int
	LocationCount int
	FirstTown     string
	FedHandle     string // "@slug@domain" when the org has a fediverse actor
}

type OrgMapPin struct {
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	OrgName string  `json:"org"`
	OrgSlug string  `json:"slug"`
	LocName string  `json:"loc"`
}

type OrgsListData struct {
	Items   []OrgListItem
	MapPins []OrgMapPin
}

//go:embed templates
var templateFS embed.FS

//go:embed static/favicon.svg
var faviconSVG []byte

//go:embed static/logo.svg
var logoSVG []byte

//go:embed static/banner.svg
var bannerSVG []byte

func svgHandler(data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	}
}

func dynamicSVGHandler(imagesDir, key string, fallback []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := findSiteAssetOnDisk(imagesDir, key)
		if len(data) == 0 {
			data = fallback
		}
		mime := detectAssetMIME(data)
		if mime == "" {
			mime = "image/svg+xml"
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(data)
	}
}

var locMonths = map[string][12]string{
	"br": {"Gen.", "C'hwev.", "Meur.", "Ebr.", "Mae", "Mezh.", "Gouer.", "Eost", "Gwen.", "Here", "Du", "Kerz."},
	"de": {"Jan", "Feb", "Mär", "Apr", "Mai", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dez"},
	"en": {"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
	"fr": {"jan.", "fév.", "mar.", "avr.", "mai", "juin", "juil.", "août", "sept.", "oct.", "nov.", "déc."},
	"es": {"Ene", "Feb", "Mar", "Abr", "May", "Jun", "Jul", "Ago", "Sep", "Oct", "Nov", "Dic"},
	"it": {"Gen", "Feb", "Mar", "Apr", "Mag", "Giu", "Lug", "Ago", "Set", "Ott", "Nov", "Dic"},
	"nl": {"Jan", "Feb", "Mrt", "Apr", "Mei", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dec"},
}
var locWeekdays = map[string][7]string{
	"br": {"Sul.", "Lun.", "Meur.", "Merc'h.", "Yaou.", "Gwen.", "Sad."},
	"de": {"So.", "Mo.", "Di.", "Mi.", "Do.", "Fr.", "Sa."},
	"en": {"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	"fr": {"Dim.", "Lun.", "Mar.", "Mer.", "Jeu.", "Ven.", "Sam."},
	"es": {"Dom", "Lun", "Mar", "Mié", "Jue", "Vie", "Sáb"},
	"it": {"Dom", "Lun", "Mar", "Mer", "Gio", "Ven", "Sab"},
	"nl": {"Zo", "Ma", "Di", "Wo", "Do", "Vr", "Za"},
}

func locMonth(lang string, m time.Month) string {
	if names, ok := locMonths[lang]; ok {
		return names[m-1]
	}
	return locMonths["en"][m-1]
}
func locWeekday(lang string, w time.Weekday) string {
	if names, ok := locWeekdays[lang]; ok {
		return names[w]
	}
	return locWeekdays["en"][w]
}

var parseLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"}

func parseTime(s string) (time.Time, bool) {
	for _, layout := range parseLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// reURLAttr matches href="..." and src="..." produced by goldmark's renderer.
var reURLAttr = regexp.MustCompile(`(?i)(href|src)="([^"]*)"`)

// safeSchemes lists URL schemes allowed in rendered markdown output.
var safeSchemes = map[string]bool{
	"http": true, "https": true, "mailto": true, "tel": true,
}

// sanitizeMarkdownHTML strips dangerous URI schemes (javascript:, data:,
// vbscript:) from href and src attributes in goldmark-rendered HTML.
// Relative URLs (no scheme) are left untouched.
func sanitizeMarkdownHTML(s string) string {
	return reURLAttr.ReplaceAllStringFunc(s, func(m string) string {
		parts := reURLAttr.FindStringSubmatch(m)
		if parts == nil {
			return m
		}
		u, err := url.Parse(parts[2])
		if err != nil {
			return parts[1] + `="#"`
		}
		scheme := strings.ToLower(u.Scheme)
		if scheme != "" && !safeSchemes[scheme] {
			return parts[1] + `="#"`
		}
		return m
	})
}

var tmplFuncMap = template.FuncMap{
	"formatTime": func(lang, s string) string {
		t, ok := parseTime(s)
		if !ok {
			return s
		}
		wd := locWeekday(lang, t.Weekday())
		mo := locMonth(lang, t.Month())
		if lang == "de" {
			return fmt.Sprintf("%s %02d. %s %d, %02d:%02d", wd, t.Day(), mo, t.Year(), t.Hour(), t.Minute())
		}
		return fmt.Sprintf("%s %02d %s %d, %02d:%02d", wd, t.Day(), mo, t.Year(), t.Hour(), t.Minute())
	},
	"formatDate": func(lang, s string) string {
		t, ok := parseTime(s)
		if !ok {
			return s
		}
		mo := locMonth(lang, t.Month())
		if lang == "de" {
			return fmt.Sprintf("%02d. %s %d", t.Day(), mo, t.Year())
		}
		return fmt.Sprintf("%02d %s %d", t.Day(), mo, t.Year())
	},
	"isoDate": func(s string) string {
		if t, ok := parseTime(s); ok {
			return t.Format("2006-01-02")
		}
		return s
	},
	"isoTime": func(s string) string {
		if t, ok := parseTime(s); ok {
			return t.Format("15:04")
		}
		return ""
	},
	"formatHourMin": func(s string) string {
		if t, ok := parseTime(s); ok {
			return fmt.Sprintf("%02d:%02d", t.Hour(), t.Minute())
		}
		return ""
	},
	"sameDate": func(s1, s2 string) bool {
		t1, ok1 := parseTime(s1)
		t2, ok2 := parseTime(s2)
		if !ok1 || !ok2 {
			return false
		}
		return t1.Year() == t2.Year() && t1.Month() == t2.Month() && t1.Day() == t2.Day()
	},
	"join": func(ss []string) string {
		return strings.Join(ss, ", ")
	},
	"jsStr": func(s string) template.JS {
		b, _ := json.Marshal(s)
		return template.JS(b)
	},
	"floatVal": func(f *float64) string {
		if f == nil {
			return ""
		}
		return strconv.FormatFloat(*f, 'f', -1, 64)
	},
	"derefInt": func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	},
	"locationsJSON": func(locs []Location) template.JS {
		type locItem struct {
			ID     int    `json:"id"`
			Label  string `json:"label"`
			OrgIDs []int  `json:"orgIDs"`
		}
		items := make([]locItem, len(locs))
		for i, l := range locs {
			label := l.Location
			if l.ShortName != "" {
				label = l.ShortName
			}
			if l.Town != "" {
				label += ", " + l.Town
			}
			orgIDs := l.OrganizationIDs
			if orgIDs == nil {
				orgIDs = []int{}
			}
			items[i] = locItem{ID: l.ID, Label: label, OrgIDs: orgIDs}
		}
		b, _ := json.Marshal(items)
		return template.JS(b)
	},
	// mastodonURL converts "@user@instance.tld" → "https://instance.tld/@user".
	// If the value already starts with "http", it is returned unchanged.
	"mastodonURL": func(handle string) string {
		if strings.HasPrefix(handle, "http") {
			return handle
		}
		// strip leading @
		h := strings.TrimPrefix(handle, "@")
		parts := strings.SplitN(h, "@", 2)
		if len(parts) == 2 {
			return "https://" + parts[1] + "/@" + parts[0]
		}
		return handle
	},
	"eventsGeoJSON": func(events []Event) template.JS {
		type geoEvent struct {
			ID                 int     `json:"id"`
			Title              string  `json:"t"`
			Start              string  `json:"s"`
			End                string  `json:"e,omitempty"`
			Location           string  `json:"loc,omitempty"`
			ShortName          string  `json:"sn,omitempty"`
			Town               string  `json:"town,omitempty"`
			Country            string  `json:"c,omitempty"`
			Lat                float64 `json:"lat"`
			Lng                float64 `json:"lng"`
			URL                string  `json:"url,omitempty"`
			Ball               bool    `json:"ball,omitempty"`
			Workshop           bool    `json:"ws,omitempty"`
			WorkshopDifficulty string  `json:"wd,omitempty"`
			Festival           bool    `json:"fest,omitempty"`
			Cancelled          bool    `json:"x,omitempty"`
			Availability       string  `json:"av,omitempty"`
			BookingEnabled     bool    `json:"book,omitempty"`
			Fee                string  `json:"fee,omitempty"` // "free"|"donation"|"paid"|""
		}
		var geo []geoEvent
		for _, e := range events {
			if e.LocationLat == nil || e.LocationLng == nil || (*e.LocationLat == 0 && *e.LocationLng == 0) {
				continue
			}
			lat := *e.LocationLat
			lng := *e.LocationLng
			fee := ""
			if e.Pricing != nil {
				switch e.Pricing.Type {
				case "free":
					fee = "free"
				case "donation":
					fee = "donation"
				case "single", "multiple":
					fee = "paid"
				}
			}
			end := ""
			if t, err := time.Parse(time.RFC3339, e.EndTime); err == nil {
				end = t.Format("2006-01-02")
			}
			geo = append(geo, geoEvent{
				ID: e.ID, Title: e.Title, Start: e.StartTime, End: end,
				Location: e.Location, ShortName: e.LocationShortName, Town: e.LocationTown, Country: e.LocationCountry,
				Lat: lat, Lng: lng, URL: e.URL,
				Ball: e.HasBall, Workshop: e.HasWorkshop, WorkshopDifficulty: e.WorkshopDifficulty,
				Festival: e.HasFestival,
				Cancelled: e.IsCancelled, Availability: e.Availability,
				BookingEnabled: e.BookingEnabled,
				Fee: fee,
			})
		}
		if geo == nil {
			return template.JS("[]")
		}
		b, _ := json.Marshal(geo)
		return template.JS(b)
	},
	"orgsMapJSON": func(pins []OrgMapPin) template.JS {
		if len(pins) == 0 {
			return template.JS("[]")
		}
		b, _ := json.Marshal(pins)
		return template.JS(b)
	},
	"limitTags": func(hasBall, hasWorkshop, hasFestival bool, tags []string) []string {
		typeCount := 0
		if hasBall {
			typeCount++
		}
		if hasWorkshop {
			typeCount++
		}
		if hasFestival {
			typeCount++
		}
		limit := 5 - typeCount
		if limit < 0 {
			limit = 0
		}
		if len(tags) <= limit {
			return tags
		}
		return tags[:limit]
	},
	"hiddenTagCount": func(hasBall, hasWorkshop, hasFestival bool, tags []string) int {
		typeCount := 0
		if hasBall {
			typeCount++
		}
		if hasWorkshop {
			typeCount++
		}
		if hasFestival {
			typeCount++
		}
		limit := 5 - typeCount
		if limit < 0 {
			limit = 0
		}
		if len(tags) <= limit {
			return 0
		}
		return len(tags) - limit
	},
	"orgName": func(orgMap map[int]Organization, id *int) string {
		if id == nil {
			return ""
		}
		if o, ok := orgMap[*id]; ok {
			return o.Name
		}
		return ""
	},
	"orgSlug": orgSlug,
	"checkinColor": func(status string) string {
		switch status {
		case "approved", "checked_in":
			return "green"
		case "confirmed":
			return "amber"
		default:
			return "red"
		}
	},
	"checkinIcon": func(status string) string {
		switch status {
		case "approved", "checked_in":
			return "✓"
		case "confirmed":
			return "?"
		default:
			return "✗"
		}
	},
	"capPct": func(approved, total int) int {
		if total <= 0 {
			return 0
		}
		pct := approved * 100 / total
		if pct > 100 {
			return 100
		}
		return pct
	},
	"markdownHTML": func(s string) template.HTML {
		var buf bytes.Buffer
		if err := goldmark.Convert([]byte(s), &buf); err != nil {
			return template.HTML(template.HTMLEscapeString(s))
		}
		return template.HTML(sanitizeMarkdownHTML(buf.String()))
	},
	"jsonLines": func(s string) string {
		if s == "" {
			return ""
		}
		var arr []string
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			return s
		}
		return strings.Join(arr, "\n")
	},
	"countryList": func(events []Event) []string {
		seen := make(map[string]bool)
		var out []string
		for _, e := range events {
			if e.LocationCountry != "" && !seen[e.LocationCountry] {
				seen[e.LocationCountry] = true
				out = append(out, e.LocationCountry)
			}
		}
		sort.Strings(out)
		return out
	},
	"sourceDomain": func(actorID string) string {
		u, err := url.Parse(actorID)
		if err != nil || u.Host == "" {
			return actorID
		}
		return u.Host
	},
	"splitComma": func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	},
	"hasPrefix": strings.HasPrefix,
	"fmtBytes": func(b int64) string {
		const unit = 1024
		if b < unit {
			return fmt.Sprintf("%d B", b)
		}
		div, exp := int64(unit), 0
		for n := b / unit; n >= unit; n /= unit {
			div *= unit
			exp++
		}
		return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
	},
}

type Templates struct {
	index              *template.Template
	event              *template.Template
	org                *template.Template
	login              *template.Template
	settings           *template.Template
	verify             *template.Template
	bookingVerify      *template.Template
	checkin            *template.Template
	adminUsers         *template.Template
	adminBookings      *template.Template
	adminOrgs          *template.Template
	adminOrgEdit       *template.Template
	adminFetchurls     *template.Template
	adminFetchurlNew   *template.Template
	adminFetchurlEdit  *template.Template
	adminLocations     *template.Template
	adminLocationEdit  *template.Template
	musicians          *template.Template
	musician           *template.Template
	adminMusicians     *template.Template
	adminMusicianEdit  *template.Template
	adminEvents        *template.Template
	adminEventNew      *template.Template
	adminEventEdit     *template.Template
	adminDances        *template.Template
	adminSiteConfig    *template.Template
	adminInfo          *template.Template
	impressum          *template.Template
	orgs               *template.Template
}

func loadTemplates() *Templates {
	load := func(page string) *template.Template {
		t, err := template.New("base").Funcs(tmplFuncMap).ParseFS(templateFS,
			"templates/base.html", "templates/"+page+".html")
		if err != nil {
			log.Fatalf("load template %s: %v", page, err)
		}
		return t
	}
	return &Templates{
		index:             load("index"),
		event:             load("event"),
		org:               load("org"),
		login:             load("login"),
		settings:          load("settings"),
		verify:            load("verify"),
		bookingVerify:     load("booking_verify"),
		checkin:           load("checkin"),
		adminUsers:        load("admin_users"),
		adminBookings:     load("admin_bookings"),
		adminOrgs:         load("admin_orgs"),
		adminOrgEdit:      load("admin_org_edit"),
		adminFetchurls:    load("admin_fetchurls"),
		adminFetchurlNew:  load("admin_fetchurl_new"),
		adminFetchurlEdit: load("admin_fetchurl_edit"),
		adminLocations:    load("admin_locations"),
		adminLocationEdit: load("admin_location_edit"),
		musicians:         load("musicians"),
		musician:          load("musician"),
		adminMusicians:    load("admin_musicians"),
		adminMusicianEdit: load("admin_musician_edit"),
		adminEvents:       load("admin_events"),
		adminEventNew:     load("admin_event_new"),
		adminEventEdit:    load("admin_event_edit"),
		adminDances:       load("admin_dances"),
		adminSiteConfig:   load("admin_site_config"),
		adminInfo:         load("admin_info"),
		impressum:         load("impressum"),
		orgs:              load("orgs"),
	}
}

func federatedEventHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		rows, err := db.QueryContext(r.Context(),
			"SELECT url FROM federated_events WHERE id = ?", id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer rows.Close()
		if !rows.Next() {
			http.NotFound(w, r)
			return
		}
		var eventURL string
		rows.Scan(&eventURL)
		if eventURL == "" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, eventURL, http.StatusFound)
	}
}

func renderTemplate(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("template error: %v", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func indexHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var events []Event
		var orgs []Organization
		var dances []Dance
		var fetchErr error
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); events, fetchErr = client.GetEvents(r.Context(), "") }()
		go func() { defer wg.Done(); orgs, _ = client.GetOrganizations(r.Context()) }()
		go func() { defer wg.Done(); dances, _ = client.GetDances(r.Context()) }()
		wg.Wait()
		if fetchErr != nil {
			http.Error(w, "could not load events", http.StatusBadGateway)
			return
		}
		orgMap := make(map[int]Organization, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o
		}
		var fedEvents []FederatedEvent
		if cfg.ShowFederatedEvents {
			fedEvents, _ = listFederatedEvents(db)
		}
		title := i18n.T(r, "events_title")
		renderTemplate(w, tmpls.index, tmplData(r, cfg, i18n, title, IndexData{Events: events, OrgMap: orgMap, FederatedEvents: fedEvents, Dances: dances}))
	}
}

func eventHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		event, err := client.GetEvent(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		var org *Organization
		var slug string
		if event.OrganizationID != nil {
			o, err := client.GetOrganization(r.Context(), *event.OrganizationID)
			if err == nil {
				org = &o
				slug = effectiveSlug(o)
			}
		}

		posts, _ := client.GetContactPosts(r.Context(), id)

		canManage := false
		if su := getSessionUser(r); su != nil {
			if su.Role == "admin" {
				canManage = true
			} else if event.OrganizationID != nil {
				token := getSessionToken(r)
				members, err := client.GetOrganizationMembers(r.Context(), *event.OrganizationID, token)
				if err == nil {
					for _, m := range members {
						if m.UserID == su.ID {
							canManage = true
							break
						}
					}
				}
			}
		}

		boardPosted := r.URL.Query().Get("board_posted") == "1"
		boardContacted := r.URL.Query().Get("board_contacted") == "1"
		boardError := r.URL.Query().Get("board_error")
		bookingOK := r.URL.Query().Get("book_ok") == "1"
		bookingError := r.URL.Query().Get("book_error")

		renderTemplate(w, tmpls.event, tmplData(r, cfg, i18n, event.Title, EventData{
			Event:          event,
			Org:            org,
			OrgSlug:        slug,
			ContactPosts:   posts,
			CanManageBoard: canManage,
			BoardPosted:    boardPosted,
			BoardContacted: boardContacted,
			BoardError:     boardError,
			BookingOK:      bookingOK,
			BookingError:   bookingError,
		}))
	}
}

func orgFrontendHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("name")

		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			orgs, oErr := client.GetOrganizations(r.Context())
			if oErr != nil {
				http.Error(w, "upstream error", http.StatusBadGateway)
				return
			}
			for _, o := range orgs {
				if effectiveSlug(o) == slug {
					actor, err = ensureActor(db, o.ID, slug)
					break
				}
			}
			if actor == nil {
				http.NotFound(w, r)
				return
			}
		} else if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		org, err := client.GetOrganization(r.Context(), actor.OrgID)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		allEvents, _ := client.GetAllEventsByOrg(r.Context(), actor.OrgID)
		musicians, _ := client.GetMusiciansByOrg(r.Context(), actor.OrgID)
		followerCount, _ := countFollowers(db, actor.OrgID)

		now := time.Now()
		var upcoming, past []Event
		for _, e := range allEvents {
			if t, err2 := time.Parse(time.RFC3339, e.EndTime); err2 == nil && t.Before(now) {
				past = append(past, e)
			} else {
				upcoming = append(upcoming, e)
			}
		}
		// Past events: most recent first
		for i, j := 0, len(past)-1; i < j; i, j = i+1, j-1 {
			past[i], past[j] = past[j], past[i]
		}

		handle := "@" + slug + "@" + cfg.Domain
		renderTemplate(w, tmpls.org, tmplData(r, cfg, i18n, org.Name, OrgData{
			Org:            org,
			UpcomingEvents: upcoming,
			PastEvents:     past,
			AllEvents:      allEvents,
			Musicians:      musicians,
			Slug:           slug,
			Handle:         handle,
			FollowerCount:  followerCount,
		}))
	}
}

func orgNameByID(orgs []Organization, id int) string {
	for _, o := range orgs {
		if o.ID == id {
			return o.Name
		}
	}
	return ""
}

func orgsHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var orgs []Organization
		var statMap map[int]OrgStatRecord
		var locs []Location
		var orgsErr error
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); orgs, orgsErr = client.GetOrganizations(r.Context()) }()
		go func() { defer wg.Done(); statMap, _ = client.GetOrgStats(r.Context()) }()
		go func() { defer wg.Done(); locs, _ = client.GetLocations(r.Context()) }()
		wg.Wait()
		if orgsErr != nil {
			http.Error(w, "could not load organizations", http.StatusBadGateway)
			return
		}

		actorSlugs, _ := listOrgActorSlugs(db)

		orgSlugMap := make(map[int]string, len(orgs))
		for _, o := range orgs {
			orgSlugMap[o.ID] = effectiveSlug(o)
		}

		firstTown := map[int]string{}
		var mapPins []OrgMapPin
		for _, l := range locs {
			for _, id := range l.OrganizationIDs {
				if firstTown[id] == "" && l.Town != "" {
					firstTown[id] = l.Town
				}
				if l.Latitude != nil && l.Longitude != nil {
					locName := l.ShortName
					if locName == "" {
						locName = l.Location
					}
					mapPins = append(mapPins, OrgMapPin{
						Lat:     *l.Latitude,
						Lng:     *l.Longitude,
						OrgName: orgNameByID(orgs, id),
						OrgSlug: orgSlugMap[id],
						LocName: locName,
					})
				}
			}
		}

		items := make([]OrgListItem, len(orgs))
		for i, o := range orgs {
			st := statMap[o.ID]
			slug := effectiveSlug(o)
			fedHandle := ""
			if _, ok := actorSlugs[o.ID]; ok {
				fedHandle = "@" + slug + "@" + cfg.Domain
			}
			items[i] = OrgListItem{
				Org:           o,
				Slug:          slug,
				EventCount:    st.EventCount,
				LocationCount: st.LocationCount,
				FirstTown:     firstTown[o.ID],
				FedHandle:     fedHandle,
			}
		}
		title := i18n.T(r, "orgs_title")
		renderTemplate(w, tmpls.orgs, tmplData(r, cfg, i18n, title, OrgsListData{Items: items, MapPins: mapPins}))
	}
}

func actorOrFrontendHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	frontendH := orgFrontendHandler(cfg, tmpls, db, client, i18n)
	apH := apActorHandler(cfg, db, client)
	return func(w http.ResponseWriter, r *http.Request) {
		if isAPRequest(r) {
			apH(w, r)
		} else {
			frontendH(w, r)
		}
	}
}

func apActorHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("name")
		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			if slug == "relay" {
				writeJSONError(w, http.StatusNotFound, "actor not found")
				return
			}
			orgs, err := client.GetOrganizations(r.Context())
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "upstream error")
				return
			}
			for _, org := range orgs {
				if effectiveSlug(org) == slug {
					actor, err = ensureActor(db, org.ID, slug)
					if err != nil {
						writeJSONError(w, http.StatusInternalServerError, "actor init error")
						return
					}
					break
				}
			}
			if actor == nil {
				writeJSONError(w, http.StatusNotFound, "actor not found")
				return
			}
		} else if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Relay actor: synthetic profile with no backing org
		if actor.OrgID == 0 {
			base := actorURL(cfg, "relay")
			a := Actor{
				Context:                   APContext,
				Type:                      "Application",
				ID:                        base,
				Name:                      "relay@" + cfg.Domain,
				URL:                       "https://" + cfg.Domain,
				PreferredUsername:         "relay",
				Inbox:                     base + "/inbox",
				Outbox:                    base + "/outbox",
				Followers:                 base + "/followers",
				ManuallyApprovesFollowers: false,
				Discoverable:              true,
				Indexable:                 true,
				Endpoints:                 &APEndpoints{SharedInbox: "https://" + cfg.Domain + "/inbox"},
				PublicKey: PublicKey{
					ID:           base + "#main-key",
					Owner:        base,
					PublicKeyPem: actor.PublicKeyPEM,
				},
			}
			writeJSON(w, http.StatusOK, a)
			return
		}

		org, err := client.GetOrganization(r.Context(), actor.OrgID)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "org not found")
			return
		}

		a := actorFromOrg(cfg, org, actor)
		writeJSON(w, http.StatusOK, a)
	}
}

func impressumHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc := loadPagesContent(cfg.PagesFile)
		lang := i18n.detectLang(r)
		body := pc.ImpressumHTML(lang)
		if body == "" {
			http.NotFound(w, r)
			return
		}
		title := i18n.T(r, "nav_impressum")
		renderTemplate(w, tmpls.impressum, tmplData(r, cfg, i18n, title, body))
	}
}
