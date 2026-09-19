package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckETag(t *testing.T) {
	t.Run("empty etag never matches", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if checkETag(w, r, "") {
			t.Fatal("expected no match for empty etag")
		}
	})

	t.Run("matching If-None-Match returns 304", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("If-None-Match", `"abc"`)
		if !checkETag(w, r, `"abc"`) {
			t.Fatal("expected a match")
		}
		if w.Code != http.StatusNotModified {
			t.Fatalf("expected 304, got %d", w.Code)
		}
	})

	t.Run("mismatched If-None-Match falls through", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("If-None-Match", `"old"`)
		if checkETag(w, r, `"new"`) {
			t.Fatal("expected no match")
		}
		if got := w.Header().Get("ETag"); got != `"new"` {
			t.Fatalf("expected ETag header set to the current value, got %q", got)
		}
	})
}

func TestCheckLastModified(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("zero time never matches", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if checkLastModified(w, r, time.Time{}) {
			t.Fatal("expected no match for zero time")
		}
	})

	t.Run("unchanged since If-Modified-Since returns 304", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("If-Modified-Since", base.Format(http.TimeFormat))
		if !checkLastModified(w, r, base) {
			t.Fatal("expected a match")
		}
		if w.Code != http.StatusNotModified {
			t.Fatalf("expected 304, got %d", w.Code)
		}
	})

	t.Run("changed after If-Modified-Since falls through", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("If-Modified-Since", base.Format(http.TimeFormat))
		if checkLastModified(w, r, base.Add(time.Hour)) {
			t.Fatal("expected no match when the resource changed since")
		}
	})
}

func TestEtagMatches(t *testing.T) {
	for _, c := range []struct {
		inm, etag string
		want      bool
	}{
		{`"a"`, `"a"`, true},
		{`W/"a"`, `"a"`, true}, // nginx compression weakens the ETag
		{`"a"`, `W/"a"`, true},
		{`"x", W/"a"`, `"a"`, true},
		{`*`, `"a"`, true},
		{`"b"`, `"a"`, false},
		{``, `"a"`, false},
		{`"a"`, ``, false},
	} {
		if got := etagMatches(c.inm, c.etag); got != c.want {
			t.Errorf("etagMatches(%q, %q) = %v, want %v", c.inm, c.etag, got, c.want)
		}
	}
}

func TestCheckETagWeakFromCompressedClient(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("If-None-Match", `W/"abc"`)
	if !checkETag(w, r, `"abc"`) || w.Code != http.StatusNotModified {
		t.Fatalf("weak If-None-Match must match: code=%d", w.Code)
	}
}

func TestCheckPublicPage(t *testing.T) {
	i18n := loadI18n("")
	get := func(lang, inm string) (*httptest.ResponseRecorder, bool) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/musicians/1", nil)
		if lang != "" {
			r.AddCookie(&http.Cookie{Name: cookieLang, Value: lang})
		}
		if inm != "" {
			r.Header.Set("If-None-Match", inm)
		}
		return w, checkPublicPage(w, r, i18n, 1_700_000_000, nil)
	}

	w, hit := get("de", "")
	if hit {
		t.Fatal("first request must not be a 304")
	}
	etag := w.Header().Get("ETag")
	if etag == "" || w.Header().Get("Last-Modified") == "" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers: etag=%q lm=%q cc=%q", etag, w.Header().Get("Last-Modified"), w.Header().Get("Cache-Control"))
	}
	if w2, hit := get("de", etag); !hit || w2.Code != http.StatusNotModified {
		t.Fatalf("same language + matching ETag must 304 (code=%d)", w2.Code)
	}
	if w2, hit := get("de", "W/"+etag); !hit || w2.Code != http.StatusNotModified {
		t.Fatalf("weak validator from a compressed client must 304 (code=%d)", w2.Code)
	}
	// Switching language must not be answered from the old language's cache.
	if _, hit := get("en", etag); hit {
		t.Fatal("a different language must not 304 against another language's ETag")
	}
}

func TestCheckPublicPageEndedEventMovesLastModified(t *testing.T) {
	i18n := loadI18n("")
	ended := time.Now().Add(-time.Hour).UTC()
	events := []Event{{EndTime: ended.Format(time.RFC3339)}}
	w := httptest.NewRecorder()
	checkPublicPage(w, httptest.NewRequest(http.MethodGet, "/", nil), i18n, 1_000, events)
	lm, err := http.ParseTime(w.Header().Get("Last-Modified"))
	if err != nil || lm.Unix() != ended.Unix() {
		t.Fatalf("Last-Modified = %v (%v), want the ended event's end time %v", lm, err, ended)
	}
}

func TestCheckPublicPageSkipsLoggedIn(t *testing.T) {
	i18n := loadI18n("")
	w := httptest.NewRecorder()
	r := withSessionUser(httptest.NewRequest(http.MethodGet, "/", nil), &SessionUser{ID: 1})
	if checkPublicPage(w, r, i18n, 1_700_000_000, nil) || w.Header().Get("ETag") != "" {
		t.Fatal("logged-in sessions must skip conditional GET entirely")
	}
}
