package main

import (
	"database/sql"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// renderPageHTML renders one page template with the shared test setup and fails
// on a template error.
func renderPageHTML(t *testing.T, pick func(*Templates) *template.Template, req *http.Request, title string, data any) string {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)
	tmpls := loadTemplates()
	rec := httptest.NewRecorder()
	renderTemplate(rec, pick(tmpls), tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), title, data))
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}
	return html
}

// TestHomepageBoardLinkSitsBesideTheTablist covers #1730: the board link is part
// of the phone bottom bar, but outside the tablist, which holds only the three
// tabs. Desktop keeps the hamburger entry; the phone hides it.
func TestHomepageBoardLinkSitsBesideTheTablist(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	html := renderPageHTML(t, func(ts *Templates) *template.Template { return ts.index }, req, "Events", IndexData{})

	if n := strings.Count(html, `role="tab" class="mtab"`); n != 3 {
		t.Errorf("tablist must hold exactly 3 tabs, found %d", n)
	}
	link := strings.Index(html, `<a class="mtab-link" href="/board">`)
	if link < 0 {
		t.Fatal("homepage bottom bar has no board link")
	}
	tablistStart := strings.Index(html, `role="tablist"`)
	tablistEnd := strings.Index(html[tablistStart:], `</div>`) + tablistStart
	if link < tablistStart || link < tablistEnd {
		t.Errorf("board link must come after the tablist, not inside it")
	}
	if strings.Contains(html, `class="mtab-link" href="/board" aria-current`) {
		t.Error("the homepage is not the board page: its link must not be aria-current")
	}
	if !strings.Contains(html, `class="mtabs-wrap"`) {
		t.Error("homepage bottom bar wrapper missing")
	}
}

// TestBoardPageHasTheFourLinkBottomBar covers #1730: /board shows the same
// destinations as the homepage tabs, with the board as the current page.
func TestBoardPageHasTheFourLinkBottomBar(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/board", nil)
	html := renderPageHTML(t, func(ts *Templates) *template.Template { return ts.board }, req, "Board", BoardData{})

	bar := strings.Index(html, `class="mnav" id="board-mnav"`)
	if bar < 0 {
		t.Fatal("board page has no phone bottom bar")
	}
	end := strings.Index(html[bar:], `</div>`) + bar
	barHTML := html[bar:end]
	for _, want := range []string{`href="/?view=list"`, `href="/?view=week"`, `href="/?view=map"`} {
		if !strings.Contains(barHTML, want) {
			t.Errorf("board bottom bar missing %s", want)
		}
	}
	if !strings.Contains(barHTML, `class="mtab-link" href="/board" aria-current="page"`) {
		t.Error("board bottom bar must mark the board link as the current page")
	}
	if strings.Contains(barHTML, `role="tab"`) {
		t.Error("board bottom bar is a navigation bar, not a tablist")
	}
}
