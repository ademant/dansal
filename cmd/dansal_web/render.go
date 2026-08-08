package main

import (
	"html/template"
	"log"
	"net/http"
	"strings"
)

// Page rendering — renderTemplate/renderEmbed share one execute path and
// renderPage wires up the common page tail (meta description + OG image) that
// public handlers used to repeat (#1031).

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

// render writes tmpl to w. withBase selects ExecuteTemplate("base") for pages
// wrapped in base.html vs Execute for standalone embed templates.
func render(w http.ResponseWriter, tmpl *template.Template, data any, withBase bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var err error
	if withBase {
		err = tmpl.ExecuteTemplate(w, "base", data)
	} else {
		err = tmpl.Execute(w, data)
	}
	if err != nil {
		// The response status is already committed (streaming template), so
		// calling http.Error here would trigger a superfluous WriteHeader
		// warning. Log genuine template bugs; silently drop client disconnects.
		if !isClientDisconnect(err) {
			log.Printf("template error: %v", err)
		}
	}
}

func renderTemplate(w http.ResponseWriter, tmpl *template.Template, data any) {
	render(w, tmpl, data, true)
}

// renderEmbed renders a standalone embed template (no base.html wrapper).
func renderEmbed(w http.ResponseWriter, tmpl *template.Template, data any) {
	render(w, tmpl, data, false)
}

// renderPage renders a full page template with the shared page tail: meta
// description plus an absolute OG image URL ("" to keep the default banner).
func renderPage(w http.ResponseWriter, cfg *Config, tmpl *template.Template, td TemplateData, metaDescription, ogImage string) {
	td.MetaDescription = metaDescription
	if ogImage != "" {
		td.OGImage = "https://" + cfg.Domain + ogImage
	}
	renderTemplate(w, tmpl, td)
}
