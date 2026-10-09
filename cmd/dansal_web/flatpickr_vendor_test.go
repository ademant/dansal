package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFlatpickrVendored covers #1448 (compliance G11): flatpickr is
// self-hosted now instead of loaded from unpkg.com. Checks the precomputed
// assets staticjs.go's init() built from the embedded files, the same way
// the routes in main.go serve them.
func TestFlatpickrVendored(t *testing.T) {
	if len(flatpickrJSMin) == 0 {
		t.Fatal("flatpickrJSMin is empty")
	}
	if !strings.Contains(string(flatpickrJSMin), "flatpickr") {
		t.Error("expected flatpickr's own source in flatpickrJSMin")
	}
	if len(flatpickrCSSMin) == 0 {
		t.Fatal("flatpickrCSSMin is empty")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/static/flatpickr/flatpickr.js", nil)
	serveNegotiatedStatic(rec, req, "application/javascript", flatpickrJSMin, flatpickrJSGzip, flatpickrJSBrotli)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	for _, loc := range flatpickrLocaleCodes {
		asset, ok := flatpickrLocales[loc]
		if !ok {
			t.Errorf("locale %s missing from flatpickrLocales", loc)
			continue
		}
		if len(asset.plain) == 0 {
			t.Errorf("locale %s: empty asset", loc)
		}
	}
}
