package main

import (
	"image"
	"image/color"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeTestBannerSource writes a small solid-color JPEG at dir/{id}.jpeg,
// standing in for an uploaded series/org banner.
func writeTestBannerSource(t *testing.T, dir string, id int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1200, 600))
	for y := range 600 {
		for x := range 1200 {
			img.Set(x, y, color.RGBA{100, 120, 200, 255})
		}
	}
	f, err := os.Create(filepath.Join(dir, strconv.Itoa(id)+".jpeg"))
	if err != nil {
		t.Fatalf("create source jpeg: %v", err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode source jpeg: %v", err)
	}
}

// setupEventBannerTestDirs points config.Server.ImagesDir, seriesImagesDir
// and orgImagesDir at fresh temp dirs and restores the previous values on
// cleanup, mirroring the real layout (main.go: ImagesDir, ImagesDir/series,
// ImagesDir/orgs).
func setupEventBannerTestDirs(t *testing.T) {
	t.Helper()
	oldConfig, oldSeriesDir, oldOrgDir := config, seriesImagesDir, orgImagesDir
	oldSeriesCache, oldOrgCache := seriesImgCache, orgImgCache
	t.Cleanup(func() {
		config, seriesImagesDir, orgImagesDir = oldConfig, oldSeriesDir, oldOrgDir
		seriesImgCache, orgImgCache = oldSeriesCache, oldOrgCache
	})
	base := t.TempDir()
	config = &Config{}
	config.Server.ImagesDir = base
	config.Server.ImageFormat = "jpeg"
	seriesImagesDir = filepath.Join(base, "series")
	orgImagesDir = filepath.Join(base, "orgs")
	seriesImgCache = &seriesImageCache{mimeType: make(map[int]string)}
	orgImgCache = &orgImageCache{mimeType: make(map[int]string)}
}

// TestEventBannerTierSelection asserts the fallback priority (own image >
// series banner > org banner > none) matches what both the serving handler
// and adminPruneImages rely on (#1082, #1083).
func TestEventBannerTierSelection(t *testing.T) {
	setupDedupTestDB(t)
	setupEventBannerTestDirs(t)

	seriesID := int64(1)
	if _, err := db.Exec("INSERT INTO event_series (id, slug, title) VALUES (?, 'test-series', 'Test Series')", seriesID); err != nil {
		t.Fatalf("insert series: %v", err)
	}
	orgID := int64(1)
	if _, err := db.Exec("INSERT INTO organizations (id, name) VALUES (?, 'Test Org')", orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	oid := int(orgID)
	sid := int(seriesID)

	eventID, _, _, err := insertEvent(db, EventInput{Title: "No fallback", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if tier := eventBannerTier(mustFetchEvent(t, eventID)); tier != "" {
		t.Fatalf("tier = %q, want \"\" (no series/org banner)", tier)
	}

	// Org banner present → org tier.
	if _, err := db.Exec("UPDATE events SET organization_id=? WHERE id=?", oid, eventID); err != nil {
		t.Fatalf("assign org: %v", err)
	}
	writeTestBannerSource(t, orgImagesDir, oid)
	orgImgCache.add(oid, "image/jpeg")
	if tier := eventBannerTier(mustFetchEvent(t, eventID)); tier != "org" {
		t.Fatalf("tier = %q, want \"org\"", tier)
	}

	// Series banner present too → series tier wins.
	if _, err := db.Exec("UPDATE events SET series_id=? WHERE id=?", sid, eventID); err != nil {
		t.Fatalf("assign series: %v", err)
	}
	writeTestBannerSource(t, seriesImagesDir, sid)
	seriesImgCache.add(sid, "image/jpeg")
	if tier := eventBannerTier(mustFetchEvent(t, eventID)); tier != "series" {
		t.Fatalf("tier = %q, want \"series\"", tier)
	}

	// Own image present → no generated fallback at all.
	imgCache.add(eventID)
	t.Cleanup(func() { imgCache.remove(eventID) })
	if tier := eventBannerTier(mustFetchEvent(t, eventID)); tier != "" {
		t.Fatalf("tier = %q, want \"\" (event has its own image)", tier)
	}
}

func mustFetchEvent(t *testing.T, id int) Event {
	t.Helper()
	event, err := fetchEventByID(db, id)
	if err != nil {
		t.Fatalf("fetchEventByID(%d): %v", id, err)
	}
	return event
}

// TestGetEventBannerImageSeriesTier exercises the full HTTP handler for the
// series-banner tier: 404 before any banner is available, 200 image/jpeg at
// the configured width once the event is assigned to a series with a
// banner, and a cache file written to disk so a second request is served
// without regenerating.
func TestGetEventBannerImageSeriesTier(t *testing.T) {
	setupDedupTestDB(t)
	setupEventBannerTestDirs(t)

	seriesID := int64(1)
	if _, err := db.Exec("INSERT INTO event_series (id, slug, title) VALUES (?, 'test-series', 'Test Series')", seriesID); err != nil {
		t.Fatalf("insert series: %v", err)
	}
	sid := int(seriesID)

	eventID, _, _, err := insertEvent(db, EventInput{Title: "Test Event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/event-banner/"+strconv.Itoa(eventID), nil)
	req.SetPathValue("event_id", strconv.Itoa(eventID))
	w := httptest.NewRecorder()
	getEventBannerImage(w, req)
	if w.Code != 404 {
		t.Fatalf("before series assignment: status = %d, want 404 (body=%s)", w.Code, w.Body.String())
	}

	if _, err := db.Exec("UPDATE events SET series_id=? WHERE id=?", sid, eventID); err != nil {
		t.Fatalf("assign series: %v", err)
	}
	writeTestBannerSource(t, seriesImagesDir, sid)
	seriesImgCache.add(sid, "image/jpeg")

	req = httptest.NewRequest("GET", "/api/v1/event-banner/"+strconv.Itoa(eventID), nil)
	req.SetPathValue("event_id", strconv.Itoa(eventID))
	w = httptest.NewRecorder()
	getEventBannerImage(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("Content-Type = %q, want image/jpeg", ct)
	}
	img, err := jpeg.Decode(w.Body)
	if err != nil {
		t.Fatalf("decode generated banner: %v", err)
	}
	if got := img.Bounds().Dx(); got != eventBannerWidth {
		t.Fatalf("width = %d, want %d", got, eventBannerWidth)
	}

	cachePath := eventBannerCachePath(eventID, "series")
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("expected cache file at %s: %v", cachePath, err)
	}

	// Second request must be served from the cached file (same bytes).
	req = httptest.NewRequest("GET", "/api/v1/event-banner/"+strconv.Itoa(eventID), nil)
	req.SetPathValue("event_id", strconv.Itoa(eventID))
	w2 := httptest.NewRecorder()
	getEventBannerImage(w2, req)
	if w2.Code != 200 {
		t.Fatalf("cached request: status = %d, want 200", w2.Code)
	}
}

// TestEventBannerFileStillValid backs the dansal-prune-images extension
// (#1082, #1083): a banner file must be considered stale once the event no
// longer qualifies for the tier its filename claims.
func TestEventBannerFileStillValid(t *testing.T) {
	setupDedupTestDB(t)
	setupEventBannerTestDirs(t)

	seriesID := int64(1)
	if _, err := db.Exec("INSERT INTO event_series (id, slug, title) VALUES (?, 'test-series', 'Test Series')", seriesID); err != nil {
		t.Fatalf("insert series: %v", err)
	}
	sid := int(seriesID)

	eventID, _, _, err := insertEvent(db, EventInput{Title: "Test Event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := db.Exec("UPDATE events SET series_id=? WHERE id=?", sid, eventID); err != nil {
		t.Fatalf("assign series: %v", err)
	}
	writeTestBannerSource(t, seriesImagesDir, sid)
	seriesImgCache.add(sid, "image/jpeg")

	if !eventBannerFileStillValid(eventID, "series") {
		t.Fatalf("expected series banner to still be valid while assigned")
	}

	// Removed from series → the "series" tier file is now stale.
	if _, err := db.Exec("UPDATE events SET series_id=NULL WHERE id=?", eventID); err != nil {
		t.Fatalf("unassign series: %v", err)
	}
	if eventBannerFileStillValid(eventID, "series") {
		t.Fatalf("expected series banner to be stale after removal from series")
	}

	// A non-existent event is never valid.
	if eventBannerFileStillValid(999999, "series") {
		t.Fatalf("expected non-existent event to be invalid")
	}
}
