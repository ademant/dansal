package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Logout purges the session cookies in every tab (#1135) but must not purge
// localStorage: it holds per-device preferences and lists (colour scheme,
// starred timetable entries, a future watchlist).
func TestLogoutClearSiteDataKeepsLocalStorage(t *testing.T) {
	handler := logoutHandler(&Config{Domain: "example.test"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if got := rec.Header().Get("Clear-Site-Data"); got != `"cookies"` {
		t.Errorf("Clear-Site-Data = %q, want %q (storage must not be purged)", got, `"cookies"`)
	}
}
