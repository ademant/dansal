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
		d := tmplData(cfg, "Dashboard", dashboardDataMap(collectDashboard(r.Context(), cfg)))
		d.User = getSessionUser(r)
		renderTemplate(w, tmpls.dashboard, d)
	}
}
