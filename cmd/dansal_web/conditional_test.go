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
