package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
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

type webContextKey int

const ctxDashboardAttention webContextKey = 1

type TemplateData struct {
	Title                  string
	Domain                 string
	SiteName               string // display name; defaults to Domain when empty
	User                   *SessionUser
	Strings                I18nStrings
	LangCode               string
	Languages              []LangOption
	Contact                string
	ImpressumURL           string
	Data                   any
	BannerHeight           int
	LogoHeight             int
	DarkMode               string // "auto", "light", or "dark"
	TimeFormat             string // "24h" or "12h"
	AppVersion             string
	AppBuildTime           string
	SuggestAvailable       bool
	RegistrationEnabled    bool
	SessionIdleTimeoutMins int
	PendingRegCount        int    // verified pending registrations awaiting action (scoped to caller)
	PendingSuggestionCount int    // unpublished events awaiting review (scoped to caller)
	PossibleDuplicateCount int    // events flagged as possible duplicates (scoped to caller)
	Path                   string // current request path, for building "return to this page" links
	CanonicalURL           string // absolute canonical URL for this page
	MetaDescription        string // page-specific meta description (falls back to i18n string in template)
	OGImage                string // absolute URL of the primary image for OG/Twitter card
	GoogleSiteVerification string
	BingSiteVerification   string
}

// dashboardAttentionMiddleware fetches the scoped "needs attention" counts for
// logged-in admin/user roles and stores them in the request context so tmplData
// can inject them into every rendered page without each handler needing to ask.
func dashboardAttentionMiddleware(client *DansalClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			su := getSessionUser(r)
			if su != nil && (su.Role == "admin" || su.Role == "user") {
				token := getSessionToken(r)
				if att, err := client.GetDashboardAttention(r.Context(), token); err == nil {
					r = r.WithContext(context.WithValue(r.Context(), ctxDashboardAttention, att))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// dashAttention returns the request-scoped "needs attention" counts stashed
// by dashboardAttentionMiddleware, or a zero value when unset (public/publisher).
func dashAttention(r *http.Request) DashboardAttention {
	if v, ok := r.Context().Value(ctxDashboardAttention).(DashboardAttention); ok {
		return v
	}
	return DashboardAttention{}
}

func tmplData(r *http.Request, cfg *Config, i18n *I18n, title string, data any) TemplateData {
	lang := i18n.detectLang(r)
	contact := siteCfg.Contact()
	if contact == "" {
		contact = cfg.pagesContent.ContactText(lang)
	}
	imp := siteCfg.Impressum()
	impressumURL := ""
	if imp[lang] != "" || cfg.pagesContent.ImpressumText(lang) != "" {
		impressumURL = "/impressum"
	}
	isMain := r.URL.Path == "/"
	bannerHeight := cfg.BannerHeightSub
	logoHeight := cfg.LogoHeightSub
	if isMain {
		bannerHeight = cfg.BannerHeightMain
		logoHeight = cfg.LogoHeightMain
	}
	siteName := siteCfg.SiteName()
	if siteName == "" {
		siteName = cfg.SiteName // YAML fallback
	}
	if siteName == "" {
		siteName = cfg.Domain
	}
	strs := i18n.Strings(lang)

	canonical := "https://" + cfg.Domain + r.URL.Path

	return TemplateData{
		Title:                  title,
		Domain:                 cfg.Domain,
		SiteName:               siteName,
		User:                   getSessionUser(r),
		Strings:                strs,
		LangCode:               lang,
		Languages:              i18n.Options(lang),
		Contact:                contact,
		ImpressumURL:           impressumURL,
		Data:                   data,
		BannerHeight:           bannerHeight,
		LogoHeight:             logoHeight,
		DarkMode:               cfg.DarkMode,
		TimeFormat:             cfg.timeFormat(),
		AppVersion:             Version,
		AppBuildTime:           BuildTime,
		SuggestAvailable:       suggestAvailable(cfg),
		RegistrationEnabled:    registrationEnabled(cfg),
		SessionIdleTimeoutMins: cfg.SessionIdleTimeoutMins,
		PendingRegCount:        dashAttention(r).PendingRegistrations,
		PendingSuggestionCount: dashAttention(r).PendingEventSuggestions,
		PossibleDuplicateCount: dashAttention(r).PossibleDuplicates,
		Path:                   r.URL.Path,
		CanonicalURL:           canonical,
		GoogleSiteVerification: cfg.GoogleSiteVerification,
		BingSiteVerification:   cfg.BingSiteVerification,
		OGImage:                "https://" + cfg.Domain + "/banner.avif",
	}
}

// metaDesc returns the first maxLen chars of s with markdown syntax stripped,
// suitable for use as a meta description or OG description.
func metaDesc(s string, maxLen int) string {
	// strip markdown: links, bold/italic, headings, list markers
	s = reMetaMD.ReplaceAllString(s, "$1")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimSpace(s)
	if len([]rune(s)) > maxLen {
		runes := []rune(s)[:maxLen]
		s = string(runes[:strings.LastIndex(string(runes), " ")]) + "…"
	}
	return s
}

var reMetaMD = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)|[*_~` + "`" + `#>]+`)

var tagDisplayNames = map[string]string{
	"bal-folk":          "Bal-folk",
	"fest-noz":          "Fest-noz",
	"session":           "Session",
	"concert":           "Concert",
	"festival":          "Festival",
	"open-air":          "Open-air",
	"workshop":          "Workshop",
	"music-course":      "Music course",
	"dance-workshop":    "Dance workshop",
	"musician-workshop": "Musician workshop",
}

// eventMetaDesc returns a concise meta description for an event page.
// If the event has a description, it is used (markdown-stripped). Otherwise
// a unique description is assembled from structured fields (type tag, date,
// location, musicians/instructors) so that every page gets a distinct value.
func eventMetaDesc(event Event, lang string) string {
	if event.Description != "" {
		return metaDesc(event.Description, 155)
	}

	var parts []string

	// Lead with format/type tag if present.
	for _, tag := range event.Tags {
		if name, ok := tagDisplayNames[tag]; ok {
			// Append level qualifier if any.
			for _, t2 := range event.Tags {
				switch t2 {
				case "beginners", "intermediate", "advanced":
					name += " (" + t2 + ")"
				}
			}
			parts = append(parts, name)
			break
		}
	}
	if len(parts) == 0 {
		parts = append(parts, event.Title)
	}

	// Date.
	if t, ok := parseTime(event.StartTime); ok {
		mo := locMonth(lang, t.Month())
		var d string
		if lang == "de" {
			d = fmt.Sprintf("%02d. %s %d", t.Day(), mo, t.Year())
		} else {
			d = fmt.Sprintf("%02d %s %d", t.Day(), mo, t.Year())
		}
		parts = append(parts, d)
	}

	// Location name + city.
	if event.Location != nil {
		loc := event.Location.Location
		if event.Location.Town != "" {
			loc += ", " + event.Location.Town
		}
		parts = append(parts, loc)
	}

	desc := strings.Join(parts, " · ")

	// Musicians (up to 3).
	if len(event.Musicians) > 0 {
		names := make([]string, 0, min(3, len(event.Musicians)))
		for i, m := range event.Musicians {
			if i >= 3 {
				break
			}
			names = append(names, m.Bandname)
		}
		suffix := ""
		if len(event.Musicians) > 3 {
			suffix = "…"
		}
		desc += ". " + strings.Join(names, ", ") + suffix
	}

	// Instructors (up to 3).
	if len(event.Instructors) > 0 {
		names := make([]string, 0, min(3, len(event.Instructors)))
		for i, inst := range event.Instructors {
			if i >= 3 {
				break
			}
			names = append(names, inst.Name)
		}
		suffix := ""
		if len(event.Instructors) > 3 {
			suffix = "…"
		}
		desc += ". " + strings.Join(names, ", ") + suffix
	}

	if len([]rune(desc)) > 155 {
		runes := []rune(desc)[:155]
		desc = string(runes[:strings.LastIndex(string(runes), " ")]) + "…"
	}
	return desc
}

type IndexData struct {
	Events          []Event
	TotalEvents     int // true server-side count; may exceed len(Events) when the API's pagination cap truncated the result
	OrgMap          map[int]Organization
	TagMap          map[string]Tag
	FederatedEvents []FederatedEvent
	Dances          []Dance
	HolidayDates    template.JS // JSON array of "YYYY-MM-DD" strings
}

type EventData struct {
	Event             Event
	Org               *Organization
	OrgSlug           string
	TagMap            map[string]Tag
	ContactPosts      []ContactPost
	CanManageBoard    bool
	BoardPosted       bool
	BoardTelegramURL  string
	BoardContacted    bool
	BoardContactTgURL string
	BoardError        string
	BookingOK         bool
	BookingError      string
	UserOrgs          []Organization
	BookFormToken     string
	BoardFormToken    string
	PrevEvent         *Event
	NextEvent         *Event
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

type LocationPageData struct {
	Location Location
	Events   []Event
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

//go:embed static/logo.avif
var logoAVIF []byte

//go:embed static/banner.avif
var bannerAVIF []byte

//go:embed static/qrcode.min.js
var qrcodeJS []byte

func suggestAvailable(cfg *Config) bool {
	return cfg.SMTPHost != "" || cfg.SMTPSendmail != "" || cfg.TelegramBotToken != ""
}

// registrationEnabled returns whether self-registration is available.
// It is decoupled from suggestAvailable so instances without SMTP/Telegram
// can still accept passkey-only registrations.
func registrationEnabled(_ *Config) bool {
	return true
}

func svgHandler(data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Type", "image/svg+xml")
		// For embedded SVG fallback, prefer gzip when client accepts it and
		// Save-Data isn't set. Serve on-the-fly gzip to avoid shipping a separate
		// precompressed asset in the repo.
		if !saveDataOn(r) && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gw := gzip.NewWriter(w)
			defer gw.Close()
			gw.Write(data)
			return
		}
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

func formatDateStr(lang, s string) string {
	t, ok := parseTime(s)
	if !ok {
		return s
	}
	mo := locMonth(lang, t.Month())
	if lang == "de" {
		return fmt.Sprintf("%02d. %s %d", t.Day(), mo, t.Year())
	}
	return fmt.Sprintf("%02d %s %d", t.Day(), mo, t.Year())
}

var parseLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}

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

func validMatrixID(s string) bool {
	if !strings.HasPrefix(s, "@") {
		return false
	}
	colon := strings.IndexByte(s, ':')
	return colon > 1 && colon < len(s)-1
}

// emailLocal returns the local part of an email address (before @).
func emailLocal(email string) string {
	if idx := strings.Index(email, "@"); idx > 0 {
		return email[:idx]
	}
	return email
}

// displayNameOrEmail returns u.DisplayName if non-empty, else the email local part.
func displayNameOrEmail(u UserInfo) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return emailLocal(u.Email)
}

// geoEvent is the compact event projection used for map markers, both in the
// initial page's #events-geo script tag and the /api/events-more incremental fetch.
// Excludes fields the map doesn't need (geohash, osm_id, notes_md, aliases, contact_*, etc).
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
	Session            bool    `json:"sess,omitempty"`
	Concert            bool    `json:"conc,omitempty"`
	Cancelled          bool    `json:"x,omitempty"`
	Availability       string  `json:"av,omitempty"`
	BookingEnabled     bool    `json:"book,omitempty"`
	Fee                string  `json:"fee,omitempty"` // "free"|"donation"|"paid"|""
	Food               string  `json:"food,omitempty"`
	Drink              string  `json:"drink,omitempty"`
	Wheelchair         bool    `json:"wc,omitempty"`
	HearingLoop        bool    `json:"hl,omitempty"`
}

// eventsToGeo projects events to geoEvent, skipping any without coordinates.
func eventsToGeo(events []Event) []geoEvent {
	var geo []geoEvent
	for _, e := range events {
		if e.Location == nil || e.Location.Latitude == nil || e.Location.Longitude == nil || (*e.Location.Latitude == 0 && *e.Location.Longitude == 0) {
			continue
		}
		lat := *e.Location.Latitude
		lng := *e.Location.Longitude
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
		// Merge location + event attributes for accessibility flags (event overrides location).
		merged := map[string]bool{}
		if e.Location != nil {
			for k, v := range e.Location.Attributes {
				merged[k] = v
			}
		}
		for k, v := range e.Attributes {
			merged[k] = v
		}
		var locName, locShortName, locTown, locCountry string
		if l := e.Location; l != nil {
			locName, locShortName, locTown, locCountry = l.Location, l.ShortName, l.Town, l.Country
		}
		var hasSession, hasConcert bool
		for _, tag := range e.Tags {
			if tag == "session" {
				hasSession = true
			} else if tag == "concert" {
				hasConcert = true
			}
		}
		geo = append(geo, geoEvent{
			ID: e.ID, Title: e.Title, Start: e.StartTime, End: end,
			Location: locName, ShortName: locShortName, Town: locTown, Country: locCountry,
			Lat: lat, Lng: lng, URL: e.URL,
			Ball: e.HasBall, Workshop: e.HasWorkshop, WorkshopDifficulty: e.WorkshopDifficulty,
			Festival: e.HasFestival, Session: hasSession, Concert: hasConcert,
			Cancelled: e.IsCancelled, Availability: e.Availability,
			BookingEnabled: e.BookingEnabled,
			Fee:            fee, Food: e.Food, Drink: e.Drink,
			Wheelchair: merged["wheelchair"], HearingLoop: merged["hearing_loop"],
		})
	}
	return geo
}

// fetchAndRenderEventRows fetches events via fetchEvents (run concurrently with
// the org/tag lookups needed to render them) and renders each one as an
// "event-row" <tr> through tmpl, so any handler producing incremental event
// batches (cmd/dansal_web/events_more.go, search.go) renders identically to the
// page's initial server-rendered rows.
func fetchAndRenderEventRows(r *http.Request, tmpl *template.Template, i18n *I18n, client *DansalClient, fetchEvents func() ([]Event, error)) ([]Event, string, error) {
	var events []Event
	var orgs []Organization
	var tagMap map[string]Tag
	var fetchErr error
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); events, fetchErr = fetchEvents() }()
	go func() { defer wg.Done(); orgs, _ = client.GetOrganizations(r.Context()) }()
	go func() { defer wg.Done(); tagMap, _ = client.GetTagMap(r.Context()) }()
	wg.Wait()
	if fetchErr != nil {
		return nil, "", fetchErr
	}

	orgMap := make(map[int]Organization, len(orgs))
	for _, o := range orgs {
		orgMap[o.ID] = o
	}
	strs := i18n.Strings(i18n.detectLang(r))

	var rowsHTML strings.Builder
	for _, e := range events {
		tmpl.ExecuteTemplate(&rowsHTML, "event-row", map[string]any{
			"Event": e, "OrgMap": orgMap, "TagMap": tagMap, "Strings": strs,
		})
	}
	return events, rowsHTML.String(), nil
}

// fmtClock formats an hour/minute pair in 24h ("13:00") or 12h ("1:00 PM") notation.
func fmtClock(timeFormat string, h, m int) string {
	if timeFormat == "12h" {
		ampm := "AM"
		if h >= 12 {
			ampm = "PM"
		}
		if h > 12 {
			h -= 12
		} else if h == 0 {
			h = 12
		}
		return fmt.Sprintf("%d:%02d %s", h, m, ampm)
	}
	return fmt.Sprintf("%02d:%02d", h, m)
}

// parseTimetableClock parses an "HH:MM" timetable time into minutes since
// midnight. Timetable start/end times are always validated to this exact
// format server-side (cmd/dansal/timetable.go, validTimeSlot), so failure
// here only happens for legacy/corrupt data.
func parseTimetableClock(s string) (int, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// TimetableDay groups timetable entries under the calendar date they
// belong to, for multi-day events (festivals, workshop weekends, #894).
type TimetableDay struct {
	Date    string // YYYY-MM-DD
	Entries []TimetableEntry
}

// timetableDays splits entries into day buckets covering every calendar day
// of the event's own start/end range — not just the days that happen to
// have dated entries — so a multi-day event always shows a section per day
// (e.g. day 1 with everything still undated, day 2 empty until entries get
// assigned a date via the admin picker) rather than collapsing everything
// into a single block. An entry without its own EntryDate belongs to the
// event's start date; single-day events always yield exactly one day (no
// visible change from before #894).
func timetableDays(entries []TimetableEntry, eventStart, eventEnd string) []TimetableDay {
	startDate := eventStart
	if t, ok := parseTime(eventStart); ok {
		startDate = t.Format("2006-01-02")
	}
	endDate := startDate
	if t, ok := parseTime(eventEnd); ok {
		endDate = t.Format("2006-01-02")
	}

	byDate := map[string][]TimetableEntry{}
	for _, e := range entries {
		d := strings.TrimSpace(e.EntryDate)
		if d == "" {
			d = startDate
		}
		byDate[d] = append(byDate[d], e)
	}

	var order []string
	seen := map[string]bool{}
	st, errSt := time.Parse("2006-01-02", startDate)
	en, errEn := time.Parse("2006-01-02", endDate)
	if errSt == nil && errEn == nil && !en.Before(st) {
		for d := st; !d.After(en); d = d.AddDate(0, 0, 1) {
			ds := d.Format("2006-01-02")
			order = append(order, ds)
			seen[ds] = true
		}
	}
	// Entries dated outside the event's own range (shouldn't normally
	// happen — the admin picker only offers in-range dates — but must not
	// be silently dropped if it does) get appended as trailing days.
	var extra []string
	for d := range byDate {
		if !seen[d] {
			extra = append(extra, d)
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)

	days := make([]TimetableDay, 0, len(order))
	for _, d := range order {
		days = append(days, TimetableDay{Date: d, Entries: byDate[d]})
	}
	return days
}

// timetableColumnKey returns the grouping key/label/other-flag for one
// timetable entry, shared by timetableGrid's column bucketing.
func timetableColumnKey(e TimetableEntry) (key, label string, isOther bool) {
	switch {
	case e.LocationID != nil:
		return fmt.Sprintf("loc:%d", *e.LocationID), e.LocationName, false
	case strings.TrimSpace(e.Room) != "":
		label := strings.TrimSpace(e.Room)
		return "room:" + strings.ToLower(label), label, false
	default:
		return "other", "", true
	}
}

func timetableGrid(entries []TimetableEntry) TimetableGrid {
	const minPxPerMin = 1.4
	const maxPxPerMin = 4.0
	const minTotalHeightPx = 220.0

	rangeMin, rangeMax := 0, 0
	haveRange := false
	type parsed struct {
		entry            TimetableEntry
		startMin, endMin int
	}
	var parsedEntries []parsed
	for _, e := range entries {
		start, ok1 := parseTimetableClock(e.StartTime)
		end, ok2 := parseTimetableClock(e.EndTime)
		if !ok1 || !ok2 {
			continue
		}
		if end <= start {
			end += 24 * 60 // crosses midnight (e.g. a fest-noz running past 00:00)
		}
		parsedEntries = append(parsedEntries, parsed{entry: e, startMin: start, endMin: end})
		if !haveRange || start < rangeMin {
			rangeMin = start
		}
		if !haveRange || end > rangeMax {
			rangeMax = end
		}
		haveRange = true
	}
	if !haveRange {
		return TimetableGrid{}
	}

	// Pick a mark step from the raw range before rounding, then round the
	// range itself out to that step so the axis starts/ends on a mark.
	step := 60
	if rangeMax-rangeMin <= 180 {
		step = 30
	}
	rangeMin -= rangeMin % step
	if r := rangeMax % step; r != 0 {
		rangeMax += step - r
	}
	totalMin := rangeMax - rangeMin
	if totalMin <= 0 {
		totalMin = step
		rangeMax = rangeMin + step
	}

	pxPerMin := minPxPerMin
	if h := float64(totalMin) * pxPerMin; h < minTotalHeightPx {
		pxPerMin = minTotalHeightPx / float64(totalMin)
	}
	if pxPerMin > maxPxPerMin {
		pxPerMin = maxPxPerMin
	}

	grid := TimetableGrid{HeightPx: float64(totalMin) * pxPerMin}
	for m := rangeMin; m <= rangeMax; m += step {
		grid.Marks = append(grid.Marks, TimetableGridMark{
			Label: fmt.Sprintf("%02d:%02d", (m/60)%24, m%60),
			TopPx: float64(m-rangeMin) * pxPerMin,
		})
	}

	colIdx := map[string]int{}
	for _, p := range parsedEntries {
		key, label, isOther := timetableColumnKey(p.entry)
		i, ok := colIdx[key]
		if !ok {
			grid.Columns = append(grid.Columns, TimetableGridColumn{Label: label, IsOther: isOther})
			i = len(grid.Columns) - 1
			colIdx[key] = i
		}
		grid.Columns[i].Panels = append(grid.Columns[i].Panels, TimetablePanel{
			Entry:    p.entry,
			TopPx:    float64(p.startMin-rangeMin) * pxPerMin,
			HeightPx: float64(p.endMin-p.startMin) * pxPerMin,
		})
	}
	return grid
}

var tmplFuncMap = template.FuncMap{
	"formatTime": func(lang, timeFormat, s string) string {
		t, ok := parseTime(s)
		if !ok {
			return s
		}
		wd := locWeekday(lang, t.Weekday())
		mo := locMonth(lang, t.Month())
		clock := fmtClock(timeFormat, t.Hour(), t.Minute())
		if lang == "de" {
			return fmt.Sprintf("%s %02d. %s %d, %s", wd, t.Day(), mo, t.Year(), clock)
		}
		return fmt.Sprintf("%s %02d %s %d, %s", wd, t.Day(), mo, t.Year(), clock)
	},
	"formatDate": func(lang, s string) string {
		return formatDateStr(lang, s)
	},
	"isoDate": func(s string) string {
		if t, ok := parseTime(s); ok {
			return t.Format("2006-01-02")
		}
		return s
	},
	// isoEndDate is like isoDate but treats times between 00:00–04:59 as
	// belonging to the previous calendar day, so late-night event endings
	// don't appear to span into the next day on the weekly calendar.
	"isoEndDate": func(s string) string {
		if t, ok := parseTime(s); ok {
			if t.Hour() < 5 {
				t = t.Add(-24 * time.Hour)
			}
			return t.Format("2006-01-02")
		}
		return s
	},
	"fmtUnix": func(ts int64) string {
		if ts == 0 {
			return ""
		}
		return time.Unix(ts, 0).UTC().Format("2006-01-02")
	},
	"parseChangedAt": parseChangedAt,
	"dict": func(pairs ...interface{}) (map[string]interface{}, error) {
		if len(pairs)%2 != 0 {
			return nil, fmt.Errorf("dict: odd number of arguments")
		}
		m := make(map[string]interface{}, len(pairs)/2)
		for i := 0; i < len(pairs); i += 2 {
			key, ok := pairs[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %v is not a string", pairs[i])
			}
			m[key] = pairs[i+1]
		}
		return m, nil
	},
	"isoTime": func(s string) string {
		if t, ok := parseTime(s); ok {
			return t.Format("15:04")
		}
		return ""
	},
	"formatHourMin": func(timeFormat, s string) string {
		if t, ok := parseTime(s); ok {
			return fmtClock(timeFormat, t.Hour(), t.Minute())
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
	"emailLocal":         emailLocal,
	"displayNameOrEmail": displayNameOrEmail,
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
	// pct renders a 0-1 fraction (e.g. Location.PlanX/PlanY) as a percentage
	// number for use in a CSS "%" value — floatVal alone would render 0.6 as
	// "0.6%" instead of "60%" (#880).
	"pct": func(f *float64) string {
		if f == nil {
			return ""
		}
		return strconv.FormatFloat(*f*100, 'f', -1, 64)
	},
	"int64Val": func(n *int64) string {
		if n == nil {
			return ""
		}
		return strconv.FormatInt(*n, 10)
	},
	// roomName looks up which of a building's rooms (children) an event's
	// LocationID refers to, for the Room column on /admin/location/{id} (#883).
	"roomName": func(children []Location, id *int) string {
		if id == nil {
			return ""
		}
		for _, c := range children {
			if c.ID == *id {
				return c.Location
			}
		}
		return ""
	},
	"derefInt": func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	},
	"intVal": func(p *int) string {
		if p == nil {
			return ""
		}
		return strconv.Itoa(*p)
	},
	// unplacedRooms/placedRooms split a building's Children (#877) by whether
	// they've been dragged onto the building's site-plan image yet.
	"unplacedRooms": func(children []Location) []Location {
		var out []Location
		for _, c := range children {
			if c.PlanX == nil || c.PlanY == nil {
				out = append(out, c)
			}
		}
		return out
	},
	"placedRooms": func(children []Location) []Location {
		var out []Location
		for _, c := range children {
			if c.PlanX != nil && c.PlanY != nil {
				out = append(out, c)
			}
		}
		return out
	},
	"timetableDays": timetableDays,
	// usedRoomIDs collects the distinct real room references (LocationID) across
	// an event's timetable entries — free-text Room strings can't be placed on
	// a site plan, so those entries are ignored (#885).
	"usedRoomIDs": func(entries []TimetableEntry) map[int]bool {
		ids := map[int]bool{}
		for _, e := range entries {
			if e.LocationID != nil {
				ids[*e.LocationID] = true
			}
		}
		return ids
	},
	// timetableGrid groups timetable entries into per-room columns and
	// positions each entry in pixels against one shared time axis, for a
	// real day-view calendar layout on /event/{id} (#887, refines #886's
	// independent-per-column stacked lists). Rooms are grouped primarily by
	// LocationID (a stable reference, labeled by LocationName), falling back
	// to the free-text Room string (trimmed/case-insensitive key) when no
	// LocationID is set, and finally a single shared "other" column for
	// entries with neither. Column order follows first appearance, i.e. the
	// timetable's existing time order.
	//
	// The axis only spans the timetable's own earliest start to latest end
	// (rounded to a mark boundary), not a fixed 24h range. Entries ending
	// before they start (e.g. a fest-noz running past midnight) are treated
	// as ending the next day. Overlapping entries within the same room are a
	// known, deliberately deferred edge case (#888) — this only lays out
	// columns/time, it doesn't detect or resolve overlaps.
	"timetableGrid": timetableGrid,
	// topLocationID resolves the top-level (building) location ID for an
	// event whose location may itself be a room (#687): a room is a child
	// Location with ParentID set, but venue pickers only ever offer the
	// top-level building, so callers select against this instead of
	// Event.LocationID directly.
	"topLocationID": func(e Event) int {
		if e.Location == nil {
			return 0
		}
		if e.Location.ParentID != nil {
			return *e.Location.ParentID
		}
		return e.Location.ID
	},
	"joinInts": func(ids []int) string {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		return strings.Join(parts, ",")
	},
	// locationsJSON flattens top-level locations and their room children into
	// one JS array. Rooms are labelled "RoomName — BuildingName" to disambiguate
	// when two buildings share a room name (mirrors timetableLocationOptionsJSON).
	// Rooms inherit the parent's orgIDs and town for org-based filtering.
	"locationsJSON": func(locs []Location) template.JS {
		type locItem struct {
			ID     int    `json:"id"`
			Label  string `json:"label"`
			Town   string `json:"town"`
			OrgIDs []int  `json:"orgIDs"`
		}
		items := make([]locItem, 0, len(locs))
		for _, l := range locs {
			label := l.Location
			if l.ShortName != "" {
				label = l.ShortName
			}
			bname := label
			if l.Town != "" {
				label += ", " + l.Town
			}
			orgIDs := l.OrganizationIDs
			if orgIDs == nil {
				orgIDs = []int{}
			}
			items = append(items, locItem{ID: l.ID, Label: label, Town: l.Town, OrgIDs: orgIDs})
			for _, c := range l.Children {
				clabel := c.Location
				if c.ShortName != "" {
					clabel = c.ShortName
				}
				childOrgIDs := c.OrganizationIDs
				if len(childOrgIDs) == 0 {
					childOrgIDs = orgIDs
				}
				items = append(items, locItem{ID: c.ID, Label: clabel + " — " + bname, Town: l.Town, OrgIDs: childOrgIDs})
			}
		}
		b, _ := json.Marshal(items)
		return template.JS(b)
	},
	// timetableLocationOptionsJSON flattens every top-level location plus all
	// of their rooms (children) into one searchable option list for the
	// timetable's per-row location autocomplete (#889) — unlike locationsJSON,
	// this isn't restricted to the event's own building. Rooms inherit their
	// building's orgIDs for the existing org-based filtering, since rooms
	// don't carry their own organization assignments.
	"timetableLocationOptionsJSON": func(locs []Location) template.JS {
		type locItem struct {
			ID     int    `json:"id"`
			Label  string `json:"label"`
			OrgIDs []int  `json:"orgIDs"`
		}
		items := []locItem{}
		for _, l := range locs {
			label := l.Location
			if l.ShortName != "" {
				label = l.ShortName
			}
			bname := label
			if l.Town != "" {
				label += ", " + l.Town
			}
			orgIDs := l.OrganizationIDs
			if orgIDs == nil {
				orgIDs = []int{}
			}
			items = append(items, locItem{ID: l.ID, Label: label, OrgIDs: orgIDs})
			for _, c := range l.Children {
				clabel := c.Location
				if c.ShortName != "" {
					clabel = c.ShortName
				}
				items = append(items, locItem{ID: c.ID, Label: clabel + " — " + bname, OrgIDs: orgIDs})
			}
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
		geo := eventsToGeo(events)
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
	"orgMapJSON": func(orgMap map[int]Organization) template.JS {
		out := make(map[string]string, len(orgMap))
		for id, o := range orgMap {
			out[strconv.Itoa(id)] = o.Name
		}
		b, _ := json.Marshal(out)
		return template.JS(b)
	},
	"limitTags": func(tags []string) []string {
		typeCount := 0
		if sliceContains(tags, "bal-folk") || sliceContains(tags, "fest-noz") {
			typeCount++
		}
		if sliceContains(tags, "workshop") || sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop") || sliceContains(tags, "music-course") {
			typeCount++
		}
		if sliceContains(tags, "festival") {
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
	"hiddenTagCount": func(tags []string) int {
		typeCount := 0
		if sliceContains(tags, "bal-folk") || sliceContains(tags, "fest-noz") {
			typeCount++
		}
		if sliceContains(tags, "workshop") || sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop") || sliceContains(tags, "music-course") {
			typeCount++
		}
		if sliceContains(tags, "festival") {
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
	"tagName": func(tagMap map[string]Tag, slug string) string {
		if t, ok := tagMap[slug]; ok {
			return t.Name
		}
		return slug
	},
	"tagKey": func(slug string) string {
		return "tag_" + strings.ReplaceAll(slug, "-", "_")
	},
	"tagCatKey": func(cat string) string {
		return "tag_cat_" + cat
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
	"locAttrs": func(loc *Location) map[string]bool {
		if loc == nil {
			return nil
		}
		return loc.Attributes
	},
	"mergeAttrs": func(loc, evt map[string]bool) map[string]bool {
		merged := make(map[string]bool, len(loc)+len(evt))
		for k, v := range loc {
			merged[k] = v
		}
		for k, v := range evt {
			merged[k] = v
		}
		return merged
	},
	"attrState": func(attrs map[string]bool, key string) string {
		v, ok := attrs[key]
		if !ok {
			return ""
		}
		if v {
			return "1"
		}
		return "0"
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
			if e.Location == nil || e.Location.Country == "" {
				continue
			}
			if !seen[e.Location.Country] {
				seen[e.Location.Country] = true
				out = append(out, e.Location.Country)
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
	"hasPrefix":     strings.HasPrefix,
	"lower":         strings.ToLower,
	"validMatrixID": validMatrixID,
	"hasTag": func(tags []string, slug string) bool {
		for _, t := range tags {
			if t == slug {
				return true
			}
		}
		return false
	},
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
	"friendlyUA": func() func(string) string {
		type rule struct {
			name    string
			pattern *regexp.Regexp
		}
		rules := []rule{
			{"Edge", regexp.MustCompile(`Edg(?:e)?/(\d+)`)},
			{"Opera", regexp.MustCompile(`OPR/(\d+)`)},
			{"Chrome", regexp.MustCompile(`Chrome/(\d+)`)},
			{"Firefox", regexp.MustCompile(`Firefox/(\d+)`)},
			{"Safari", regexp.MustCompile(`Version/(\d+)`)},
		}
		return func(ua string) string {
			if ua == "" {
				return ""
			}
			for _, r := range rules {
				if m := r.pattern.FindStringSubmatch(ua); m != nil {
					return r.name + " " + m[1]
				}
			}
			if len(ua) > 40 {
				return ua[:40] + "…"
			}
			return ua
		}
	}(),
	"friendlyIP": func(ip string) string {
		if ip == "127.0.0.1" || ip == "::1" {
			return "localhost"
		}
		return ip
	},
	"parseUserMetadata": func(s string) map[string]string {
		var m map[string]string
		if s == "" {
			return nil
		}
		json.Unmarshal([]byte(s), &m)
		return m
	},
	"add": func(a, b int) int { return a + b },
	// pagerRange returns page numbers to display, using -1 as an ellipsis sentinel.
	"pagerRange": func(current, total int) []int {
		if total <= 7 {
			pages := make([]int, total)
			for i := range pages {
				pages[i] = i + 1
			}
			return pages
		}
		show := map[int]bool{1: true, total: true}
		for _, p := range []int{current - 1, current, current + 1} {
			if p >= 1 && p <= total {
				show[p] = true
			}
		}
		var sorted []int
		for p := range show {
			sorted = append(sorted, p)
		}
		sort.Ints(sorted)
		var out []int
		for i, p := range sorted {
			if i > 0 && p-sorted[i-1] > 1 {
				out = append(out, -1)
			}
			out = append(out, p)
		}
		return out
	},
}

type Templates struct {
	index                     *template.Template
	search                    *template.Template
	event                     *template.Template
	org                       *template.Template
	location                  *template.Template
	login                     *template.Template
	settings                  *template.Template
	verify                    *template.Template
	bookingVerify             *template.Template
	checkin                   *template.Template
	adminUsers                *template.Template
	adminBookings             *template.Template
	adminOrgs                 *template.Template
	adminOrgEdit              *template.Template
	adminFetchurls            *template.Template
	adminFetchurlNew          *template.Template
	adminFetchurlEdit         *template.Template
	adminLocations            *template.Template
	adminLocationsMaintenance *template.Template
	adminLocationEdit         *template.Template
	musicians                 *template.Template
	musician                  *template.Template
	adminMusicians            *template.Template
	adminMusicianEdit         *template.Template
	instructors               *template.Template
	instructor                *template.Template
	adminInstructors          *template.Template
	adminInstructorEdit       *template.Template
	adminEvents               *template.Template
	adminEventsMaintenance    *template.Template
	adminEventForm            *template.Template
	adminEventsImport         *template.Template
	adminTemplates            *template.Template
	adminTemplateAssign       *template.Template
	adminDances               *template.Template
	adminInfo                 *template.Template
	adminStats                *template.Template
	impressum                 *template.Template
	orgs                      *template.Template
	suggestEvent              *template.Template
	suggestDone               *template.Template
	suggestVerified           *template.Template
	invite                    *template.Template
	register                  *template.Template
	registerDone              *template.Template
	registerVerified          *template.Template
	adminRegistrations        *template.Template
	adminManagement           *template.Template
	adminRecentChanges        *template.Template
	help                      *template.Template
	contactManage             *template.Template
	board                     *template.Template
	adminSeries               *template.Template
	adminSeriesNew            *template.Template
	adminSeriesEdit           *template.Template
	adminEnrich               *template.Template
	adminOrgDashboard         *template.Template
	adminLocationDashboard    *template.Template
	adminInstructorDashboard  *template.Template
	adminMusicianDashboard    *template.Template
	seriesToken               *template.Template
	embedEvents               *template.Template
	embedEvent                *template.Template
	embedOrg                  *template.Template
	embedNext                 *template.Template
	embedCalendar             *template.Template
	embedLocations            *template.Template
	dashboard                 *template.Template
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
	// Standalone embed templates — no base.html wrapper.
	loadEmbed := func(page string) *template.Template {
		t, err := template.New(page).Funcs(tmplFuncMap).ParseFS(templateFS,
			"templates/"+page+".html")
		if err != nil {
			log.Fatalf("load embed template %s: %v", page, err)
		}
		named := t.Lookup(page + ".html")
		if named == nil {
			log.Fatalf("load embed template %s: no template named %q", page, page+".html")
		}
		return named
	}
	return &Templates{
		index:                     load("index"),
		search:                    load("search"),
		event:                     load("event"),
		org:                       load("org"),
		location:                  load("location"),
		login:                     load("login"),
		settings:                  load("settings"),
		verify:                    load("verify"),
		bookingVerify:             load("booking_verify"),
		checkin:                   load("checkin"),
		adminUsers:                load("admin_users"),
		adminBookings:             load("admin_bookings"),
		adminOrgs:                 load("admin_orgs"),
		adminOrgEdit:              load("admin_org_edit"),
		adminFetchurls:            load("admin_fetchurls"),
		adminFetchurlNew:          load("admin_fetchurl_new"),
		adminFetchurlEdit:         load("admin_fetchurl_edit"),
		adminLocations:            load("admin_locations"),
		adminLocationsMaintenance: load("admin_locations_maintenance"),
		adminLocationEdit:         load("admin_location_edit"),
		musicians:                 load("musicians"),
		musician:                  load("musician"),
		adminMusicians:            load("admin_musicians"),
		adminMusicianEdit:         load("admin_musician_edit"),
		instructors:               load("instructors"),
		instructor:                load("instructor"),
		adminInstructors:          load("admin_instructors"),
		adminInstructorEdit:       load("admin_instructor_edit"),
		adminEvents:               load("admin_events"),
		adminEventsMaintenance:    load("admin_events_maintenance"),
		adminEventForm:            load("admin_event_form"),
		adminEventsImport:         load("admin_events_import"),
		adminTemplates:            load("admin_templates"),
		adminTemplateAssign:       load("admin_template_assign"),
		adminDances:               load("admin_dances"),
		adminInfo:                 load("admin_info"),
		adminStats:                load("admin_stats"),
		impressum:                 load("impressum"),
		orgs:                      load("orgs"),
		suggestEvent:              load("events_suggest"),
		suggestDone:               load("events_suggest_done"),
		suggestVerified:           load("events_suggest_verified"),
		invite:                    load("invite"),
		register:                  load("register"),
		registerDone:              load("register_done"),
		registerVerified:          load("register_verified"),
		adminRegistrations:        load("admin_registrations"),
		adminManagement:           load("admin_management"),
		adminRecentChanges:        load("admin_recent_changes"),
		help:                      load("help"),
		contactManage:             load("contact_manage"),
		board:                     load("board"),
		adminSeries:               load("admin_series"),
		adminSeriesNew:            load("admin_series_new"),
		adminSeriesEdit:           load("admin_series_edit"),
		adminEnrich:               load("admin_enrich"),
		adminOrgDashboard:         load("admin_org_dashboard"),
		adminLocationDashboard:    load("admin_location_dashboard"),
		adminInstructorDashboard:  load("admin_instructor_dashboard"),
		adminMusicianDashboard:    load("admin_musician_dashboard"),
		seriesToken:               load("series_token"),
		embedEvents:               loadEmbed("embed_events"),
		embedEvent:                loadEmbed("embed_event"),
		embedOrg:                  loadEmbed("embed_org"),
		embedNext:                 loadEmbed("embed_next"),
		embedCalendar:             loadEmbed("embed_calendar"),
		embedLocations:            loadEmbed("embed_locations"),
		dashboard:                 load("dashboard"),
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

// isClientDisconnect reports whether err is a routine client-side network
// termination (broken pipe, connection reset, i/o timeout). These happen when
// a browser navigates away mid-response and are not actionable server-side.
func isClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "i/o timeout")
}

func renderTemplate(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		// The response status is already committed (streaming template), so
		// calling http.Error here would trigger a superfluous WriteHeader
		// warning. Log genuine template bugs; silently drop client disconnects.
		if !isClientDisconnect(err) {
			log.Printf("template error: %v", err)
		}
	}
}

// renderEmbed renders a standalone embed template (no base.html wrapper).
func renderEmbed(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		if !isClientDisconnect(err) {
			log.Printf("template error: %v", err)
		}
	}
}

// legacyGancioRedirect 301s an unsupported Gancio-era URL pattern to target.
// dansal's IDs/slugs don't correspond 1:1 to Gancio's (different DB), so we
// can't resolve these to a specific equivalent page — a permanent redirect
// to the closest generic target still transfers SEO signal, unlike letting
// the request silently fall through to "/" with a 200 (issue #823).
func legacyGancioRedirect(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	}
}

func indexHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		var events []Event
		var orgs []Organization
		var dances []Dance
		var tagMap map[string]Tag
		var fetchErr error
		var wg sync.WaitGroup
		wg.Add(4)
		go func() { defer wg.Done(); events, fetchErr = client.GetEvents(r.Context(), "") }()
		go func() { defer wg.Done(); orgs, _ = client.GetOrganizations(r.Context()) }()
		go func() { defer wg.Done(); dances, _ = client.GetDances(r.Context()) }()
		go func() { defer wg.Done(); tagMap, _ = client.GetTagMap(r.Context()) }()
		wg.Wait()
		if fetchErr != nil {
			logHTTPError(w, r, "could not load events", http.StatusBadGateway)
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
		holidayDates := template.JS("[]")
		if hc := getSiteSetting(db, "holiday_country"); hc != "" {
			now := time.Now()
			h := publicHolidays(hc, now.Year())
			if now.Month() >= 10 {
				for k, v := range publicHolidays(hc, now.Year()+1) {
					h[k] = v
				}
			}
			if len(h) > 0 {
				dates := make([]string, 0, len(h))
				for d := range h {
					dates = append(dates, `"`+d+`"`)
				}
				holidayDates = template.JS("[" + strings.Join(dates, ",") + "]")
			}
		}
		renderTemplate(w, tmpls.index, tmplData(r, cfg, i18n, title, IndexData{Events: events, TotalEvents: client.EventsTotal(), OrgMap: orgMap, TagMap: tagMap, FederatedEvents: fedEvents, Dances: dances, HolidayDates: holidayDates}))
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
		var event Event
		if token := getSessionToken(r); token != "" {
			event, err = client.GetEventAuthed(r.Context(), id, token)
			if err != nil {
				event, err = client.GetEvent(r.Context(), id)
			}
		} else {
			event, err = client.GetEvent(r.Context(), id)
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}

		var (
			org      *Organization
			slug     string
			posts    []ContactPost
			members  []OrgMember
			tagMap   map[string]Tag
			userOrgs []Organization
		)

		su := getSessionUser(r)
		needMembers := su != nil && su.Role != "admin" && event.OrganizationID != nil
		token := getSessionToken(r)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); tagMap, _ = client.GetTagMap(r.Context()) }()
		if event.OrganizationID != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				o, err := client.GetOrganization(r.Context(), *event.OrganizationID)
				if err == nil {
					org = &o
					slug = effectiveSlug(o)
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			posts, _ = client.GetContactPosts(r.Context(), id)
		}()
		if needMembers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ms, err := client.GetOrganizationMembers(r.Context(), *event.OrganizationID, token)
				if err == nil {
					members = ms
				}
			}()
		}
		// Series siblings for prev/next navigation.
		var prevEvent, nextEvent *Event
		if event.SeriesID != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				siblings, err := client.GetEventsBySeries(r.Context(), *event.SeriesID)
				if err != nil {
					return
				}
				for i, e := range siblings {
					if e.ID == event.ID {
						if i > 0 {
							prev := siblings[i-1]
							prevEvent = &prev
						}
						if i < len(siblings)-1 {
							next := siblings[i+1]
							nextEvent = &next
						}
						break
					}
				}
			}()
		}
		// For logged-in users: fetch their orgs (for assign/publish flow and save-as-template).
		if su != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				allOrgs, _ := client.GetOrganizations(r.Context())
				if su.Role == "admin" {
					userOrgs = allOrgs
				} else {
					orgIDs := getUserOrgIDs(r.Context(), client, su.ID, token)
					idSet := make(map[int]bool, len(orgIDs))
					for _, oid := range orgIDs {
						idSet[oid] = true
					}
					for _, o := range allOrgs {
						if idSet[o.ID] {
							userOrgs = append(userOrgs, o)
						}
					}
				}
			}()
		}
		wg.Wait()

		canManage := su != nil && su.Role == "admin"
		if !canManage && needMembers {
			for _, m := range members {
				if m.UserID == su.ID {
					canManage = true
					break
				}
			}
		}

		boardPosted := r.URL.Query().Get("board_posted") == "1"
		boardTelegramURL := r.URL.Query().Get("board_tg_url")
		boardContacted := r.URL.Query().Get("board_contacted") == "1"
		boardContactTgURL := r.URL.Query().Get("board_contact_tg_url")
		boardError := r.URL.Query().Get("board_error")
		bookingOK := r.URL.Query().Get("book_ok") == "1"
		bookingError := r.URL.Query().Get("book_error")

		clientIP := getClientIP(r)
		lang := i18n.detectLang(r)
		pageTitle := event.Title
		if d := formatDateStr(lang, event.StartTime); d != event.StartTime {
			pageTitle = event.Title + " – " + d
		}
		td := tmplData(r, cfg, i18n, pageTitle, EventData{
			Event:             event,
			Org:               org,
			OrgSlug:           slug,
			TagMap:            tagMap,
			ContactPosts:      posts,
			CanManageBoard:    canManage,
			BoardPosted:       boardPosted,
			BoardTelegramURL:  boardTelegramURL,
			BoardContacted:    boardContacted,
			BoardContactTgURL: boardContactTgURL,
			BoardError:        boardError,
			BookingOK:         bookingOK,
			BookingError:      bookingError,
			UserOrgs:          userOrgs,
			BookFormToken:     issueFormToken(clientIP),
			BoardFormToken:    issueFormToken(clientIP),
			PrevEvent:         prevEvent,
			NextEvent:         nextEvent,
		})
		td.MetaDescription = eventMetaDesc(event, lang)
		if event.ImageURL != "" {
			td.OGImage = "https://" + cfg.Domain + event.ImageURL
		}
		renderTemplate(w, tmpls.event, td)
	}
}

func eventAssignOrgHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su := getSessionUser(r)
		if su == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		orgID, err := strconv.Atoi(r.FormValue("org_id"))
		if err != nil || orgID == 0 {
			http.Error(w, "invalid org_id", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		members, err := client.GetOrganizationMembers(r.Context(), orgID, token)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		isMember := false
		for _, m := range members {
			if m.UserID == su.ID {
				isMember = true
				break
			}
		}
		if !isMember {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if err := client.AssignEventOrg(r.Context(), id, orgID, token); err != nil {
			logHTTPError(w, r, "assign failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/events/%d", id), http.StatusSeeOther)
	}
}

func orgFrontendHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("name")

		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			orgs, oErr := client.GetOrganizations(r.Context())
			if oErr != nil {
				logHTTPError(w, r, "upstream error", http.StatusBadGateway)
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
			logHTTPError(w, r, "database error", http.StatusInternalServerError)
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
		td := tmplData(r, cfg, i18n, org.Name, OrgData{
			Org:            org,
			UpcomingEvents: upcoming,
			PastEvents:     past,
			AllEvents:      allEvents,
			Musicians:      musicians,
			Slug:           slug,
			Handle:         handle,
			FollowerCount:  followerCount,
		})
		td.MetaDescription = metaDesc(org.Description, 155)
		if org.ImageURL != "" {
			td.OGImage = "https://" + cfg.Domain + org.ImageURL
		}
		renderTemplate(w, tmpls.org, td)
	}
}

func locationPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		loc, err := client.GetLocation(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		events, _ := client.GetEventsByLocation(r.Context(), id)
		// A parent (building) page aggregates events across all its rooms
		// (#687) — a room is a child Location, so its events live under its
		// own location_id and wouldn't otherwise show up on the building page.
		for _, child := range loc.Children {
			childEvents, _ := client.GetEventsByLocation(r.Context(), child.ID)
			events = append(events, childEvents...)
		}
		if len(loc.Children) > 0 {
			sort.Slice(events, func(i, j int) bool { return events[i].StartTime < events[j].StartTime })
		}
		title := loc.ShortName
		if title == "" {
			title = loc.Location
		}
		td := tmplData(r, cfg, i18n, title, LocationPageData{
			Location: loc,
			Events:   events,
		})
		parts := []string{title}
		if loc.Town != "" && loc.Town != title {
			parts = append(parts, loc.Town)
		}
		if loc.Country != "" {
			parts = append(parts, loc.Country)
		}
		td.MetaDescription = strings.Join(parts, ", ")
		renderTemplate(w, tmpls.location, td)
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
			logHTTPError(w, r, "could not load organizations", http.StatusBadGateway)
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
				writeJSONError(w, r, http.StatusNotFound, "actor not found")
				return
			}
			orgs, err := client.GetOrganizations(r.Context())
			if err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, "upstream error")
				return
			}
			for _, org := range orgs {
				if effectiveSlug(org) == slug {
					actor, err = ensureActor(db, org.ID, slug)
					if err != nil {
						writeJSONError(w, r, http.StatusInternalServerError, "actor init error")
						return
					}
					break
				}
			}
			if actor == nil {
				writeJSONError(w, r, http.StatusNotFound, "actor not found")
				return
			}
		} else if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		// Relay actor: synthetic profile with no backing org
		if actor.OrgID == 0 {
			base := actorURL(cfg, cfg.RelayActorName)
			a := Actor{
				Context:                   APContext,
				Type:                      "Application",
				ID:                        base,
				Name:                      cfg.RelayActorName + "@" + cfg.Domain,
				URL:                       "https://" + cfg.Domain,
				PreferredUsername:         cfg.RelayActorName,
				Inbox:                     base + "/inbox",
				Outbox:                    base + "/outbox",
				Followers:                 base + "/followers",
				ManuallyApprovesFollowers: false,
				Discoverable:              true,
				Indexable:                 true,
				Endpoints:                 &APEndpoints{SharedInbox: "https://" + cfg.Domain + "/inbox"},
				AlsoKnownAs:               cfg.RelayAlsoKnownAs,
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
			writeJSONError(w, r, http.StatusNotFound, "org not found")
			return
		}

		a := actorFromOrg(cfg, org, actor)
		writeJSON(w, http.StatusOK, a)
	}
}

// imageProxyHandler forwards GET requests for a single path segment ID to the
// API backend, streaming the response (including Content-Type) back to the
// browser. This makes /api/v1/*-images/{id} URLs work when the browser hits
// the web frontend instead of the API directly.
func imageProxyHandler(client *DansalClient, prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		apiURL := client.BaseURL + prefix + id
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, apiURL, nil)
		if err != nil {
			logHTTPError(w, r, "proxy error", http.StatusBadGateway)
			return
		}
		resp, err := client.HTTP.Do(req)
		if err != nil {
			logHTTPError(w, r, "proxy error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "" {
			w.Header().Set("Cache-Control", cc)
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body) //nolint:errcheck
	}
}

type BoardData struct {
	Posts         []ContactPost
	TownFilter    string
	Query         string
	Towns         []string
	ShowRides     bool
	ShowSleep     bool
	ShowTickets   bool
	ShowLostFound bool
	FormToken     string
}

func boardHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		// Type group toggles: default all on; individual group off only if explicitly excluded.
		showRides := q.Get("rides") != "0"
		showSleep := q.Get("sleep") != "0"
		showTickets := q.Get("tickets") != "0"
		showLostFound := q.Get("lostfound") != "0"

		var typeList []string
		if showRides {
			typeList = append(typeList, "ride_offer", "ride_request")
		}
		if showSleep {
			typeList = append(typeList, "sleep_offer", "sleep_request")
		}
		if showTickets {
			typeList = append(typeList, "ticket_offer", "ticket_request")
		}
		if showLostFound {
			typeList = append(typeList, "lost_item", "found_item")
		}

		params := url.Values{}
		if len(typeList) < 8 { // if not all types, pass the filter
			params.Set("type", strings.Join(typeList, ","))
		}
		townFilter := q.Get("town")
		search := q.Get("q")
		if townFilter != "" {
			params.Set("town", townFilter)
		}
		if search != "" {
			params.Set("q", search)
		}

		posts, _ := client.GetAllContactPosts(r.Context(), params)

		// Reuse posts for the town dropdown when no filter narrowed the
		// results; only fetch the unfiltered list separately if needed.
		allPosts := posts
		if len(params) > 0 {
			allPosts, _ = client.GetAllContactPosts(r.Context(), url.Values{})
		}
		townSet := map[string]struct{}{}
		for _, p := range allPosts {
			if p.Event != nil && p.Event.Town != "" {
				townSet[p.Event.Town] = struct{}{}
			}
		}
		towns := make([]string, 0, len(townSet))
		for t := range townSet {
			towns = append(towns, t)
		}
		sort.Strings(towns)

		title := i18n.T(r, "nav_board")
		data := BoardData{
			Posts:         posts,
			TownFilter:    townFilter,
			Query:         search,
			Towns:         towns,
			ShowRides:     showRides,
			ShowSleep:     showSleep,
			ShowTickets:   showTickets,
			ShowLostFound: showLostFound,
			FormToken:     issueFormToken(getClientIP(r)),
		}
		renderTemplate(w, tmpls.board, tmplData(r, cfg, i18n, title, data))
	}
}

func impressumHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := i18n.detectLang(r)
		var body template.HTML
		if text := siteCfg.Impressum()[lang]; text != "" {
			body = template.HTML(`<pre class="impressum-text">` + template.HTMLEscapeString(text) + `</pre>`)
		} else if md := LegalMarkdownHTML(cfg.LegalDir, "impressum"); md != "" {
			body = md
		} else {
			body = cfg.pagesContent.ImpressumHTML(lang)
		}
		if body == "" {
			http.NotFound(w, r)
			return
		}
		title := i18n.T(r, "nav_impressum")
		renderTemplate(w, tmpls.impressum, tmplData(r, cfg, i18n, title, body))
	}
}

func legalPageHandler(cfg *Config, tmpls *Templates, i18n *I18n, file, titleKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := LegalMarkdownHTML(cfg.LegalDir, file)
		if body == "" {
			http.NotFound(w, r)
			return
		}
		title := i18n.T(r, titleKey)
		renderTemplate(w, tmpls.impressum, tmplData(r, cfg, i18n, title, body))
	}
}
