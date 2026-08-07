package main

import (
	"net/http"
)

func dashboardHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		d := tmplData(r, cfg, "Dashboard", collectDashboard(r.Context(), cfg))
		renderTemplate(w, tmpls.dashboard, d)
	}
}
