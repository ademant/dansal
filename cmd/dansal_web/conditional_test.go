package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
