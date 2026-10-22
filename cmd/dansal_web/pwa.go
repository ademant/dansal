package main

import (
	_ "embed"
	"net/http"
	"strings"
)

// The progressive web app (#1589): the service worker at /sw.js, the offline
// page at /offline, and the PNG icons the manifest lists. The worker caches
// static assets and the offline page only. It never caches HTML pages, since a
// cached page can carry personal data that a shared device would show offline.

//go:embed static/sw.js
var swJS string

// swHandler serves /sw.js. The cache name carries the build version, so each
// deploy gets a new cache and the old one is dropped when the new worker
// activates.
func swHandler() http.HandlerFunc {
	body := strings.ReplaceAll(swJS, "__VERSION__", Version)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(body))
	}
}

// offlineHandler serves /offline, the page shown for a navigation made without
// a connection.
func offlineHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := i18n.T(r, "pwa_offline_title")
		renderTemplate(w, tmpls.offline, tmplData(r, cfg, i18n, title, nil))
	}
}

// pwaIconHandler serves a square PNG of the site favicon for the manifest.
func pwaIconHandler(imagesDir string, fallbackSVG []byte, size int) http.HandlerFunc {
	return iconHandler(imagesDir, fallbackSVG, "image/png", func(s *iconSource) ([]byte, error) {
		return s.png(size, true)
	})
}
