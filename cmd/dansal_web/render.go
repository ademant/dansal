package main

import (
	"github.com/ademant/dansal/internal/logging"
	"html/template"
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
			logging.Errorf("template error: %v", err)
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

// ErrorPageData is templates/error.html's .Data.
type ErrorPageData struct {
	Title    string
	Message  string
	BackHome string
	// #1661: optional extras for the legacy-URL 410 page — "maybe you're
	// looking for" links derived from the old slug, plus further nav links.
	SuggestHeading string
	Suggestions    []ErrorPageSuggestion
	Links          []ErrorPageLink
}

// ErrorPageSuggestion is one candidate on the legacy 410 page. StartTime is
// set for events (formatted in the template), Sub for venues (the town).
type ErrorPageSuggestion struct {
	Title     string
	URL       string
	StartTime string
	Sub       string
}

type ErrorPageLink struct {
	Label string
	URL   string
}

// errorPageDeps holds the one Config/Templates/I18n instance this process
// loaded at startup, so htmlError/forbidden can render the HTML error page
// from deep call sites (helpers and middleware) that only ever receive
// (w, r) — adding cfg/tmpls/i18n to all ~40 of those call sites across the
// codebase would be a much larger, riskier change for the same result
// (#1512). Safe because dansal_web runs one process per instance (see
// flash.go's flashStore comment) — there is exactly one of each for the
// process's lifetime, set once by setErrorPageDeps during startup.
var errorPageDeps struct {
	cfg   *Config
	tmpls *Templates
	i18n  *I18n
}

func setErrorPageDeps(cfg *Config, tmpls *Templates, i18n *I18n) {
	errorPageDeps.cfg, errorPageDeps.tmpls, errorPageDeps.i18n = cfg, tmpls, i18n
}

// wantsHTML reports whether r is a normal browser navigation that should get
// the styled HTML error page rather than a JSON error body — i.e. its Accept
// header actually prefers text/html, which a fetch()/XHR/API client's rarely
// does (they send "application/json" or "*/*").
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// errorPageTextKeys maps a status code onto its (title, message) i18n keys;
// any status without an entry falls back to the generic wording.
var errorPageTextKeys = map[int][2]string{
	http.StatusBadRequest: {"error_page_400_title", "error_page_400_message"},
	http.StatusForbidden:  {"error_page_403_title", "error_page_403_message"},
	http.StatusNotFound:   {"error_page_404_title", "error_page_404_message"},
	http.StatusGone:       {"error_page_410_title", "error_page_410_message"},
}

// htmlError renders the generic styled HTML error page for status when r
// prefers HTML (see wantsHTML), otherwise falls back to logHTTPError (plain
// text + error_id) — the same split forbidden() uses, for the public-page
// and middleware call sites that aren't a plain 403 (#1512). Deliberately
// generic wording only (no per-call-site detail message): these are all
// terminal "this isn't here" pages for an end user, and a shared instance
// means every language gets a translated page for free instead of each call
// site needing its own i18n keys for a one-off detail.
func htmlError(w http.ResponseWriter, r *http.Request, status int) {
	if !wantsHTML(r) || errorPageDeps.tmpls == nil {
		logHTTPError(w, r, http.StatusText(status), status)
		return
	}
	titleKey, msgKey := "error_page_generic_title", "error_page_generic_message"
	if keys, ok := errorPageTextKeys[status]; ok {
		titleKey, msgKey = keys[0], keys[1]
	}
	i18n := errorPageDeps.i18n
	renderErrorPage(w, r, status, ErrorPageData{
		Title:    i18n.T(r, titleKey),
		Message:  i18n.T(r, msgKey),
		BackHome: i18n.T(r, "error_page_back_home"),
	})
}

// renderErrorPage writes the styled error page with status. Callers check
// wantsHTML and errorPageDeps.tmpls first.
func renderErrorPage(w http.ResponseWriter, r *http.Request, status int, data ErrorPageData) {
	td := tmplData(r, errorPageDeps.cfg, errorPageDeps.i18n, data.Title, data)
	// Content-Type must be set before WriteHeader; renderTemplate's own
	// identical Header().Set() call after is then a harmless no-op.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	renderTemplate(w, errorPageDeps.tmpls.errorPage, td)
}
