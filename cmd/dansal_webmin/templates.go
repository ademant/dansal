package main

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
)

//go:embed templates
var templateFS embed.FS

type Templates struct {
	login         *template.Template
	dashboard     *template.Template
	users         *template.Template
	sessions      *template.Template
	notifications *template.Template
	maintenance   *template.Template
	siteConfig    *template.Template
	botStats      *template.Template
}

var tmplFuncs = template.FuncMap{
	"pct": func(part, total int) string {
		if total == 0 {
			return "0"
		}
		return fmt.Sprintf("%d", 100*part/total)
	},
	"add":   func(a, b int) int { return a + b },
	"bytes": fmtBytes,
}

func loadTemplates() *Templates {
	load := func(page string) *template.Template {
		t, err := template.New("base").Funcs(tmplFuncs).ParseFS(templateFS,
			"templates/base.html", "templates/"+page+".html")
		if err != nil {
			log.Fatalf("load template %s: %v", page, err)
		}
		return t
	}
	return &Templates{
		login:         load("login"),
		dashboard:     load("dashboard"),
		users:         load("users"),
		sessions:      load("sessions"),
		notifications: load("notifications"),
		maintenance:   load("maintenance"),
		siteConfig:    load("siteconfig"),
		botStats:      load("botstats"),
	}
}

type TemplateData struct {
	Title    string
	SiteName string
	User     *SessionUser
	Data     any
	Version  string
	NavPath  string // top-level path for active nav highlighting
	Nonce    string // per-request CSP nonce; every inline <script> must carry nonce="{{$.Nonce}}" (#1141)
}

func tmplData(r *http.Request, cfg *Config, title string, data any) TemplateData {
	nav := r.URL.Path
	// normalise sub-pages to their top-level nav entry: sessions live under
	// /users/{email}/sessions, which must highlight the Users nav item.
	if strings.HasPrefix(nav, "/users/") {
		nav = "/users"
	}
	return TemplateData{
		Title:    title,
		SiteName: cfg.SiteName,
		User:     getSessionUser(r),
		Data:     data,
		Version:  Version,
		NavPath:  nav,
		Nonce:    nonceFromRequest(r),
	}
}

func renderTemplate(w http.ResponseWriter, t *template.Template, data TemplateData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		log.Printf("template error: %v", err)
	}
}
