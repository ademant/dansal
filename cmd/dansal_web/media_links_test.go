package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMediaLinksFromForm(t *testing.T) {
	form := url.Values{
		"media_kind":  {"video", "audio", "other"},
		"media_title": {"Live", "", "Blank"},
		"media_url":   {" https://youtu.be/x ", "https://example.org/a.mp3", "  "},
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ParseForm()

	got := mediaLinksFromForm(r)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (blank-URL row skipped): %+v", len(got), got)
	}
	if got[0] != (MediaLink{Kind: "video", Title: "Live", URL: "https://youtu.be/x"}) {
		t.Errorf("row 0 = %+v", got[0])
	}
	if got[1].Kind != "audio" || got[1].Title != "" {
		t.Errorf("row 1 = %+v", got[1])
	}

	empty := httptest.NewRequest("POST", "/", nil)
	empty.ParseForm()
	if links := mediaLinksFromForm(empty); links == nil || len(links) != 0 {
		t.Errorf("no rows must yield a non-nil empty slice (means clear), got %#v", links)
	}
}

func TestMediaLinkHostAndTitle(t *testing.T) {
	l := MediaLink{URL: "https://www.youtube.com/watch?v=abc"}
	if l.Host() != "youtube.com" {
		t.Errorf("Host = %q", l.Host())
	}
	if l.DisplayTitle() != "youtube.com" {
		t.Errorf("untitled link should show host, got %q", l.DisplayTitle())
	}
	l.Title = " Live "
	if l.DisplayTitle() != "Live" {
		t.Errorf("DisplayTitle = %q", l.DisplayTitle())
	}
}

// musicianWrite must omit "media" when the caller never set it (so other API
// clients' lists survive) and send [] when the form cleared every row.
func TestMusicianWriteMediaSemantics(t *testing.T) {
	m := Musician{Bandname: "Band"}
	b, _ := json.Marshal(musicianWrite{Musician: m})
	if strings.Contains(string(b), `"media"`) {
		t.Errorf("nil media must be omitted: %s", b)
	}

	empty := []MediaLink{}
	b, _ = json.Marshal(musicianWrite{Musician: m, Media: &empty})
	if !strings.Contains(string(b), `"media":[]`) {
		t.Errorf("empty non-nil media must be sent as []: %s", b)
	}

	links := []MediaLink{{Kind: "video", URL: "https://example.org/v"}}
	b, _ = json.Marshal(musicianWrite{Musician: Musician{Bandname: "Band", Media: links}, Media: &links})
	if strings.Count(string(b), `"media"`) != 1 {
		t.Errorf("shadowed field must marshal once: %s", b)
	}
}

func TestMergeMediaLists(t *testing.T) {
	a := MediaLink{Kind: "video", URL: "https://example.org/a"}
	b := MediaLink{Kind: "audio", URL: "https://example.org/b"}

	if got := mergeMediaLists(nil, nil); got != nil {
		t.Fatalf("empty+empty must stay nil (so nothing is sent), got %#v", got)
	}
	got := mergeMediaLists([]MediaLink{a}, []MediaLink{a, b})
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("want [a b] with base first and duplicate URL dropped, got %#v", got)
	}

	var many []MediaLink
	for i := 0; i < maxMediaLinks; i++ {
		many = append(many, MediaLink{Kind: "other", URL: "https://example.org/" + string(rune('a'+i))})
	}
	if got := mergeMediaLists(many, []MediaLink{{Kind: "other", URL: "https://example.org/new"}}); len(got) != maxMediaLinks {
		t.Fatalf("cap not enforced: %d", len(got))
	}
}

func TestOrgAndLocationWriteMediaSemantics(t *testing.T) {
	// nil -> key omitted (leave stored links alone); empty non-nil -> [] (clear).
	loc := Location{ID: 1}
	b, _ := json.Marshal(locationWrite{Location: loc, Media: mediaPtr(loc.Media)})
	if strings.Contains(string(b), `"media"`) {
		t.Fatalf("nil media must be omitted: %s", b)
	}
	loc.Media = []MediaLink{}
	b, _ = json.Marshal(locationWrite{Location: loc, Media: mediaPtr(loc.Media)})
	if !strings.Contains(string(b), `"media":[]`) {
		t.Fatalf("empty media must send []: %s", b)
	}
	org := Organization{ID: 1, Media: []MediaLink{{Kind: "video", URL: "https://example.org/v"}}}
	b, _ = json.Marshal(orgWrite{Organization: org, Media: mediaPtr(org.Media)})
	if !strings.Contains(string(b), `"url":"https://example.org/v"`) {
		t.Fatalf("org media not sent: %s", b)
	}
}
