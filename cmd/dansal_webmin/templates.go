package main

import (
	"embed"
	"html/template"
	"log"
	"net/http"
)

//go:embed templates
var templateFS embed.FS

type Templates struct {
	login     *template.Template
	dashboard *template.Template
}

func loadTemplates() *Templates {
	load := func(page string) *template.Template {
		t, err := template.New("base").ParseFS(templateFS,
			"templates/base.html", "templates/"+page+".html")
		if err != nil {
			log.Fatalf("load template %s: %v", page, err)
		}
		return t
	}
	return &Templates{
		login:     load("login"),
		dashboard: load("dashboard"),
	}
}

type TemplateData struct {
	Title    string
	SiteName string
	User     *SessionUser
	Data     any
	Version  string
}

func tmplData(cfg *Config, title string, data any) TemplateData {
	return TemplateData{
		Title:    title,
		SiteName: cfg.SiteName,
		Data:     data,
		Version:  Version,
	}
}

func renderTemplate(w http.ResponseWriter, t *template.Template, data TemplateData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		log.Printf("template error: %v", err)
	}
}
