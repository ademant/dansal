package main

import (
	"bufio"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var siteAssetExts = []string{".svg", ".avif", ".jpg", ".gif"}

// findSiteAssetOnDisk returns the raw bytes of key.{svg,avif,jpg,gif} from dir, or nil.
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
