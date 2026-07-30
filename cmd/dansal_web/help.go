package main

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"os"
	"path/filepath"

	"github.com/yuin/goldmark"
)

//go:embed help
var helpFS embed.FS

type helpTopic struct {
	ID      string
	I18nKey string // used for the nav group label; title comes from the markdown # heading
}

var publicTopics = []helpTopic{
	{"browse-events", ""},
	{"ical-feeds", ""},
	{"activitypub", ""},
	{"suggest-event", ""},
}

var adminTopics = []helpTopic{
	{"event-form", ""},
	{"location-form", ""},
	{"musicians", ""},
	{"publishing", ""},
	{"feeds-import", ""},
}

// allowedTopics is the complete set of valid topic IDs (prevents path traversal).
var allowedTopics = func() map[string]bool {
	m := make(map[string]bool)
	for _, t := range publicTopics {
		m[t.ID] = true
	}
	for _, t := range adminTopics {
		m[t.ID] = true
	}
	return m
}()

// HelpSystem loads topic markdown from disk (override) or the embedded defaults.
type HelpSystem struct {
	dir string // /etc/dansal/help or "" for embedded-only
}

func newHelpSystem(dir string) *HelpSystem {
	return &HelpSystem{dir: dir}
}

// topicContent returns safe HTML for the given topic, language-resolved.
// Fallback chain: disk lang → disk "en" → embedded lang → embedded "en" → "".
func (h *HelpSystem) topicContent(lang, topicID string) template.HTML {
	if !allowedTopics[topicID] {
		return ""
	}
	// on-disk overrides
	if h.dir != "" {
		if html := h.readAndRender(filepath.Join(h.dir, lang, topicID+".md")); html != "" {
			return html
		}
		if lang != "en" {
			if html := h.readAndRender(filepath.Join(h.dir, "en", topicID+".md")); html != "" {
				return html
			}
		}
	}
	// embedded defaults
	if html := h.renderEmbedded("help/" + lang + "/" + topicID + ".md"); html != "" {
		return html
	}
	if lang != "en" {
		if html := h.renderEmbedded("help/en/" + topicID + ".md"); html != "" {
			return html
		}
	}
	return ""
}

func (h *HelpSystem) readAndRender(path string) template.HTML {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return renderHelpMarkdown(data)
}

func (h *HelpSystem) renderEmbedded(path string) template.HTML {
	data, err := helpFS.ReadFile(path)
	if err != nil {
		return ""
	}
	return renderHelpMarkdown(data)
}

func renderHelpMarkdown(src []byte) template.HTML {
	var buf bytes.Buffer
	if err := goldmark.Convert(src, &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(string(src)))
	}
	return template.HTML(sanitizeMarkdownHTML(buf.String()))
}

// markdownFirstHeading extracts the text of the first `# Heading` line.
func markdownFirstHeading(src []byte) string {
	for _, line := range bytes.Split(src, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("# ")) {
			return string(bytes.TrimPrefix(line, []byte("# ")))
		}
	}
	return ""
}

// topicTitle returns the rendered title of a topic in the given language.
func (h *HelpSystem) topicTitle(lang, topicID string) string {
	if !allowedTopics[topicID] {
		return topicID
	}
	tryFile := func(path string) string {
		var data []byte
		var err error
		if h.dir != "" {
			data, err = os.ReadFile(filepath.Join(h.dir, path))
			if err != nil {
				data, err = helpFS.ReadFile("help/" + path)
			}
		} else {
			data, err = helpFS.ReadFile("help/" + path)
		}
		if err != nil {
			return ""
		}
		return markdownFirstHeading(data)
	}
	if t := tryFile(lang + "/" + topicID + ".md"); t != "" {
		return t
	}
	if lang != "en" {
		if t := tryFile("en/" + topicID + ".md"); t != "" {
			return t
		}
	}
	return topicID
}

// renderedTopic is the template-facing view of one topic.
type renderedTopic struct {
	ID      string
	Title   string
	Content template.HTML
}

type helpPageData struct {
	PublicTopics []renderedTopic
	AdminTopics  []renderedTopic
}

func helpHandler(cfg *Config, tmpls *Templates, i18n *I18n, help *HelpSystem) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := i18n.detectLang(r)
		user := getSessionUser(r)

		render := func(t helpTopic) renderedTopic {
			return renderedTopic{
				ID:      t.ID,
				Title:   help.topicTitle(lang, t.ID),
				Content: help.topicContent(lang, t.ID),
			}
		}

		pub := make([]renderedTopic, 0, len(publicTopics))
		for _, t := range publicTopics {
			pub = append(pub, render(t))
		}

		var adm []renderedTopic
		if user != nil {
			adm = make([]renderedTopic, 0, len(adminTopics))
			for _, t := range adminTopics {
				adm = append(adm, render(t))
			}
		}

		data := helpPageData{
			PublicTopics: pub,
			AdminTopics:  adm,
		}

		title := i18n.Strings(lang).T("nav_help")
		td := tmplData(r, cfg, i18n, title, data)
		td.Hreflang = true
		renderTemplate(w, tmpls.help, td)
	}
}
