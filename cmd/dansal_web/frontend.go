package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
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
	DateFormat             string // "de" for DD.MM.YYYY; "" for locale-based
	AppVersion             string
	AppBuildTime           string
	SuggestAvailable       bool
	RegistrationEnabled    bool
	SessionIdleTimeoutMins int
	PendingRegCount        int    // verified pending registrations awaiting action (scoped to caller)
	PendingSuggestionCount int    // unpublished events awaiting review (scoped to caller)
	PossibleDuplicateCount int    // events flagged as possible duplicates (scoped to caller)
	PendingEditCount       int    // published events with a pending suggester edit awaiting review
	NotVerifiedEventCount  int    // events with email_verified=0, invisible everywhere until verified/backfilled
	Path                   string // current request path, for building "return to this page" links
	CanonicalURL           string // absolute canonical URL for this page (may include ?lang=XX)
	HreflangBaseURL        string // same as CanonicalURL but never includes ?lang= — used as the base for hreflang hrefs so bots don't accumulate ?lang=X?lang=Y chains (#1089)
	Hreflang               bool   // emit <link rel="alternate" hreflang> tags for this page (only where content is 100% i18n-driven, not organizer-entered)
	MetaDescription        string // page-specific meta description (falls back to i18n string in template)
	OGImage                string // absolute URL of the primary image for OG/Twitter card
	GoogleSiteVerification string
	BingSiteVerification   string
	BannerAIGenerated      bool
	LogoAIGenerated        bool
	RelayActorURL          string // absolute ActivityPub actor URL of the relay actor, for a site-wide discovery link (#951)
	Nonce                  string // per-request CSP nonce; every inline <script> must carry nonce="{{$.Nonce}}" (#1141)
}

// attentionCache serves the scoped "needs attention" counts from a short-TTL
// cache keyed by user ID, so the nav badge doesn't block every page render on
// a round-trip to the API. The 10s window matches siteSettingsCache and is
// fine for a badge: admin actions that change the counts are visible within
// a refresh or two (perf #1032).
type attentionCache struct {
	ttl time.Duration
	mu  sync.Mutex
	at  map[int]time.Time
	val map[int]DashboardAttention
}

func newAttentionCache() *attentionCache {
	return &attentionCache{
		ttl: 10 * time.Second,
		at:  make(map[int]time.Time),
		val: make(map[int]DashboardAttention),
	}
}

// get returns the cached attention for userID, ok=false when missing or stale.
func (c *attentionCache) get(userID int) (DashboardAttention, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.at[userID]
	if !ok || time.Since(t) > c.ttl {
		return DashboardAttention{}, false
	}
	return c.val[userID], true
}

func (c *attentionCache) put(userID int, a DashboardAttention) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at[userID] = time.Now()
	c.val[userID] = a
}

// dashboardAttentionMiddleware fetches the scoped "needs attention" counts for
// logged-in admin/user roles and stores them in the request context so tmplData
// can inject them into every rendered page without each handler needing to ask.
// The fetch is served from attentionCache so a slow API stalls at most one
// request per user per 10s window instead of every page render (#1032).
func dashboardAttentionMiddleware(client *DansalClient) func(http.Handler) http.Handler {
	cache := newAttentionCache()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			su := getSessionUser(r)
			if su != nil && (su.Role == "admin" || su.Role == "user") {
				if att, ok := cache.get(su.ID); ok {
					r = r.WithContext(context.WithValue(r.Context(), ctxDashboardAttention, att))
				} else if att, err := client.GetDashboardAttention(r.Context(), getSessionToken(r)); err == nil {
					cache.put(su.ID, att)
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

	hreflangBase := "https://" + cfg.Domain + r.URL.Path // never has ?lang=
	canonical := hreflangBase
	// When a ?lang= override is in the URL, include it in the canonical so the
	// hreflang self-reference is consistent and Google Search Console does not
	// flag the page as "Alternate page with proper canonical tag" (#966).
	// HreflangBaseURL intentionally omits ?lang= so hreflang hrefs in the
	// template are always "basepath?lang=XX" — never "basepath?lang=YY?lang=XX"
	// which bots would accumulate into infinite URL chains (#1089).
	if l := r.URL.Query().Get("lang"); l != "" {
		canonical += "?lang=" + l
	}

	var relayActorURL string
	if cfg.RelayActorName != "" {
		relayActorURL = actorURL(cfg, cfg.RelayActorName)
	}

	return TemplateData{
		Title:        title,
		Domain:       cfg.Domain,
		SiteName:     siteName,
		User:         getSessionUser(r),
		Strings:      strs,
		LangCode:     lang,
		Languages:    i18n.Options(lang),
		Contact:      contact,
		ImpressumURL: impressumURL,
		Data:         data,
		BannerHeight: bannerHeight,
		LogoHeight:   logoHeight,
		DarkMode:     cfg.DarkMode,
		TimeFormat: func() string {
			if tf := siteCfg.TimeFormatSite(); tf != "" {
				return tf
			}
			return cfg.timeFormat()
		}(),
		DateFormat:             siteCfg.DateFormat(),
		AppVersion:             Version,
		AppBuildTime:           BuildTime,
		SuggestAvailable:       suggestAvailable(cfg),
		RegistrationEnabled:    registrationEnabled(cfg),
		SessionIdleTimeoutMins: cfg.SessionIdleTimeoutMins,
		PendingRegCount:        dashAttention(r).PendingRegistrations,
		PendingSuggestionCount: dashAttention(r).PendingEventSuggestions,
		PossibleDuplicateCount: dashAttention(r).PossibleDuplicates,
		PendingEditCount:       dashAttention(r).PendingEdits,
		NotVerifiedEventCount:  dashAttention(r).NotVerifiedEventCount,
		Path:                   r.URL.Path,
		CanonicalURL:           canonical,
		HreflangBaseURL:        hreflangBase,
		GoogleSiteVerification: cfg.GoogleSiteVerification,
		BingSiteVerification:   cfg.BingSiteVerification,
		OGImage:                "https://" + cfg.Domain + "/banner.avif",
		BannerAIGenerated:      siteCfg.BannerAIGenerated(),
		LogoAIGenerated:        siteCfg.LogoAIGenerated(),
		RelayActorURL:          relayActorURL,
		Nonce:                  nonceFromRequest(r),
	}
}

type IndexData struct {
	Events          []Event
	TotalEvents     int // true server-side count; may exceed len(Events) when the API's pagination cap truncated the result
	OrgMap          map[int]Organization
	TagMap          map[string]Tag
	FederatedEvents []FederatedEvent
	Dances          []Dance
	HolidayDates    template.JS // JSON array of "YYYY-MM-DD" strings
	// ExternalOverlayJSON (#1220) is the cached external map overlay (see
	// external_overlay.go) as a JSON array of geoEvent-shaped pins with
	// Ext=true — "[]" when the feature isn't configured, never empty/unset,
	// so index.html's JSON.parse of it always succeeds.
	ExternalOverlayJSON template.JS
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
	BoardErrorMsg     string // detailed message from the API, when available (#973); falls back to BoardError's i18n key otherwise
	BoardErrorID      string // error_id to quote when reporting the problem (#985)
	BookingOK         bool
	BookingError      string
	BookingErrorMsg   string
	BookingErrorID    string
	UserOrgs          []Organization
	BookFormToken     string
	BoardFormToken    string
	PrevEvent         *Event
	NextEvent         *Event
	// Board session prefill (#1047): populated from dsw_board cookie when valid.
	BoardSessionEmail    string
	BoardSessionNickname string
	// SeriesImageURL and SeriesImageAIGenerated carry the series banner for OGImage,
	// JSON-LD fallback, and hero display when the event has no own image (#1072).
	SeriesImageURL         string
	SeriesImageAIGenerated bool
	// TimetableHistory (#1176) is the timetable's change journal, newest
	// first; empty when the event has no timetable or no saves yet.
	TimetableHistory []TimetableHistoryEntry
}

type OrgData struct {
	Org             Organization
	UpcomingEvents  []Event
	PastEvents      []Event
	AllEvents       []Event
	Musicians       []Musician
	Slug            string
	Handle          string
	FollowerCount   int
	RecurringSeries []SeriesCadenceEntry
}

// SeriesCadenceEntry is one row in an org page's "recurring events" section
// (#1185): title/cadence/next-occurrence for one series, derived from the
// org's already-loaded upcoming events rather than a separate series API
// call. This is what makes "cadence set AND at least one published event"
// hold by construction — upcoming is already published-only (GetAllEventsByOrg
// requests is_published=true) and by definition in the future, so a series
// only appears here when both conditions are actually true right now.
type SeriesCadenceEntry struct {
	SeriesID     int
	Title        string
	Cadence      string
	NextEventID  int
	NextStart    string
	LocationName string
}

// recurringSeriesFromEvents derives the distinct list of series to show in
// an org page's recurring-events section from that org's upcoming events,
// which are chronologically ordered (GetEvents/GetAllEventsByOrg both sort
// "e.start_time ASC" server-side) — so the first event seen for a given
// series_id is its soonest upcoming occurrence.
func recurringSeriesFromEvents(events []Event) []SeriesCadenceEntry {
	seen := make(map[int]bool)
	var out []SeriesCadenceEntry
	for _, e := range events {
		if e.SeriesID == nil || e.SeriesCadence == "" || seen[*e.SeriesID] {
			continue
		}
		seen[*e.SeriesID] = true
		locName := ""
		if e.Location != nil {
			switch {
			case e.Location.ShortName != "":
				locName = e.Location.ShortName
			case e.Location.Location != "":
				locName = e.Location.Location
			default:
				locName = e.Location.Town
			}
		}
		out = append(out, SeriesCadenceEntry{
			SeriesID: *e.SeriesID, Title: e.Title, Cadence: e.SeriesCadence,
			NextEventID: e.ID, NextStart: e.StartTime, LocationName: locName,
		})
	}
	return out
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

//go:embed static/ai-badge.svg
var aiBadgeDefault []byte

//go:embed static/qrcode.min.js
var qrcodeJS []byte

//go:embed static/base.js
var baseJS []byte

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

// geoEvent is the compact event projection used for map markers, both in the
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
	ID        int     `json:"id"`
	Title     string  `json:"t"`
	Start     string  `json:"s"`
	End       string  `json:"e,omitempty"`
	Location  string  `json:"loc,omitempty"`
	ShortName string  `json:"sn,omitempty"`
	Town      string  `json:"town,omitempty"`
	Country   string  `json:"c,omitempty"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	URL       string  `json:"url,omitempty"`
	// Tags is the event's raw tag slug list (#1173) — index.html's JS
	// derives per-home-group flags from this against HOME_GROUPS[i].members
	// itself, rather than dansal_web precomputing a fixed Ball/Workshop/
	// Festival/Session/Concert set of booleans for a vocabulary that's no
	// longer fixed. WorkshopDifficulty stays a dedicated field: it's not a
	// tag, it's a separate per-event attribute the "workshop" home-group's
	// badge happens to also show a sub-badge for.
	Tags               []string `json:"tags,omitempty"`
	WorkshopDifficulty string   `json:"wd,omitempty"`
	Cancelled          bool     `json:"x,omitempty"`
	Availability       string   `json:"av,omitempty"`
	BookingEnabled     bool     `json:"book,omitempty"`
	Fee                string   `json:"fee,omitempty"` // "free"|"donation"|"paid"|""
	Food               string   `json:"food,omitempty"`
	Drink              string   `json:"drink,omitempty"`
	Wheelchair         bool     `json:"wc,omitempty"`
	HearingLoop        bool     `json:"hl,omitempty"`
	// Ext/Src (#1220): set only for the external map overlay (see
	// external_overlay.go) — a live pin sourced from a third-party site
	// (e.g. folkbalbende.be), never a dansal event. index.html's JS uses
	// Ext to give the pin a distinct icon, credit Src in the popup, and
	// link out via URL instead of /events/{id}. Both are omitted (false/
	// empty) for every normal dansal event.
	Ext bool   `json:"ext,omitempty"`
	Src string `json:"src,omitempty"`
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
		geo = append(geo, geoEvent{
			ID: e.ID, Title: e.Title, Start: e.StartTime, End: end,
			Location: locName, ShortName: locShortName, Town: locTown, Country: locCountry,
			Lat: lat, Lng: lng, URL: e.URL,
			Tags: e.Tags, WorkshopDifficulty: e.WorkshopDifficulty,
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
	err := fetchParallel(
		func() error { var err error; events, err = fetchEvents(); return err },
		func() error { var err error; orgs, err = client.GetOrganizations(r.Context()); return err },
		func() error { var err error; tagMap, err = client.GetTagMap(r.Context()); return err },
	)
	if err != nil {
		return nil, "", err
	}

	orgMap := orgMapByID(orgs)
	strs := i18n.Strings(i18n.detectLang(r))

	var rowsHTML strings.Builder
	for _, e := range events {
		tmpl.ExecuteTemplate(&rowsHTML, "event-row", map[string]any{
			"Event": e, "OrgMap": orgMap, "TagMap": tagMap, "Strings": strs,
		})
	}
	return events, rowsHTML.String(), nil
}

// tmplFuncMap is the merged template function map, assembled from per-domain
// fragments in templatefuncs_*.go (time, location, tags, chat, misc) so the
// literal no longer dominates frontend.go (#1031).
var tmplFuncMap = func() template.FuncMap {
	m := template.FuncMap{}
	for _, part := range []template.FuncMap{tmplFuncsTime, tmplFuncsLocation, tmplFuncsTags, tmplFuncsChat, tmplFuncsMisc} {
		for k, v := range part {
			m[k] = v
		}
	}
	return m
}()

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
	tag                       *template.Template
	tagsIndex                 *template.Template
	cities                    *template.Template
	city                      *template.Template
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
	adminCategoryMappings     *template.Template
	adminOIDCProviders        *template.Template
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
	adminTimetable            *template.Template
	adminEventDescription     *template.Template
	seriesToken               *template.Template
	embedEvents               *template.Template
	embedEvent                *template.Template
	embedTimetable            *template.Template
	embedOrg                  *template.Template
	embedNext                 *template.Template
	embedCalendar             *template.Template
	embedLocations            *template.Template
	dashboard                 *template.Template
	festivals                 *template.Template
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
		tag:                       load("tag"),
		tagsIndex:                 load("tags"),
		cities:                    load("cities"),
		city:                      load("city"),
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
		adminCategoryMappings:     load("admin_category_mappings"),
		adminOIDCProviders:        load("admin_oidc_providers"),
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
		adminTimetable:            load("admin_timetable"),
		adminEventDescription:     load("admin_event_description"),
		seriesToken:               load("series_token"),
		embedEvents:               loadEmbed("embed_events"),
		embedEvent:                loadEmbed("embed_event"),
		embedTimetable:            loadEmbed("embed_timetable"),
		embedOrg:                  loadEmbed("embed_org"),
		embedNext:                 loadEmbed("embed_next"),
		embedCalendar:             loadEmbed("embed_calendar"),
		embedLocations:            loadEmbed("embed_locations"),
		dashboard:                 load("dashboard"),
		festivals:                 load("festivals"),
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
		if err := rows.Scan(&eventURL); err != nil {
			logHTTPError(w, r, "could not read federated event URL", http.StatusInternalServerError)
			return
		}
		// Defense-in-depth (#1000): apObjectToFederatedEvent already drops
		// unsafe URLs at ingest, but re-validate here too in case a row was
		// written before that check existed.
		if eventURL == "" || validateAPURL(eventURL) != nil {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, eventURL, http.StatusFound)
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
		// Populate/refresh the events cache first so its conditional-GET
		// ETag (already fingerprinted by the API's checkPublicCacheHeaders)
		// is known before doing any other work. A crawler revisiting an
		// unchanged event list gets a 304 without the orgs/dances/tags
		// fetches or a template render (#1129).
		if _, err := client.GetEvents(r.Context(), ""); err == nil {
			if checkETag(w, r, client.EventsETag()) {
				return
			}
		}
		var events []Event
		var orgs []Organization
		var dances []Dance
		var tagMap map[string]Tag
		err := fetchParallel(
			func() error { var err error; events, err = client.GetEvents(r.Context(), ""); return err },
			func() error {
				var err error
				orgs, err = client.GetOrganizations(r.Context())
				if err != nil {
					log.Printf("index: could not load organizations: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				dances, err = client.GetDances(r.Context())
				if err != nil {
					log.Printf("index: could not load dances: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				tagMap, err = client.GetTagMap(r.Context())
				if err != nil {
					log.Printf("index: could not load tag map: %v", err)
				}
				return nil
			},
		)
		if err != nil {
			logHTTPError(w, r, "could not load events", http.StatusBadGateway)
			return
		}
		orgMap := orgMapByID(orgs)
		var fedEvents []FederatedEvent
		if cfg.ShowFederatedEvents {
			fedEvents, err = listFederatedEvents(db)
			if err != nil {
				logHTTPError(w, r, "could not load federated events", http.StatusBadGateway)
				return
			}
		}
		title := i18n.T(r, "events_title")
		holidayDates := template.JS("[]")
		if hc := siteCfg.HolidayCountry(); hc != "" {
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
		renderTemplate(w, tmpls.index, tmplData(r, cfg, i18n, title, IndexData{Events: events, TotalEvents: client.EventsTotal(), OrgMap: orgMap, TagMap: tagMap, FederatedEvents: fedEvents, Dances: dances, HolidayDates: holidayDates, ExternalOverlayJSON: template.JS(currentExternalOverlayJSON())}))
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
		event, err := fetchEventWithFallback(r, client, id)
		if err != nil {
			if errors.Is(err, errNotFound) {
				http.NotFound(w, r)
			} else {
				logHTTPError(w, r, "could not load event", http.StatusBadGateway)
			}
			return
		}

		// ActivityPub: serve the event as a Note when requested by an AP client.
		if isAPRequest(r) {
			slug := cfg.RelayActorName
			if event.OrganizationID != nil {
				if org, oerr := client.GetOrganization(r.Context(), *event.OrganizationID); oerr == nil {
					slug = effectiveSlug(org)
				}
			}
			note := buildNoteFromEvent(cfg, slug, event)
			note.Context = APContext
			writeJSON(w, http.StatusOK, note)
			return
		}

		su := getSessionUser(r)

		// Conditional GET (#1129): anonymous visitors and crawlers all see
		// the same public page, so a 304 is safe there. Logged-in sessions
		// see personalized controls (canManage, board-session prefill) that
		// don't move with ChangedAt, so skip it for them — mirrors the
		// admin-skips-cache-headers convention in getEvents (cmd/dansal).
		if su == nil {
			if changedAt := parseChangedAt(event.ChangedAt); changedAt > 0 {
				if checkLastModified(w, r, time.Unix(changedAt, 0)) {
					return
				}
			}
		}

		epd := loadEventPageData(r, client, event, su)
		canManage := eventCanManage(su, event, epd.members)

		// One-time flash (#985): the redirect after a board/booking form
		// submission carries only an opaque ?msg=<token>; flashTake reads and
		// deletes the payload in one shot, so reopening the same URL later
		// (bookmark, history, a shared link) renders a clean page instead of
		// the banner forever.
		flash := flashTake(r.URL.Query().Get("msg"))

		clientIP := getClientIP(r)
		lang := i18n.detectLang(r)
		pageTitle := eventPageTitle(event, lang)

		// Board session prefill (#1047): fetch email/nickname from dsw_board cookie.
		var bsEmail, bsNickname string
		if bsToken := getBoardSessionToken(r); bsToken != "" && getSessionUser(r) == nil {
			if info, err := client.GetBoardSessionMe(r.Context(), bsToken); err == nil {
				bsEmail = info.Email
				bsNickname = info.Nickname
			}
		}

		td := tmplData(r, cfg, i18n, pageTitle, EventData{
			Event:                  event,
			Org:                    epd.org,
			OrgSlug:                epd.slug,
			TagMap:                 epd.tagMap,
			ContactPosts:           epd.posts,
			CanManageBoard:         canManage,
			BoardPosted:            flash.BoardPosted,
			BoardTelegramURL:       flash.BoardTelegramURL,
			BoardContacted:         flash.BoardContacted,
			BoardContactTgURL:      flash.BoardContactTgURL,
			BoardError:             flash.BoardError,
			BoardErrorMsg:          flash.BoardErrorMsg,
			BoardErrorID:           flash.BoardErrorID,
			BookingOK:              flash.BookingOK,
			BookingError:           flash.BookingError,
			BookingErrorMsg:        flash.BookingErrorMsg,
			BookingErrorID:         flash.BookingErrorID,
			UserOrgs:               epd.userOrgs,
			BookFormToken:          issueFormToken(clientIP),
			BoardFormToken:         issueFormToken(clientIP),
			PrevEvent:              epd.prevEvent,
			NextEvent:              epd.nextEvent,
			BoardSessionEmail:      bsEmail,
			BoardSessionNickname:   bsNickname,
			SeriesImageURL:         epd.seriesImageURL,
			SeriesImageAIGenerated: epd.seriesImageAIGenerated,
			TimetableHistory:       epd.timetableHistory,
		})
		// OGImage fallback: event → generated series/org banner overlay
		// (#1072, #1082, #1083). The generated banner is used whenever a
		// series or org banner would otherwise apply, since it carries a
		// per-event date/title signal a plain reused banner doesn't.
		ogImg := event.ImageURL
		if ogImg == "" && (epd.seriesImageURL != "" || (epd.org != nil && epd.org.ImageURL != "")) {
			ogImg = fmt.Sprintf("/api/v1/event-banner/%d", event.ID)
		}
		renderPage(w, cfg, tmpls.event, td, eventMetaDesc(event, lang), ogImg)
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

		// The relay actor is synthetic and has no backing org page; send
		// browsers to the homepage instead of a 404 (issue #1057).
		if cfg.RelayActorName != "" && slug == cfg.RelayActorName {
			http.Redirect(w, r, "https://"+cfg.Domain+"/", http.StatusFound)
			return
		}

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

		allEvents, err := client.GetAllEventsByOrg(r.Context(), actor.OrgID)
		if err != nil {
			logHTTPError(w, r, "could not load org events", http.StatusBadGateway)
			return
		}
		musicians, err := client.GetMusiciansByOrg(r.Context(), actor.OrgID)
		if err != nil {
			logHTTPError(w, r, "could not load org musicians", http.StatusBadGateway)
			return
		}
		followerCount, err := countFollowers(db, actor.OrgID)
		if err != nil {
			logHTTPError(w, r, "could not load follower count", http.StatusBadGateway)
			return
		}

		upcoming, past := splitUpcomingPast(allEvents, time.Now())
		// Past events: most recent first
		for i, j := 0, len(past)-1; i < j; i, j = i+1, j-1 {
			past[i], past[j] = past[j], past[i]
		}

		handle := "@" + slug + "@" + cfg.Domain
		td := tmplData(r, cfg, i18n, org.Name, OrgData{
			Org:             org,
			UpcomingEvents:  upcoming,
			PastEvents:      past,
			AllEvents:       allEvents,
			Musicians:       musicians,
			Slug:            slug,
			Handle:          handle,
			FollowerCount:   followerCount,
			RecurringSeries: recurringSeriesFromEvents(upcoming),
		})
		renderPage(w, cfg, tmpls.org, td, metaDesc(org.Description, metaDescMaxLen), org.ImageURL)
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
		// A parent (building) page aggregates events across all its rooms
		// (#687) — a room is a child Location, so its events live under its
		// own location_id and wouldn't otherwise show up on the building page.
		// Fetch the parent and every room in parallel instead of one N+1
		// round-trip per child (perf #1032).
		ids := []int{id}
		for _, child := range loc.Children {
			ids = append(ids, child.ID)
		}
		eventsByLoc := make([][]Event, len(ids))
		fns := make([]func() error, len(ids))
		for i, lid := range ids {
			i, lid := i, lid
			fns[i] = func() error {
				ev, eErr := client.GetEventsByLocation(r.Context(), lid)
				if eErr != nil {
					return eErr
				}
				eventsByLoc[i] = ev
				return nil
			}
		}
		if err := fetchParallel(fns...); err != nil {
			logHTTPError(w, r, "could not load location events", http.StatusBadGateway)
			return
		}
		var events []Event
		for _, ev := range eventsByLoc {
			events = append(events, ev...)
		}
		if len(loc.Children) > 0 {
			sort.Slice(events, func(i, j int) bool { return events[i].StartTime < events[j].StartTime })
		}

		// Conditional GET (#1129): freshness must cover both the location
		// row itself and every event shown on the page (a new/changed event
		// at this location doesn't touch loc.UpdatedAt), so take the max of
		// both. Public page — no session-user personalization to worry about
		// here, unlike eventHandler.
		if getSessionUser(r) == nil {
			latest := loc.UpdatedAt
			for _, ev := range events {
				if ca := parseChangedAt(ev.ChangedAt); ca > latest {
					latest = ca
				}
			}
			if latest > 0 && checkLastModified(w, r, time.Unix(latest, 0)) {
				return
			}
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
		renderPage(w, cfg, tmpls.location, td, strings.Join(parts, ", "), "")
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
		err := fetchParallel(
			func() error { var err error; orgs, err = client.GetOrganizations(r.Context()); return err },
			func() error {
				var err error
				statMap, err = client.GetOrgStats(r.Context())
				if err != nil {
					log.Printf("orgs: could not load stats: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				locs, err = client.GetLocations(r.Context())
				if err != nil {
					log.Printf("orgs: could not load locations: %v", err)
				}
				return nil
			},
		)
		if err != nil {
			logHTTPError(w, r, "could not load organizations", http.StatusBadGateway)
			return
		}

		actorSlugs, aErr := listOrgActorSlugs(db)
		if aErr != nil {
			log.Printf("orgs: could not load actor slugs: %v", aErr)
		}

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
		td := tmplData(r, cfg, i18n, title, OrgsListData{Items: items, MapPins: mapPins})
		td.Hreflang = true
		renderTemplate(w, tmpls.orgs, td)
	}
}

func actorOrFrontendHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	frontendH := orgFrontendHandler(cfg, tmpls, db, client, i18n)
	apH := requireAPSignature(cfg, apActorHandler(cfg, db, client))
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
		// Actor documents are cacheable for a short TTL (5 min matches the
		// Mastodon recommendation) — but only on the AP JSON path, never the
		// HTML org page (issue #1058).
		w.Header().Set("Cache-Control", "public, max-age=300")
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

		// Relay actor: synthetic profile with no backing org.
		if actor.OrgID == 0 {
			writeJSON(w, http.StatusOK, relayActorFromRecord(cfg, actor))
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
	ResendSent    bool // true when redirected back from POST /board/resend-manage
	RenewSent     bool // true when redirected back from POST /board/renew-session
	RenewDone     bool // true when redirected back from GET /board/renew-session/{token} (session set)
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

		var posts, allPosts []ContactPost
		if len(params) > 0 {
			// Filtered view: the town dropdown needs the unfiltered list, so
			// run both fetches concurrently instead of back-to-back (#1032).
			err := fetchParallel(
				func() error {
					p, eErr := client.GetAllContactPosts(r.Context(), params)
					if eErr != nil {
						return eErr
					}
					posts = p
					return nil
				},
				func() error {
					p, eErr := client.GetAllContactPosts(r.Context(), url.Values{})
					if eErr != nil {
						return eErr
					}
					allPosts = p
					return nil
				},
			)
			if err != nil {
				logHTTPError(w, r, "could not load board posts", http.StatusBadGateway)
				return
			}
		} else {
			var err error
			posts, err = client.GetAllContactPosts(r.Context(), params)
			if err != nil {
				logHTTPError(w, r, "could not load board posts", http.StatusBadGateway)
				return
			}
			allPosts = posts
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
			ResendSent:    q.Get("resend") == "1",
			RenewSent:     q.Get("renew") == "1",
			RenewDone:     q.Get("renewed") == "1",
		}
		renderTemplate(w, tmpls.board, tmplData(r, cfg, i18n, title, data))
	}
}

func impressumHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := i18n.detectLang(r)
		var body template.HTML
		if text := siteCfg.Impressum()[lang]; text != "" {
			var buf bytes.Buffer
			if err := goldmark.Convert([]byte(text), &buf); err != nil {
				body = template.HTML(`<div class="impressum-text">` + template.HTMLEscapeString(text) + `</div>`)
			} else {
				body = template.HTML(sanitizeMarkdownHTML(buf.String()))
			}
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
