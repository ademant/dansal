package main

import (
	"bytes"
	"flag"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"gopkg.in/yaml.v2"
)

var Version = "dev"
var BuildTime = "unknown"

type Config struct {
	Listen                string `yaml:"listen"`
	Domain                string `yaml:"domain"`
	BaseURL               string `yaml:"base_url"`
	ContentDir            string `yaml:"content_dir"`
	IndexPage             string `yaml:"index_page"`
	SiteName              string `yaml:"site_name"`
	ReadHeaderTimeoutSecs int    `yaml:"read_header_timeout_secs"`
	ReadTimeoutSecs       int    `yaml:"read_timeout_secs"`
	WriteTimeoutSecs      int    `yaml:"write_timeout_secs"`
	IdleTimeoutSecs       int    `yaml:"idle_timeout_secs"`
}

type Page struct {
	Slug     string
	Title    string
	Path     string
	NavOrder int
	HasOrder bool
}

// frontMatter holds the optional metadata block at the top of a wiki page,
// e.g.:
//
//	---
//	nav_order: 2
//	title: Custom title
//	---
//
// It's stripped before the body is handed to goldmark, so it never renders.
// GitHub's wiki viewer doesn't understand this block, so pages using it
// still need a wiki/_Sidebar.md for correct ordering there.
type frontMatter struct {
	NavOrder int    `yaml:"nav_order"`
	Title    string `yaml:"title"`
}

// splitFrontMatter parses a leading "---\n...\n---\n" YAML block, returning
// the parsed metadata and the remaining body. If data has no front matter,
// it returns the zero frontMatter and data unchanged.
func splitFrontMatter(data []byte) (frontMatter, []byte) {
	const delim = "---"
	text := string(data)
	if !strings.HasPrefix(text, delim+"\n") {
		return frontMatter{}, data
	}
	rest := text[len(delim)+1:]
	end := strings.Index(rest, "\n"+delim)
	if end == -1 {
		return frontMatter{}, data
	}
	block := rest[:end]
	body := rest[end+len("\n"+delim):]
	body = strings.TrimPrefix(body, "\n")

	var fm frontMatter
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return frontMatter{}, data
	}
	return fm, []byte(body)
}

type App struct {
	cfg        Config
	pages      map[string]Page
	nav        []Page
	imagesRoot string
	markdown   goldmark.Markdown
}

func main() {
	cfg := loadConfig()
	app, err := newApp(cfg)
	if err != nil {
		log.Fatalf("load docs: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handlePage)
	mux.HandleFunc("/images/", app.handleImage)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: duration(cfg.ReadHeaderTimeoutSecs, 5),
		ReadTimeout:       duration(cfg.ReadTimeoutSecs, 10),
		WriteTimeout:      duration(cfg.WriteTimeoutSecs, 30),
		IdleTimeout:       duration(cfg.IdleTimeoutSecs, 60),
	}

	log.Printf("dansal-doc %s listening on %s content=%s", Version, cfg.Listen, cfg.ContentDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func loadConfig() Config {
	cfg := Config{
		Listen:                "127.0.0.1:8070",
		ContentDir:            "/usr/share/dansal/wiki",
		IndexPage:             "README",
		SiteName:              "Dansal Documentation",
		ReadHeaderTimeoutSecs: 5,
		ReadTimeoutSecs:       10,
		WriteTimeoutSecs:      30,
		IdleTimeoutSecs:       60,
	}

	configPath := flag.String("config", "/etc/dansal/doc.yaml", "path to doc.yaml")
	flag.Parse()

	if *configPath != "" {
		data, err := os.ReadFile(*configPath)
		if err != nil {
			log.Fatalf("read config %s: %v", *configPath, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			log.Fatalf("parse config: %v", err)
		}
	}

	if v := os.Getenv("DANSAL_DOC_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("DANSAL_DOC_DOMAIN"); v != "" {
		cfg.Domain = v
	}
	if v := os.Getenv("DANSAL_DOC_BASE_URL"); v != "" {
		cfg.BaseURL = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("DANSAL_DOC_CONTENT_DIR"); v != "" {
		cfg.ContentDir = v
	}

	if cfg.Domain == "" && cfg.BaseURL == "" {
		log.Fatal("domain or base_url is required")
	}
	if cfg.SiteName == "" {
		cfg.SiteName = "Dansal Documentation"
	}
	cfg.IndexPage = strings.TrimSuffix(strings.TrimSpace(cfg.IndexPage), ".md")
	if cfg.IndexPage == "" {
		cfg.IndexPage = "README"
	}
	return cfg
}

func newApp(cfg Config) (*App, error) {
	entries, err := os.ReadDir(cfg.ContentDir)
	if err != nil {
		return nil, err
	}

	pages := make(map[string]Page)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		if strings.HasPrefix(entry.Name(), "_") {
			continue // e.g. _Sidebar.md — GitHub-wiki-only, not a real page
		}
		path := filepath.Join(cfg.ContentDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		fm, body := splitFrontMatter(data)
		slug := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		title := fm.Title
		if title == "" {
			title = firstHeading(body, slug)
		}
		page := Page{Slug: slug, Title: title, Path: path, NavOrder: fm.NavOrder, HasOrder: fm.NavOrder != 0}
		pages[slug] = page
	}

	nav := make([]Page, 0, len(pages))
	for _, page := range pages {
		nav = append(nav, page)
	}
	sort.Slice(nav, func(i, j int) bool {
		if nav[i].Slug == "README" {
			return true
		}
		if nav[j].Slug == "README" {
			return false
		}
		if nav[i].HasOrder != nav[j].HasOrder {
			return nav[i].HasOrder
		}
		if nav[i].HasOrder {
			return nav[i].NavOrder < nav[j].NavOrder
		}
		return nav[i].Title < nav[j].Title
	})

	imagesRoot, err := filepath.Abs(filepath.Join(cfg.ContentDir, "images"))
	if err != nil {
		return nil, err
	}

	markdown := goldmark.New(
		goldmark.WithExtensions(extension.Table),
	)

	return &App{cfg: cfg, pages: pages, nav: nav, imagesRoot: imagesRoot, markdown: markdown}, nil
}

// handleImage serves screenshots referenced from wiki markdown (e.g.
// "![Setup](images/setup.png)") from content_dir/images. Relative markdown
// links resolve to /images/... the same way on GitHub wiki and here, since
// both drop the current page's last path segment when resolving.
func (app *App) handleImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/images/")
	path := filepath.Join(app.imagesRoot, filepath.Clean("/"+rel))
	if !strings.HasPrefix(path, app.imagesRoot+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (app *App) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slug := strings.Trim(r.URL.Path, "/")
	if slug == "" {
		slug = app.cfg.IndexPage
	}
	if strings.HasSuffix(slug, ".md") {
		http.Redirect(w, r, "/"+strings.TrimSuffix(slug, ".md"), http.StatusMovedPermanently)
		return
	}
	if strings.Contains(slug, "/") || strings.Contains(slug, "\\") || slug == "." || slug == ".." {
		http.NotFound(w, r)
		return
	}

	page, ok := app.pages[slug]
	if !ok {
		http.NotFound(w, r)
		return
	}

	data, err := os.ReadFile(page.Path)
	if err != nil {
		http.Error(w, "read page", http.StatusInternalServerError)
		return
	}
	_, data = splitFrontMatter(data)

	var rendered bytes.Buffer
	if err := app.markdown.Convert(data, &rendered); err != nil {
		http.Error(w, "render page", http.StatusInternalServerError)
		return
	}

	model := struct {
		Config  Config
		Nav     []Page
		Page    Page
		HTML    template.HTML
		Year    int
		Version string
	}{
		Config:  app.cfg,
		Nav:     app.nav,
		Page:    page,
		HTML:    template.HTML(rewriteMarkdownLinks(rendered.String())),
		Year:    time.Now().Year(),
		Version: Version,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, model); err != nil {
		log.Printf("render template: %v", err)
	}
}

func duration(seconds, fallback int) time.Duration {
	if seconds <= 0 {
		seconds = fallback
	}
	return time.Duration(seconds) * time.Second
}

func firstHeading(data []byte, fallback string) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return strings.ReplaceAll(fallback, "-", " ")
}

func rewriteMarkdownLinks(html string) string {
	html = strings.ReplaceAll(html, `.md"`, `"`)
	html = strings.ReplaceAll(html, `.md#`, `#`)
	return html
}

// securityHeaders sets X-Frame-Options and Content-Security-Policy only.
// X-Content-Type-Options and Referrer-Policy are deliberately NOT set here:
// nginx (deploy/nginx/dansal-doc.conf) already applies them unconditionally
// to every response, including ones nginx itself generates that never reach
// this middleware — setting them here too produced duplicate header lines
// (#1142). X-Frame-Options and CSP stay app-side as the single source of
// truth for those.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'self'; frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Page.Title}} - {{.Config.SiteName}}</title>
  <style>
    :root { color-scheme: light dark; --bg:#f7f8fa; --panel:#fff; --text:#1d242d; --muted:#667085; --line:#d9dee7; --accent:#2f6f6d; --code:#eef3f2; }
    @media (prefers-color-scheme: dark) { :root { --bg:#111417; --panel:#191d22; --text:#edf1f5; --muted:#a8b0bb; --line:#303842; --accent:#7cc2bb; --code:#232b2d; } }
    * { box-sizing: border-box; }
    body { margin:0; background:var(--bg); color:var(--text); font:16px/1.58 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    header { border-bottom:1px solid var(--line); background:var(--panel); }
    .top { max-width:1160px; margin:0 auto; padding:18px 24px; display:flex; gap:16px; align-items:center; justify-content:space-between; }
    .brand { color:var(--text); font-weight:700; text-decoration:none; font-size:1.05rem; }
    .version { color:var(--muted); font-size:.88rem; }
    main { max-width:1160px; margin:0 auto; padding:28px 24px 44px; display:grid; grid-template-columns:260px minmax(0, 1fr); gap:34px; }
    nav { border-right:1px solid var(--line); padding-right:22px; }
    nav a { display:block; color:var(--text); text-decoration:none; padding:7px 0; }
    nav a.active { color:var(--accent); font-weight:700; }
    article { min-width:0; }
    article h1 { margin-top:0; line-height:1.15; }
    article h2, article h3 { margin-top:1.8em; line-height:1.25; }
    article a { color:var(--accent); }
    article img { max-width:100%; height:auto; }
    article code { background:var(--code); padding:.12em .32em; border-radius:4px; }
    article pre { background:var(--code); padding:16px; overflow:auto; border-radius:6px; }
    article pre code { padding:0; background:transparent; }
    article table { border-collapse:collapse; width:100%; overflow:auto; display:block; }
    article th, article td { border:1px solid var(--line); padding:8px 10px; }
    footer { max-width:1160px; margin:0 auto; padding:18px 24px 34px; color:var(--muted); border-top:1px solid var(--line); font-size:.9rem; }
    @media (max-width: 760px) {
      .top { padding:14px 18px; align-items:flex-start; flex-direction:column; gap:4px; }
      main { display:block; padding:18px; }
      nav { border-right:0; border-bottom:1px solid var(--line); padding:0 0 16px; margin-bottom:24px; }
      nav a { padding:9px 0; }
    }
  </style>
</head>
<body>
  <header>
    <div class="top">
      <a class="brand" href="/">{{.Config.SiteName}}</a>
      <span class="version">dansal-doc {{.Version}}</span>
    </div>
  </header>
  <main>
    <nav aria-label="Documentation">
      {{range .Nav}}<a href="/{{.Slug}}" class="{{if eq $.Page.Slug .Slug}}active{{end}}">{{.Title}}</a>{{end}}
    </nav>
    <article>{{.HTML}}</article>
  </main>
  <footer>&copy; {{.Year}} Dansal</footer>
</body>
</html>`))
