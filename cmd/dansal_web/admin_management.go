package main

import (
	"bufio"
	"context"
	"database/sql"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maybeServePrecompressed attempts to serve path or its precompressed variants
// path.br or path.gz depending on Accept-Encoding. Returns true if a file
// was served.
func maybeServePrecompressed(w http.ResponseWriter, r *http.Request, path, contentType string) bool {
	w.Header().Set("Vary", "Accept-Encoding")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "br") {
		if _, err := os.Stat(path + ".br"); err == nil {
			w.Header().Set("Content-Encoding", "br")
			w.Header().Set("Content-Type", contentType)
			http.ServeFile(w, r, path+".br")
			return true
		}
	}
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		if _, err := os.Stat(path + ".gz"); err == nil {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", contentType)
			http.ServeFile(w, r, path+".gz")
			return true
		}
	}
	return false
}

var siteAssetExts = []string{".svg", ".avif", ".png", ".jpg", ".gif"}

// findSiteAssetOnDisk returns the raw bytes of key.{svg,avif,jpg,gif} from dir, or nil.
// findSiteAssetOnDisk returns the raw bytes of key.{svg,avif,jpg,gif} from dir, or nil.
// It will not consider compressed variants — callers should prefer the
// streaming helper maybeServeSiteAsset which can serve .br/.gz variants when
// present and set appropriate headers.
func findSiteAssetOnDisk(dir, key string) []byte {
	if dir == "" {
		return nil
	}
	for _, ext := range siteAssetExts {
		if data, err := os.ReadFile(filepath.Join(dir, key+ext)); err == nil {
			return data
		}
	}
	return nil
}

// maybeServeSiteAsset attempts to serve a site asset from disk, preferring
// precompressed .br/.gz variants when the client supports them. Returns true
// if a file was served.
func maybeServeSiteAsset(w http.ResponseWriter, r *http.Request, dir, key string) bool {
	if dir == "" {
		return false
	}
	for _, ext := range siteAssetExts {
		path := filepath.Join(dir, key+ext)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		mime := detectAssetMIMEFromExt(ext)
		// Respect Save-Data by preferring a small variant if present
		if saveDataOn(r) {
			smallPath := filepath.Join(dir, key+".small"+ext)
			if _, err := os.Stat(smallPath); err == nil {
				if maybeServePrecompressed(w, r, smallPath, mime) {
					return true
				}
				w.Header().Set("Content-Type", mime)
				http.ServeFile(w, r, smallPath)
				return true
			}
		}
		if maybeServePrecompressed(w, r, path, mime) {
			return true
		}
		w.Header().Set("Content-Type", mime)
		http.ServeFile(w, r, path)
		return true
	}
	return false
}

func detectAssetMIMEFromExt(ext string) string {
	switch ext {
	case ".svg":
		return "image/svg+xml"
	case ".avif":
		return "image/avif"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}

// detectAssetMIME returns the MIME type for supported site asset formats
// (SVG, AVIF, JPEG, GIF) or "" if the data is not a recognised format.
func detectAssetMIME(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	// SVG: text-based, look for the <svg element
	s := strings.TrimSpace(string(data[:min(len(data), 512)]))
	if strings.HasPrefix(s, "<svg") || strings.HasPrefix(s, "<?xml") || strings.Contains(s, "<svg") {
		return "image/svg+xml"
	}
	// AVIF: ISO BMFF ftyp box with avif/avis brand
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		end := len(data)
		if end > 128 {
			end = 128
		}
		for i := 8; i+4 <= end; i += 4 {
			if i == 12 {
				continue // minor version, not a brand
			}
			switch string(data[i : i+4]) {
			case "avif", "avis":
				return "image/avif"
			}
		}
	}
	// PNG: 89 50 4E 47 magic
	if len(data) >= 4 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "image/png"
	}
	// JPEG: FF D8 magic
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8 {
		return "image/jpeg"
	}
	// GIF: GIF87a or GIF89a magic
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "image/gif"
	}
	return ""
}

type AdminInfoData struct {
	WebVersion   string
	WebBuildTime string
	API          DansalInfo
	OutboundIP   string
	LoadAvg      string
}

type AdminStatsData struct {
	Info DansalInfo
}

func adminStatsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		info, _ := client.GetDansalInfo(r.Context())
		renderTemplate(w, tmpls.adminStats, tmplData(r, cfg, i18n, i18n.T(r, "admin_stats_title"), AdminStatsData{Info: info}))
	}
}

func adminManagementHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		title := i18n.T(r, "admin_management_title")
		renderTemplate(w, tmpls.adminManagement, tmplData(r, cfg, i18n, title, nil))
	}
}

func adminInfoHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		info, _ := client.GetDansalInfo(r.Context())
		outboundIP := outboundIP()
		loadAvg := readLoadAvg()

		data := AdminInfoData{
			WebVersion:   Version,
			WebBuildTime: BuildTime,
			API:          info,
			OutboundIP:   outboundIP,
			LoadAvg:      loadAvg,
		}
		renderTemplate(w, tmpls.adminInfo, tmplData(r, cfg, i18n, "System info", data))
	}
}

func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func readLoadAvg() string {
	f, err := os.Open("/proc/loadavg")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 3 {
			return strings.Join(fields[:3], " ")
		}
	}
	return ""
}

// relayAssetURL returns the URL and MIME type for a relay actor image asset.
// It prefers an uploaded file in ImagesDir (served at servedAt path), then
// falls back to the URL set in config.
func relayAssetURL(cfg *Config, fileKey, servedAt, configURL string) (string, string) {
	if cfg.ImagesDir != "" {
		for _, ext := range siteAssetExts {
			if info, err := os.Stat(filepath.Join(cfg.ImagesDir, fileKey+ext)); err == nil {
				// Remote servers commonly cache actor images.  Include the file's
				// modification time so replacing an uploaded image yields a new URL
				// when the actor profile is refreshed.
				return "https://" + cfg.Domain + servedAt + "?v=" + strconv.FormatInt(info.ModTime().UnixNano(), 10), detectAssetMIMEFromExt(ext)
			}
		}
	}
	if configURL != "" {
		return configURL, "image/jpeg"
	}
	return "", ""
}

// POST /internal/relay/redeliver — localhost-only endpoint called by dansal-webmin
// to push Announce activities for all published events to relay followers.
func internalRelayRedeliverHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if host != "127.0.0.1" && host != "::1" {
			http.NotFound(w, r)
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			relayActor, err := ensureRelayActor(db, cfg.RelayActorName)
			if err != nil {
				log.Printf("relay redeliver: get actor: %v", err)
				return
			}
			params := url.Values{
				"limit":        {"500"},
				"future":       {"true"},
				"include_past": {"true"},
			}
			events, err := client.GetEventsFiltered(ctx, params)
			if err != nil {
				log.Printf("relay redeliver: get events: %v", err)
				return
			}
			sent := 0
			for _, e := range events {
				if !e.IsPublished || e.OrganizationID == nil {
					continue
				}
				orgActor, oerr := getActorByOrgID(db, *e.OrganizationID)
				if oerr != nil {
					continue
				}
				activity := buildAnnounceActivity(cfg, relayActor.OrgSlug, orgActor.OrgSlug, e)
				if err := deliverToFollowers(cfg, db, relayActor, activity); err != nil {
					log.Printf("relay redeliver event %d: %v", e.ID, err)
				} else {
					sent++
				}
			}
			log.Printf("relay redeliver: sent %d Announce activities", sent)
		}()
		w.WriteHeader(http.StatusAccepted)
	}
}

// internalRelayProfileUpdateHandler is called by webmin after a relay asset
// upload. Like redelivery, it is only reachable from the local machine.
func internalRelayProfileUpdateHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if host != "127.0.0.1" && host != "::1" {
			http.NotFound(w, r)
			return
		}
		go deliverRelayProfileUpdate(cfg, db)
		w.WriteHeader(http.StatusNoContent)
	}
}
