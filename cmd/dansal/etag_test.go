package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWeakEtag(t *testing.T) {
	if got := weakEtag(1234); got != `"1234"` {
		t.Fatalf("weakEtag(1234) = %q, want %q", got, `"1234"`)
	}
}

func TestCheckIfMatchConflict(t *testing.T) {
	t.Run("no If-Match header preserves last-write-wins", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/events/1", nil)
		if checkIfMatchConflict(w, r, 100) {
			t.Fatal("expected no conflict when If-Match is absent")
		}
		if w.Code != 200 {
			t.Fatalf("expected untouched response, got status %d", w.Code)
		}
	})

	t.Run("matching If-Match passes", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/events/1", nil)
		r.Header.Set("If-Match", weakEtag(100))
		if checkIfMatchConflict(w, r, 100) {
			t.Fatal("expected no conflict when If-Match matches current etag")
		}
	})

	t.Run("wildcard If-Match always passes", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/events/1", nil)
		r.Header.Set("If-Match", "*")
		if checkIfMatchConflict(w, r, 999) {
			t.Fatal("expected no conflict for If-Match: *")
		}
	})

	t.Run("stale If-Match writes 412 and reports conflict", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/events/1", nil)
		r.Header.Set("If-Match", weakEtag(50))
		if !checkIfMatchConflict(w, r, 100) {
			t.Fatal("expected a conflict when If-Match is stale")
		}
		if w.Code != http.StatusPreconditionFailed {
			t.Fatalf("expected 412, got %d", w.Code)
		}
	})
}
