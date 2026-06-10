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

const ctxPendingRegCount webContextKey = 1

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
	AppVersion             string
	AppBuildTime           string
	SuggestAvailable       bool
	RegistrationEnabled    bool
	SessionIdleTimeoutMins int
	PendingRegCount        int // verified pending registrations awaiting action (scoped to caller)
	ShowCookieBanner       bool
}

// pendingRegCountMiddleware fetches the scoped pending-registration count for
// logged-in admin/user roles and stores it in the request context so tmplData
// can inject it into every rendered page without each handler needing to ask.
func pendingRegCountMiddleware(client *DansalClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			su := getSessionUser(r)
			if su != nil && (su.Role == "admin" || su.Role == "user") {
				token := getSessionToken(r)
				if count, err := client.GetPendingRegCount(r.Context(), token); err == nil && count > 0 {
					r = r.WithContext(context.WithValue(r.Context(), ctxPendingRegCount, count))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
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
	// Determine whether to show the cookie consent banner: show when there is
	// no explicit consent cookie and the user hasn't signalled Do-Not-Track.
	showCookieBanner := false
	if _, err := r.Cookie("dsw_cookie_consent"); err == http.ErrNoCookie {
		if !doNotTrack(r) {
			showCookieBanner = true
		}
	}

	// Ensure banner strings exist with sensible defaults when not provided in i18n.yaml
	strs := i18n.Strings(lang)
	if _, ok := strs["cookie_banner_text"]; !ok {
		strs["cookie_banner_text"] = "This site uses cookies to improve your experience."
	}
	if _, ok := strs["accept_cookies"]; !ok {
		strs["accept_cookies"] = "Accept"
	}

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
		AppVersion:             Version,
		AppBuildTime:           BuildTime,
		SuggestAvailable:       suggestAvailable(cfg),
		RegistrationEnabled:    registrationEnabled(cfg),
		SessionIdleTimeoutMins: cfg.SessionIdleTimeoutMins,
		PendingRegCount: func() int {
			if v, ok := r.Context().Value(ctxPendingRegCount).(int); ok {
				return v
			}
			return 0
		}(),
		ShowCookieBanner: showCookieBanner,
	}
}

type IndexData struct {
	Events          []Event
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
	"fmtUnix": func(ts int64) string {
		if ts == 0 {
			return ""
		}
		return time.Unix(ts, 0).UTC().Format("2006-01-02")
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
	"int64Val": func(n *int64) string {
		if n == nil {
			return ""
		}
		return strconv.FormatInt(*n, 10)
	},
	"derefInt": func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	},
	"joinInts": func(ids []int) string {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		return strings.Join(parts, ",")
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
			Food               string  `json:"food,omitempty"`
			Drink              string  `json:"drink,omitempty"`
			Wheelchair         bool    `json:"wc,omitempty"`
			HearingLoop        bool    `json:"hl,omitempty"`
		}
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
				Ball: e.HasBall, Workshop: e.HasWorkshop, WorkshopDifficulty: e.WorkshopDifficulty,
				Festival:  e.HasFestival,
				Cancelled: e.IsCancelled, Availability: e.Availability,
				BookingEnabled: e.BookingEnabled,
				Fee:            fee, Food: e.Food, Drink: e.Drink,
				Wheelchair: merged["wheelchair"], HearingLoop: merged["hearing_loop"],
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
	"orgMapJSON": func(orgMap map[int]Organization) template.JS {
		out := make(map[string]string, len(orgMap))
		for id, o := range orgMap {
			out[strconv.Itoa(id)] = o.Name
		}
		b, _ := json.Marshal(out)
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
}

type Templates struct {
	index                     *template.Template
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
	adminInstructors          *template.Template
	adminInstructorEdit       *template.Template
	adminEvents               *template.Template
	adminEventNew             *template.Template
	adminEventEdit            *template.Template
	adminEventsImport         *template.Template
	adminTemplates            *template.Template
	adminTemplateAssign       *template.Template
	adminDances               *template.Template
	adminInfo                 *template.Template
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
	help                      *template.Template
	contactManage             *template.Template
	board                     *template.Template
	adminSeries               *template.Template
	adminSeriesNew            *template.Template
	adminSeriesEdit           *template.Template
	seriesToken               *template.Template
	embedEvents               *template.Template
	embedEvent                *template.Template
	embedOrg                  *template.Template
	embedNext                 *template.Template
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
		return t
	}
	return &Templates{
		index:                     load("index"),
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
		adminInstructors:          load("admin_instructors"),
		adminInstructorEdit:       load("admin_instructor_edit"),
		adminEvents:               load("admin_events"),
		adminEventNew:             load("admin_event_new"),
		adminEventEdit:            load("admin_event_edit"),
		adminEventsImport:         load("admin_events_import"),
		adminTemplates:            load("admin_templates"),
		adminTemplateAssign:       load("admin_template_assign"),
		adminDances:               load("admin_dances"),
		adminInfo:                 load("admin_info"),
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
		help:                      load("help"),
		contactManage:             load("contact_manage"),
		board:                     load("board"),
		adminSeries:               load("admin_series"),
		adminSeriesNew:            load("admin_series_new"),
		adminSeriesEdit:           load("admin_series_edit"),
		seriesToken:               load("series_token"),
		embedEvents:               loadEmbed("embed_events"),
		embedEvent:                loadEmbed("embed_event"),
		embedOrg:                  loadEmbed("embed_org"),
		embedNext:                 loadEmbed("embed_next"),
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
		renderTemplate(w, tmpls.index, tmplData(r, cfg, i18n, title, IndexData{Events: events, OrgMap: orgMap, TagMap: tagMap, FederatedEvents: fedEvents, Dances: dances, HolidayDates: holidayDates}))
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
		renderTemplate(w, tmpls.event, tmplData(r, cfg, i18n, event.Title, EventData{
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
		}))
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
		title := loc.ShortName
		if title == "" {
			title = loc.Location
		}
		renderTemplate(w, tmpls.location, tmplData(r, cfg, i18n, title, LocationPageData{
			Location: loc,
			Events:   events,
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
