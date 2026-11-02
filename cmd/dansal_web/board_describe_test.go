package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1741: /board says what it is and counts singular and plural correctly.

// withSiteSettings gives the test a site settings cache, as a running server has.
func withSiteSettings(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	old := siteCfg
	siteCfg = newSiteSettingsCache(db)
	t.Cleanup(func() { siteCfg = old })
}

func boardPost(typ string, eventID int, title string) ContactPost {
	return ContactPost{
		Type:    typ,
		EventID: eventID,
		Persons: 1,
		Event:   &ContactPostEvent{Title: title, StartTime: "2026-12-27T20:00:00Z"},
	}
}

func TestBoardPageTitleSingularAndPlural(t *testing.T) {
	en := loadI18n("").Strings("en")
	if got, want := boardPageTitle(en, []ContactPost{boardPost("ticket_offer", 1, "A")}), "Tickets – 1 post"; got != want {
		t.Errorf("one post: got %q, want %q", got, want)
	}
	two := []ContactPost{boardPost("ticket_offer", 1, "A"), boardPost("ticket_request", 2, "B")}
	if got, want := boardPageTitle(en, two), "Tickets – 2 posts"; got != want {
		t.Errorf("two posts: got %q, want %q", got, want)
	}
	if got := boardPageTitle(en, nil); got != en.T("nav_board") {
		t.Errorf("no posts: got %q, want the bare board label", got)
	}
}

func TestBoardMetaDescLeadWithoutPosts(t *testing.T) {
	withSiteSettings(t)
	en := loadI18n("").Strings("en")
	got := boardMetaDesc(nil, en, "en")
	if got == "" || got != en.T("board_meta_lead") {
		t.Errorf("no posts: got %q, want the board lead", got)
	}
}

func TestBoardMetaDescLeadsWithPostsAndStaysWithinLimit(t *testing.T) {
	withSiteSettings(t)
	en := loadI18n("").Strings("en")
	var posts []ContactPost
	for i := 1; i <= 8; i++ {
		posts = append(posts, boardPost("ride_offer", i, fmt.Sprintf("Very long balfolk event name number %d with many words", i)))
	}
	got := boardMetaDesc(posts, en, "en")
	if !strings.HasPrefix(got, en.T("board_meta_lead")) {
		t.Errorf("description does not lead with the board lead: %q", got)
	}
	if !strings.Contains(got, "Very long balfolk event name number 1") {
		t.Errorf("description lacks the top event: %q", got)
	}
	if n := len([]rune(got)); n > metaDescMaxLen+1 {
		t.Errorf("description has %d runes, limit %d", n, metaDescMaxLen)
	}
}

func TestBoardPageRendersIntroAndLocalisedCardDate(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/board", nil)
	data := BoardData{Posts: []ContactPost{boardPost("ticket_offer", 1, "Ball Kassel")}}
	html := renderPageHTML(t, func(ts *Templates) *template.Template { return ts.board }, req, "Board", data)

	// No language cookie or header: the page renders in the default language.
	i18n := loadI18n("")
	intro := i18n.Strings(i18n.DefaultLang).T("board_intro")
	if !strings.Contains(html, `class="board-intro"`) || !strings.Contains(html, intro) {
		t.Error("board page does not render the intro paragraph")
	}
	if strings.Contains(html, `bgc-meta">· 2026-12-27`) {
		t.Error("card date is still the raw ISO date, not the site format")
	}
	if !strings.Contains(html, `bgc-meta">· `) {
		t.Error("card has no date at all")
	}
}
