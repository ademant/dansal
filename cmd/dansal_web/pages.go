package main

import (
	"bytes"
	"html"
	"html/template"
	"log"
	"os"
	"path/filepath"

	"github.com/yuin/goldmark"
	"gopkg.in/yaml.v2"
)

type PagesContent struct {
	Contact   map[string]string `yaml:"contact"`
	Impressum map[string]string `yaml:"impressum"`
}

func loadPagesContent(path string) *PagesContent {
	if path == "" {
		return &PagesContent{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("pages: read %s: %v", path, err)
		return &PagesContent{}
	}
	var pc PagesContent
	if err := yaml.Unmarshal(data, &pc); err != nil {
		log.Printf("pages: parse %s: %v", path, err)
		return &PagesContent{}
	}
	return &pc
}

func (pc *PagesContent) ContactText(lang string) string {
	if pc == nil || pc.Contact == nil {
		return ""
	}
	if v, ok := pc.Contact[lang]; ok {
		return v
	}
	if v, ok := pc.Contact["de"]; ok {
		return v
	}
	return ""
}

func (pc *PagesContent) ImpressumText(lang string) string {
	if pc == nil || pc.Impressum == nil {
		return ""
	}
	if v, ok := pc.Impressum[lang]; ok {
		return v
	}
	if v, ok := pc.Impressum["de"]; ok {
		return v
	}
	return ""
}

func (pc *PagesContent) ImpressumHTML(lang string) template.HTML {
	text := pc.ImpressumText(lang)
	if text == "" {
		return ""
	}
	return template.HTML(`<pre class="impressum-text">` + html.EscapeString(text) + `</pre>`)
}

// LegalMarkdownHTML reads {dir}/{name}.md, renders it as sanitised HTML.
// Returns "" when the file does not exist or dir is empty.
func LegalMarkdownHTML(dir, name string) template.HTML {
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, name+".md"))
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := goldmark.Convert(data, &buf); err != nil {
		log.Printf("legal markdown %s: render error: %v", name, err)
		return template.HTML(`<pre>` + html.EscapeString(string(data)) + `</pre>`)
	}
	return template.HTML(sanitizeMarkdownHTML(buf.String()))
}
