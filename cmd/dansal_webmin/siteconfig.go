package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ademant/dansal/internal/webcommon"
	_ "github.com/mattn/go-sqlite3"

	"github.com/ademant/dansal/internal/places"
)

var legalPageLangs = []string{"de", "br", "ca", "cs", "en", "es", "fr", "it", "nl", "pl", "pt", "uk"}
var siteConfigLegalLanguages = []siteConfigLegalLanguage{
	{Code: "de", Name: "Deutsch"},
	{Code: "br", Name: "Brezhoneg"},
	{Code: "ca", Name: "Català"},
	{Code: "cs", Name: "Čeština"},
	{Code: "en", Name: "English"},
	{Code: "es", Name: "Español"},
	{Code: "fr", Name: "Français"},
	{Code: "it", Name: "Italiano"},
	{Code: "nl", Name: "Nederlands"},
	{Code: "pl", Name: "Polski"},
	{Code: "pt", Name: "Português"},
	{Code: "uk", Name: "Українська"},
}
var siteAssetExts = []string{".svg", ".avif", ".jpg", ".gif"}

const legalTextJSONFormat = "dansal-legal-text"
const legalTextImportMaxBytes = 1 << 20

type siteConfigLegalLanguage struct {
	Code string
	Name string
}

type legalTextJSON struct {
	Format    string            `json:"format"`
	Version   int               `json:"version"`
	Document  string            `json:"document"`
	Languages map[string]string `json:"languages"`
}

// commonTimezones is a curated list of IANA zone names covering every
// continent, for the server.timezone dropdown (#1394). Go's time package
// exposes no portable way to enumerate every zone the host's tzdata carries
// (that needs walking a filesystem path that varies by OS/packaging), and a
// full ~400-entry IANA list would dwarf the handful an instance actually
// needs — one representative zone per UTC-offset-and-DST-rule region is
// enough for an admin to find theirs, and buildTimezoneOptions below always
// keeps the current value selectable even if it isn't in this list.
var commonTimezones = []string{
	"UTC",
	"Europe/London", "Europe/Dublin", "Europe/Lisbon",
	"Europe/Berlin", "Europe/Paris", "Europe/Madrid", "Europe/Rome", "Europe/Amsterdam",
	"Europe/Brussels", "Europe/Vienna", "Europe/Zurich", "Europe/Warsaw", "Europe/Prague",
	"Europe/Budapest", "Europe/Stockholm", "Europe/Oslo", "Europe/Copenhagen",
	"Europe/Helsinki", "Europe/Athens", "Europe/Bucharest", "Europe/Sofia",
	"Europe/Kyiv", "Europe/Moscow", "Europe/Istanbul",
	"America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles",
	"America/Anchorage", "America/Sao_Paulo", "America/Argentina/Buenos_Aires",
	"America/Mexico_City", "America/Bogota", "America/Toronto", "America/Vancouver",
	"Pacific/Honolulu", "Pacific/Auckland", "Pacific/Fiji",
	"Asia/Jerusalem", "Asia/Dubai", "Asia/Karachi", "Asia/Kolkata", "Asia/Dhaka",
	"Asia/Bangkok", "Asia/Jakarta", "Asia/Shanghai", "Asia/Hong_Kong", "Asia/Tokyo",
	"Asia/Seoul", "Asia/Singapore", "Asia/Manila",
	"Africa/Cairo", "Africa/Lagos", "Africa/Johannesburg", "Africa/Nairobi",
	"Australia/Perth", "Australia/Adelaide", "Australia/Sydney", "Australia/Brisbane",
}

// buildTimezoneOptions returns commonTimezones with current inserted in
// alphabetical position if it isn't already one of them, so a select built
// from this list can always show the true effective value as selected
// instead of silently falling back to whatever option happens to be first.
func buildTimezoneOptions(current string) []string {
	for _, z := range commonTimezones {
		if z == current {
			return commonTimezones
		}
	}
	out := make([]string, 0, len(commonTimezones)+1)
	inserted := false
	for _, z := range commonTimezones {
		if !inserted && current != "" && current < z {
			out = append(out, current)
			inserted = true
		}
		out = append(out, z)
	}
	if !inserted && current != "" {
		out = append(out, current)
	}
	return out
}

type dance struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func openWebDB(path string) *sql.DB {
	if path == "" {
		return nil
	}
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=ON")
	if err != nil {
		log.Printf("open web db %s: %v", path, err)
		return nil
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(time.Hour)
	return db
}

func getSiteSetting(db *sql.DB, key string) string {
	if db == nil {
		return ""
	}
	var v string
	err := db.QueryRow("SELECT value FROM site_settings WHERE key = ?", key).Scan(&v)
	if err != nil {
		// Missing key is expected; missing table happens on a brand-new DB
		// before dansal-web has created it. Anything else is worth logging.
		if err != sql.ErrNoRows && !strings.Contains(err.Error(), "no such table") {
			log.Printf("get site setting %s: %v", key, err)
		}
		return ""
	}
	return v
}

func setSiteSetting(db *sql.DB, key, value string) {
	if db == nil {
		return
	}
	if _, err := db.Exec(
		"INSERT INTO site_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		key, value); err != nil {
		log.Printf("set site setting %s: %v", key, err)
	}
}

func siteAssetExists(dir, key string) bool {
	if dir == "" {
		return false
	}
	for _, ext := range siteAssetExts {
		if _, err := os.Stat(filepath.Join(dir, key+ext)); err == nil {
			return true
		}
	}
	return false
}

func detectAssetMIME(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	peek := min(len(data), 512)
	s := strings.TrimSpace(string(data[:peek]))
	if strings.HasPrefix(s, "<svg") || strings.HasPrefix(s, "<?xml") || strings.Contains(s, "<svg") {
		return "image/svg+xml"
	}
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		end := min(len(data), 128)
		for i := 8; i+4 <= end; i += 4 {
			if i == 12 {
				continue
			}
			switch string(data[i : i+4]) {
			case "avif", "avis":
				return "image/avif"
			}
		}
	}
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8 {
		return "image/jpeg"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "image/gif"
	}
	return ""
}

func saveSiteAsset(dir, key string, data []byte) error {
	if dir == "" {
		return fmt.Errorf("images_dir not configured")
	}
	var ext string
	switch detectAssetMIME(data) {
	case "image/svg+xml":
		ext = ".svg"
	case "image/avif":
		ext = ".avif"
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	default:
		return fmt.Errorf("unsupported image format")
	}
	for _, old := range siteAssetExts {
		if old != ext {
			os.Remove(filepath.Join(dir, key+old))
		}
	}
	return os.WriteFile(filepath.Join(dir, key+ext), data, 0o644)
}

// handleAssetUploads reads the multipart file uploads for keys, validates and
// saves each, and returns the keys that were successfully written. GIF is
// accepted only when allowGIF is set or the key is favicon/ai-badge, which
// historically allow it.
func handleAssetUploads(r *http.Request, dir string, keys []string, allowGIF bool) []string {
	var saved []string
	for _, key := range keys {
		f, _, err := r.FormFile(key)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			continue
		}
		mime := detectAssetMIME(data)
		if mime == "" {
			continue
		}
		if !allowGIF && key != "favicon" && key != "ai-badge" && mime == "image/gif" {
			continue
		}
		if err := saveSiteAsset(dir, key, data); err != nil {
			log.Printf("save site asset %s: %v", key, err)
		} else {
			saved = append(saved, key)
		}
	}
	return saved
}

func fetchDances(ctx context.Context, dansalURL string) []dance {
	var dances []dance
	if err := getJSON(ctx, dansalURL+"/api/v1/dances", &dances); err != nil {
		log.Printf("fetch dances: %v", err)
		return nil
	}
	return dances
}

func loadDefaultDanceIDs(db *sql.DB) map[int]bool {
	raw := getSiteSetting(db, "default_dance_ids")
	if raw == "" {
		return nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		log.Printf("default dance ids: parse %q: %v", raw, err)
		return nil
	}
	m := make(map[int]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

type siteConfigData struct {
	Flash                string
	SiteName             string
	Contact              string
	HasLogo              bool
	HasBanner            bool
	HasFavicon           bool
	HasAIBadge           bool
	LogoAIGenerated      bool
	BannerAIGenerated    bool
	HasRelayAvatar       bool
	HasRelayBanner       bool
	Dances               []dance
	DefaultDanceIDs      map[int]bool
	LegalPageLangs       []siteConfigLegalLanguage
	LegalLang            string
	ImpressumText        string
	PrivacyText          string
	TermsText            string
	HolidayCountry       string
	PlaceCountries       string                // #1429: countries for the city type-ahead's place table
	PlaceImports         []places.ImportStatus // #1429: import state per country
	IndexNowKey          string
	RescheduledBadgeDays string
	DateFormat           string // "" locale-based, "de" DD.MM.YYYY
	TimeFormatSite       string // "" web.yaml default, "24h", "12h"
	// Timezone and TimezoneOptions (#1394) are authoritative in the API's
	// own config.yaml (server.timezone), not a site_settings value like
	// DateFormat/TimeFormatSite above — read/written via the admin socket
	// (timezone-get/timezone-set), never through db. TimezoneOptions is a
	// curated common-zone list with the current effective value always
	// present even if it isn't one of the curated options.
	Timezone         string
	TimezoneOptions  []string
	TimezoneError    string
	SameAs           string // one external profile URL per line (#1296)
	HomeIntroYAML    string // #1298: lang -> homepage intro paragraph, as YAML text
	DescBallYAML     string // #1290: lang -> default event description for a ball/fest-noz-tagged event, as YAML text
	DescWorkshopYAML string // #1290: lang -> default event description for a workshop-tagged event, as YAML text
	DescFestivalYAML string // #1290: lang -> default event description for a festival-tagged event, as YAML text
	NoDB             bool
	NoImagesDir      bool
}

func siteConfigPageHandler(cfg *Config, tmpls *Templates, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := tmplData(r, cfg, "Site configuration", nil)

		data := siteConfigData{
			Flash:          r.URL.Query().Get("flash"),
			LegalPageLangs: siteConfigLegalLanguages,
			LegalLang:      selectedLegalLanguage(r.URL.Query().Get("legal_lang")),
		}

		// #1394: authoritative in the API's own config.yaml, fetched over the
		// admin socket — independent of web_db_path, so this runs even when
		// db is nil (unlike every field below, which needs the web DB).
		var tzData struct {
			Timezone string `json:"timezone"`
		}
		if err := getSocketData(cfg.AdminSocket, "timezone-get", &tzData); err != nil {
			data.TimezoneError = err.Error()
		} else {
			data.Timezone = tzData.Timezone
		}
		data.TimezoneOptions = buildTimezoneOptions(data.Timezone)

		if db == nil {
			data.NoDB = true
			d.Data = data
			renderTemplate(w, tmpls.siteConfig, d)
			return
		}

		data.SiteName = getSiteSetting(db, "site_name")
		data.Contact = getSiteSetting(db, "contact")
		data.LogoAIGenerated = getSiteSetting(db, "logo_ai_generated") == "1"
		data.BannerAIGenerated = getSiteSetting(db, "banner_ai_generated") == "1"
		data.HolidayCountry = getSiteSetting(db, "holiday_country")
		data.PlaceCountries = getSiteSetting(db, "place_countries")
		if err := places.EnsureSchema(db); err == nil {
			data.PlaceImports, _ = places.Statuses(db)
		}
		data.IndexNowKey = getSiteSetting(db, "indexnow_key")
		if v := getSiteSetting(db, "rescheduled_badge_days"); v != "" {
			data.RescheduledBadgeDays = v
		} else {
			data.RescheduledBadgeDays = "7"
		}

		data.ImpressumText = getSiteSetting(db, "impressum_"+data.LegalLang)
		data.PrivacyText = getSiteSetting(db, "privacy_"+data.LegalLang)
		data.TermsText = getSiteSetting(db, "terms_"+data.LegalLang)
		data.Dances = fetchDances(r.Context(), cfg.DansalURL)
		data.DefaultDanceIDs = loadDefaultDanceIDs(db)
		data.DateFormat = getSiteSetting(db, "date_format")
		data.TimeFormatSite = getSiteSetting(db, "time_format")
		data.SameAs = getSiteSetting(db, "same_as")
		// #1298: pre-fill with the shipped default when nothing's been saved
		// yet, so the admin sees working, correctly-formatted content to
		// edit rather than a blank field of unclear expected shape.
		if v := getSiteSetting(db, "home_intro"); v != "" {
			data.HomeIntroYAML = v
		} else {
			data.HomeIntroYAML = webcommon.DefaultHomeIntroYAML
		}
		// #1290: same pre-fill-with-shipped-default treatment for each of
		// the three default-event-description buckets.
		if v := getSiteSetting(db, "default_desc_ball"); v != "" {
			data.DescBallYAML = v
		} else {
			data.DescBallYAML = webcommon.DefaultDescBallYAML
		}
		if v := getSiteSetting(db, "default_desc_workshop"); v != "" {
			data.DescWorkshopYAML = v
		} else {
			data.DescWorkshopYAML = webcommon.DefaultDescWorkshopYAML
		}
		if v := getSiteSetting(db, "default_desc_festival"); v != "" {
			data.DescFestivalYAML = v
		} else {
			data.DescFestivalYAML = webcommon.DefaultDescFestivalYAML
		}

		if cfg.ImagesDir == "" {
			data.NoImagesDir = true
		} else {
			data.HasLogo = siteAssetExists(cfg.ImagesDir, "logo")
			data.HasBanner = siteAssetExists(cfg.ImagesDir, "banner")
			data.HasFavicon = siteAssetExists(cfg.ImagesDir, "favicon")
			data.HasAIBadge = siteAssetExists(cfg.ImagesDir, "ai-badge")
			data.HasRelayAvatar = siteAssetExists(cfg.ImagesDir, "relay-avatar")
			data.HasRelayBanner = siteAssetExists(cfg.ImagesDir, "relay-banner")
		}

		d.Data = data
		renderTemplate(w, tmpls.siteConfig, d)
	}
}

func siteConfigSaveHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Error: web_db_path not configured in webmin.yaml"), http.StatusSeeOther)
			return
		}
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Error: bad request"), http.StatusSeeOther)
			return
		}

		var callerID int
		if u := getSessionUser(r); u != nil {
			callerID = u.ID
		}

		setSiteSetting(db, "site_name", strings.TrimSpace(r.FormValue("site_name")))
		setSiteSetting(db, "contact", strings.TrimSpace(r.FormValue("contact")))
		setSiteSetting(db, "holiday_country", strings.ToUpper(strings.TrimSpace(r.FormValue("holiday_country"))))
		// #1429: the city type-ahead's countries. Every save syncs the place
		// table in the background — a no-op unless a country was added
		// (download + import) or removed (rows deleted).
		placeCountries := places.ParseCountries(r.FormValue("place_countries"))
		setSiteSetting(db, "place_countries", strings.Join(placeCountries, ","))
		startPlaceSync(cfg, db, placeCountries, false)
		setSiteSetting(db, "indexnow_key", strings.TrimSpace(r.FormValue("indexnow_key")))

		// Date/time notation (validated to known values only).
		if df := r.FormValue("date_format"); df == "" || df == "de" {
			setSiteSetting(db, "date_format", df)
		}
		if tf := r.FormValue("time_format_site"); tf == "" || tf == "24h" || tf == "12h" {
			setSiteSetting(db, "time_format", tf)
		}
		if n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("rescheduled_badge_days"))); err == nil && n >= 0 {
			setSiteSetting(db, "rescheduled_badge_days", strconv.Itoa(n))
		}

		// #1296: one external profile URL per line, for the site-wide WebSite
		// JSON-LD's sameAs. Stored as-is (whole-textarea trim only) — split,
		// per-line trim, and blank-dropping happen at render time
		// (siteSettingsCache.SameAs) in dansal_web.
		setSiteSetting(db, "same_as", strings.TrimSpace(r.FormValue("same_as")))

		// #1298: homepage intro paragraph, YAML text (lang -> text). Stored
		// as-is (whole-textarea trim only) — YAML parsing and per-language
		// fallback to the shipped default happen at render time
		// (siteSettingsCache.HomeIntro) in dansal_web, so a malformed edit
		// here never blanks the homepage.
		setSiteSetting(db, "home_intro", strings.TrimSpace(r.FormValue("home_intro")))

		// #1290: default event-description sentences, one YAML text per tag
		// bucket. Same storage convention as home_intro above.
		setSiteSetting(db, "default_desc_ball", strings.TrimSpace(r.FormValue("default_desc_ball")))
		setSiteSetting(db, "default_desc_workshop", strings.TrimSpace(r.FormValue("default_desc_workshop")))
		setSiteSetting(db, "default_desc_festival", strings.TrimSpace(r.FormValue("default_desc_festival")))

		var defaultDanceIDs []int
		for _, v := range r.MultipartForm.Value["default_dance_ids"] {
			if n, err := strconv.Atoi(v); err == nil {
				defaultDanceIDs = append(defaultDanceIDs, n)
			}
		}
		j, _ := json.Marshal(defaultDanceIDs)
		setSiteSetting(db, "default_dance_ids", string(j))

		if r.FormValue("logo_ai_generated") == "1" {
			setSiteSetting(db, "logo_ai_generated", "1")
		} else {
			setSiteSetting(db, "logo_ai_generated", "0")
		}
		if r.FormValue("banner_ai_generated") == "1" {
			setSiteSetting(db, "banner_ai_generated", "1")
		} else {
			setSiteSetting(db, "banner_ai_generated", "0")
		}

		var uploadedAssets []string
		if cfg.ImagesDir != "" {
			uploadedAssets = handleAssetUploads(r, cfg.ImagesDir, []string{"logo", "banner", "favicon", "ai-badge"}, false)
		}

		if len(uploadedAssets) > 0 {
			log.Printf("audit: site_settings assets=[%s] updated by user=%d", strings.Join(uploadedAssets, ","), callerID)
		}
		log.Printf("audit: site_settings keys=[site_name,contact,holiday_country,default_dance_ids,indexnow_key,rescheduled_badge_days,logo_ai_generated,banner_ai_generated,date_format,time_format,same_as,home_intro,default_desc_ball,default_desc_workshop,default_desc_festival] updated by user=%d", callerID)

		http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Settings saved"), http.StatusSeeOther)
	}
}

func selectedLegalLanguage(lang string) string {
	for _, candidate := range legalPageLangs {
		if lang == candidate {
			return lang
		}
	}
	return "de"
}

func siteConfigLegalTextSaveHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Error: web_db_path not configured in webmin.yaml"), http.StatusSeeOther)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		page := r.FormValue("page")
		if page != "impressum" && page != "privacy" && page != "terms" {
			http.Error(w, "invalid legal page", http.StatusBadRequest)
			return
		}
		lang := selectedLegalLanguage(r.FormValue("lang"))
		if lang != r.FormValue("lang") {
			http.Error(w, "invalid language", http.StatusBadRequest)
			return
		}

		setSiteSetting(db, page+"_"+lang, strings.TrimSpace(r.FormValue("text")))
		var callerID int
		if u := getSessionUser(r); u != nil {
			callerID = u.ID
		}
		log.Printf("audit: site_settings legal_page=%s language=%s updated by user=%d", page, lang, callerID)

		q := url.Values{"legal_lang": {lang}, "flash": {"Legal text saved"}}
		http.Redirect(w, r, "/site-config?"+q.Encode(), http.StatusSeeOther)
	}
}

func siteConfigLegalTextExportHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if !validLegalDocument(page) {
			http.Error(w, "invalid legal document", http.StatusBadRequest)
			return
		}
		if db == nil {
			http.Error(w, "web database is not configured", http.StatusServiceUnavailable)
			return
		}

		export := legalTextJSON{
			Format:    legalTextJSONFormat,
			Version:   1,
			Document:  page,
			Languages: make(map[string]string, len(legalPageLangs)),
		}
		for _, lang := range legalPageLangs {
			var text string
			err := db.QueryRow("SELECT value FROM site_settings WHERE key = ?", page+"_"+lang).Scan(&text)
			if err != nil && err != sql.ErrNoRows {
				log.Printf("export legal text %s/%s: %v", page, lang, err)
				http.Error(w, "could not read legal document", http.StatusInternalServerError)
				return
			}
			export.Languages[lang] = text
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+page+`.json"`)
		if err := json.NewEncoder(w).Encode(export); err != nil {
			log.Printf("encode legal text export %s: %v", page, err)
		}
	}
}

func siteConfigLegalTextImportHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if !validLegalDocument(page) {
			http.Error(w, "invalid legal document", http.StatusBadRequest)
			return
		}
		if db == nil {
			http.Error(w, "web database is not configured", http.StatusServiceUnavailable)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, legalTextImportMaxBytes)
		if err := r.ParseMultipartForm(legalTextImportMaxBytes); err != nil {
			redirectLegalTextImport(w, r, page, "Error: could not read JSON upload")
			return
		}
		defer r.MultipartForm.RemoveAll()

		file, _, err := r.FormFile("file")
		if err != nil {
			redirectLegalTextImport(w, r, page, "Error: choose a JSON file to import")
			return
		}
		defer file.Close()

		var imported legalTextJSON
		decoder := json.NewDecoder(io.LimitReader(file, legalTextImportMaxBytes+1))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&imported); err != nil {
			redirectLegalTextImport(w, r, page, "Error: invalid legal text JSON")
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			redirectLegalTextImport(w, r, page, "Error: JSON file must contain one object")
			return
		}
		if imported.Format != legalTextJSONFormat || imported.Version != 1 || imported.Document != page || len(imported.Languages) == 0 {
			redirectLegalTextImport(w, r, page, "Error: JSON format or document does not match")
			return
		}
		for lang := range imported.Languages {
			if !isLegalLanguage(lang) {
				redirectLegalTextImport(w, r, page, "Error: JSON contains an unsupported language")
				return
			}
		}

		tx, err := db.Begin()
		if err != nil {
			log.Printf("begin legal text import %s: %v", page, err)
			redirectLegalTextImport(w, r, page, "Error: could not import legal text")
			return
		}
		for lang, text := range imported.Languages {
			if _, err := tx.Exec(
				"INSERT INTO site_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
				page+"_"+lang, text,
			); err != nil {
				_ = tx.Rollback()
				log.Printf("save imported legal text %s/%s: %v", page, lang, err)
				redirectLegalTextImport(w, r, page, "Error: could not import legal text")
				return
			}
		}
		if err := tx.Commit(); err != nil {
			log.Printf("commit legal text import %s: %v", page, err)
			redirectLegalTextImport(w, r, page, "Error: could not import legal text")
			return
		}

		var callerID int
		if u := getSessionUser(r); u != nil {
			callerID = u.ID
		}
		log.Printf("audit: site_settings legal_page=%s imported_languages=%d by user=%d", page, len(imported.Languages), callerID)
		redirectLegalTextImport(w, r, page, "Legal text translations imported")
	}
}

func validLegalDocument(page string) bool {
	return page == "impressum" || page == "privacy" || page == "terms"
}

func isLegalLanguage(lang string) bool {
	for _, candidate := range legalPageLangs {
		if lang == candidate {
			return true
		}
	}
	return false
}

func redirectLegalTextImport(w http.ResponseWriter, r *http.Request, page, flash string) {
	q := url.Values{
		"legal_lang": {selectedLegalLanguage(r.FormValue("legal_lang"))},
		"flash":      {flash},
	}
	http.Redirect(w, r, "/site-config?"+q.Encode(), http.StatusSeeOther)
}

// POST /site-config/relay/assets — uploads relay actor avatar and/or banner.
// Kept separate from the main site-config save so uploading an image does not
// overwrite text settings (site_name, impressum, etc.) with empty strings.
func siteConfigRelayAssetsHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.ImagesDir == "" {
			http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("images_dir not configured in webmin.yaml"), http.StatusSeeOther)
			return
		}
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Error: bad request"), http.StatusSeeOther)
			return
		}
		var callerID int
		if u := getSessionUser(r); u != nil {
			callerID = u.ID
		}
		uploaded := handleAssetUploads(r, cfg.ImagesDir, []string{"relay-avatar", "relay-banner"}, true)
		if len(uploaded) > 0 {
			log.Printf("audit: relay assets=[%s] updated by user=%d", strings.Join(uploaded, ","), callerID)
			go notifyRelayProfileUpdate(cfg)
		}
		http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Relay assets uploaded"), http.StatusSeeOther)
	}
}

func notifyRelayProfileUpdate(cfg *Config) {
	target := strings.TrimRight(cfg.WebURL, "/") + "/internal/relay/profile-update"
	req, err := http.NewRequest(http.MethodPost, target, nil)
	if err != nil {
		log.Printf("relay profile update: create request: %v", err)
		return
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("relay profile update: call dansal-web: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		log.Printf("relay profile update: dansal-web returned %s", resp.Status)
	}
}

// POST /site-config/relay/redeliver — calls dansal-web's internal endpoint to
// push Announce activities for all published events to relay followers.
func siteConfigRelayRedeliverHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := strings.TrimRight(cfg.WebURL, "/") + "/internal/relay/redeliver"
		resp, err := httpClient.Post(target, "application/json", nil)
		if err != nil {
			log.Printf("relay redeliver: call dansal-web: %v", err)
			http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Redeliver failed: "+err.Error()), http.StatusSeeOther)
			return
		}
		resp.Body.Close()
		http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Re-delivering events to relay followers in the background"), http.StatusSeeOther)
	}
}

// placesHTTPClient downloads GeoNames dumps (a few MB per country).
var placesHTTPClient = &http.Client{Timeout: 10 * time.Minute}

// startPlaceSync runs places.Sync in the background (#1429); a var so tests
// can observe it without downloading anything.
var startPlaceSync = func(cfg *Config, db *sql.DB, countries []string, force bool) {
	go places.Sync(context.Background(), db, placesHTTPClient, cfg.GeoNamesURL, countries, force)
}

// POST /site-config/places/import — re-import every configured country's
// place names now (#1429), e.g. after a failed download or to pick up
// GeoNames updates.
func siteConfigPlacesImportHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Error: web_db_path not configured in webmin.yaml"), http.StatusSeeOther)
			return
		}
		countries := places.ParseCountries(getSiteSetting(db, "place_countries"))
		if len(countries) == 0 {
			http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("No countries configured for the city search"), http.StatusSeeOther)
			return
		}
		startPlaceSync(cfg, db, countries, true)
		http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Re-importing place names for "+strings.Join(countries, ", ")+" in the background — reload this page to see the status"), http.StatusSeeOther)
	}
}

// siteConfigTimezoneHandler sets server.timezone via the admin socket
// (#1394) — kept as its own form/handler, separate from siteConfigSaveHandler,
// the same way relay-assets and relay-redeliver are: a change here is
// authoritative in the API's own config.yaml, not a site_settings value, and
// the confirmation prompt in the template (data-confirm) is specific to the
// real consequence of this one field (every existing event's displayed
// wall-clock time changes) rather than the generic "settings saved" of the
// rest of the page.
func siteConfigTimezoneHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Error: bad request"), http.StatusSeeOther)
			return
		}
		tz := strings.TrimSpace(r.FormValue("timezone"))
		var callerID int
		if u := getSessionUser(r); u != nil {
			callerID = u.ID
		}
		if _, ok := socketFlashRedirect(w, r, cfg, "/site-config", "Timezone update failed", socketRequest{
			Cmd:      "timezone-set",
			Timezone: tz,
		}); !ok {
			return
		}
		log.Printf("audit: server.timezone set to %s by user=%d", tz, callerID)
		http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Timezone set to "+tz+" — applied immediately, no restart needed"), http.StatusSeeOther)
	}
}
