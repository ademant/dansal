package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// #1457: some crawlers resolve the geo: navigation link
// (event.html/location.html) as if it were a relative URL, requesting
// /events/geo:lat,lon or /location/geo:lat,lon instead of a numeric ID.
// These answer 410 so retry queues drain faster than a plain 404 would.

func TestEventHandlerGoneForGeoLinkPath(t *testing.T) {
	cfg := &Config{Domain: "example.test"}
	handler := eventHandler(cfg, loadTemplates(), nil, loadI18n(""))

	req := httptest.NewRequest(http.MethodGet, "/events/geo:51.2,7.1", nil)
	req.SetPathValue("id", "geo:51.2,7.1")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestLocationPageHandlerGoneForGeoLinkPath(t *testing.T) {
	cfg := &Config{Domain: "example.test"}
	handler := locationPageHandler(cfg, loadTemplates(), nil, loadI18n(""))

	req := httptest.NewRequest(http.MethodGet, "/location/geo:51.2,7.1", nil)
	req.SetPathValue("id", "geo:51.2,7.1")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410 (body=%s)", rec.Code, rec.Body.String())
	}
}

// A non-numeric, non-geo: id must still 404 as before — the new prefix
// check mustn't swallow other bad-id cases.
func TestEventHandlerStillNotFoundForOtherBadID(t *testing.T) {
	cfg := &Config{Domain: "example.test"}
	handler := eventHandler(cfg, loadTemplates(), nil, loadI18n(""))

	req := httptest.NewRequest(http.MethodGet, "/events/not-a-number", nil)
	req.SetPathValue("id", "not-a-number")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}
