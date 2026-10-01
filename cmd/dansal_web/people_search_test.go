package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestSplitPeopleList(t *testing.T) {
	got := splitPeopleList(" Indigo Baur, Christiane Meis;Karola Lubosch\nindigo baur ,, Duo X & Y ")
	want := []string{"Indigo Baur", "Christiane Meis", "Karola Lubosch", "Duo X & Y"}
	if !slices.Equal(got, want) {
		t.Fatalf("splitPeopleList = %q, want %q", got, want)
	}
}

func TestNormalizePersonName(t *testing.T) {
	cases := map[string]string{
		"Dr. Roland Vogel":     "roland vogel",
		"Chant : Judith Laux":  "judith laux",
		"Prof. Dr. Jörg Weiß":  "jorg weiss",
		"Céline  Dupré-Martin": "celine dupre martin",
		"Duo X & Y":            "duo x y",
	}
	for in, want := range cases {
		if got := strings.Join(normalizePersonName(in), " "); got != want {
			t.Errorf("normalizePersonName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchPeople(t *testing.T) {
	entries := peopleEntriesFrom(
		[]Musician{{Bandname: "Roland Vogel"}, {Bandname: "Duo Bow", ShortName: "Bow"}, {Bandname: "Judith Laux"}},
		[]Instructor{{Name: "Sabine Keck"}, {Name: "Christiane Meiß"}},
	)
	res := matchPeople([]string{
		"Dr. Roland Vogel",    // title stripped → known
		"Chant : Judith Laux", // role prefix stripped → known
		"Rolan Vogel",         // typo → similar
		"Bow",                 // short name key → known
		"Sabine Keck",         // exists only as instructor → similar, kind instructor
		"Christiane Meis",     // ß vs s typo across kinds → similar
		"Karola Lubosch",      // nothing → new
	}, "musician", entries)

	want := []struct{ status, match, cand, kind string }{
		{"known", "Roland Vogel", "", ""},
		{"known", "Judith Laux", "", ""},
		{"similar", "", "Roland Vogel", "musician"},
		{"known", "Duo Bow", "", ""},
		{"similar", "", "Sabine Keck", "instructor"},
		{"similar", "", "Christiane Meiß", "instructor"},
		{"new", "", "", ""},
	}
	for i, w := range want {
		r := res[i]
		if r.Status != w.status || r.Match != w.match {
			t.Errorf("%q: status/match = %s/%q, want %s/%q", r.Name, r.Status, r.Match, w.status, w.match)
			continue
		}
		if w.cand != "" && (len(r.Candidates) == 0 || r.Candidates[0].Name != w.cand || r.Candidates[0].Kind != w.kind) {
			t.Errorf("%q: candidates = %+v, want first %s (%s)", r.Name, r.Candidates, w.cand, w.kind)
		}
	}
}

func TestPeopleFromForm(t *testing.T) {
	r := &http.Request{Form: url.Values{
		"dansal_musicians":      {"Legacy Band"},
		"dansal_musicians_text": {"A, legacy band; B"},
	}}
	if got, want := peopleFromForm(r, "dansal_musicians"), []string{"Legacy Band", "A", "B"}; !slices.Equal(got, want) {
		t.Fatalf("peopleFromForm = %q, want %q", got, want)
	}
}

// TestSuggestPagePeopleFields covers #1412: the suggest form renders one
// comma-separated text field per kind plus the "did you mean" dialog, with
// translated strings and valid inline JS.
func TestSuggestPagePeopleFields(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	req := httptest.NewRequest(http.MethodGet, "/events/suggest", nil)
	rec := httptest.NewRecorder()
	renderTemplate(rec, loadTemplates().suggestEvent, tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), "test", SuggestPageData{}))
	body := rec.Body.String()
	checkInlineJS(t, body)
	for _, want := range []string{
		`name="dansal_musicians_text"`,
		`name="dansal_instructors_text"`,
		`id="sg-people-dialog"`,
		`/search/people?kind=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("suggest page missing %s", want)
		}
	}
	for _, raw := range []string{"people_list_hint", "people_dialog_title", "people_status_similar", "people_keep_typed"} {
		if strings.Contains(body, raw) {
			t.Errorf("untranslated i18n key %s in suggest page", raw)
		}
	}
}
