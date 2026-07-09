package main

import (
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

	_ "github.com/mattn/go-sqlite3"
)

var siteConfigLangs = []string{"de", "en", "fr", "nl", "it", "es", "br"}
var siteAssetExts = []string{".svg", ".avif", ".jpg", ".gif"}

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
	db.QueryRow("SELECT value FROM site_settings WHERE key = ?", key).Scan(&v)
	return v
}

func setSiteSetting(db *sql.DB, key, value string) {
	db.Exec(
		"INSERT INTO site_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		key, value)
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
	peek := len(data)
	if peek > 512 {
		peek = 512
	}
	s := strings.TrimSpace(string(data[:peek]))
	if strings.HasPrefix(s, "<svg") || strings.HasPrefix(s, "<?xml") || strings.Contains(s, "<svg") {
		return "image/svg+xml"
	}
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		end := len(data)
		if end > 128 {
			end = 128
		}
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

func fetchDances(dansalURL string) []dance {
	resp, err := http.Get(dansalURL + "/api/v1/dances")
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()
	var dances []dance
	json.NewDecoder(resp.Body).Decode(&dances)
	return dances
}

func loadDefaultDanceIDs(db *sql.DB) map[int]bool {
	raw := getSiteSetting(db, "default_dance_ids")
	if raw == "" {
		return nil
	}
	var ids []int
	json.Unmarshal([]byte(raw), &ids)
	m := make(map[int]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

type siteConfigData struct {
	Flash           string
	SiteName        string
	Contact         string
	HasLogo         bool
	HasBanner       bool
	HasFavicon      bool
	Dances          []dance
	DefaultDanceIDs map[int]bool
	ImpressumTexts  map[string]string
	ImpressumLangs  []string
	HolidayCountry  string
	NoDB            bool
	NoImagesDir     bool
}

func siteConfigPageHandler(cfg *Config, tmpls *Templates, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := tmplData(r, cfg, "Site configuration", nil)
		d.User = getSessionUser(r)

		data := siteConfigData{
			Flash:          r.URL.Query().Get("flash"),
			ImpressumLangs: siteConfigLangs,
		}
		if db == nil {
			data.NoDB = true
			d.Data = data
			renderTemplate(w, tmpls.siteConfig, d)
			return
		}

		data.SiteName = getSiteSetting(db, "site_name")
		data.Contact = getSiteSetting(db, "contact")
		data.HolidayCountry = getSiteSetting(db, "holiday_country")

		impTexts := make(map[string]string)
		for _, lang := range siteConfigLangs {
			impTexts[lang] = getSiteSetting(db, "impressum_"+lang)
		}
		data.ImpressumTexts = impTexts
		data.Dances = fetchDances(cfg.DansalURL)
		data.DefaultDanceIDs = loadDefaultDanceIDs(db)

		if cfg.ImagesDir == "" {
			data.NoImagesDir = true
		} else {
			data.HasLogo = siteAssetExists(cfg.ImagesDir, "logo")
			data.HasBanner = siteAssetExists(cfg.ImagesDir, "banner")
			data.HasFavicon = siteAssetExists(cfg.ImagesDir, "favicon")
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
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var callerID int
		if u := getSessionUser(r); u != nil {
			callerID = u.ID
		}

		setSiteSetting(db, "site_name", strings.TrimSpace(r.FormValue("site_name")))
		setSiteSetting(db, "contact", strings.TrimSpace(r.FormValue("contact")))
		setSiteSetting(db, "holiday_country", strings.ToUpper(strings.TrimSpace(r.FormValue("holiday_country"))))

		for _, lang := range siteConfigLangs {
			setSiteSetting(db, "impressum_"+lang, strings.TrimSpace(r.FormValue("impressum_"+lang)))
		}

		var defaultDanceIDs []int
		for _, v := range r.MultipartForm.Value["default_dance_ids"] {
			if n, err := strconv.Atoi(v); err == nil {
				defaultDanceIDs = append(defaultDanceIDs, n)
			}
		}
		j, _ := json.Marshal(defaultDanceIDs)
		setSiteSetting(db, "default_dance_ids", string(j))

		var uploadedAssets []string
		if cfg.ImagesDir != "" {
			for _, key := range []string{"logo", "banner", "favicon"} {
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
				if key != "favicon" && mime == "image/gif" {
					continue
				}
				if err := saveSiteAsset(cfg.ImagesDir, key, data); err != nil {
					log.Printf("save site asset %s: %v", key, err)
				} else {
					uploadedAssets = append(uploadedAssets, key)
				}
			}
		}

		if len(uploadedAssets) > 0 {
			log.Printf("audit: site_settings assets=[%s] updated by user=%d", strings.Join(uploadedAssets, ","), callerID)
		}
		log.Printf("audit: site_settings keys=[site_name,contact,holiday_country,impressum_*,default_dance_ids] updated by user=%d", callerID)

		http.Redirect(w, r, "/site-config?flash="+url.QueryEscape("Settings saved"), http.StatusSeeOther)
	}
}
