package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/ademant/dansal/internal/logging"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Generated per-event fallback banner (#1082, #1083): when an event has no
// own image, Search Console flags it as missing a structured-data image
// even though the JSON-LD/og:image fallback chain (event → series → org →
// website banner) already covers it, because a plain series/org banner is
// identical across every event that reuses it and carries no per-event
// signal. This overlays the event's date/time (series tier) or title/
// date-time/location (org tier) onto a resized copy of the series/org
// banner, cached to disk like every other image type here.
//
// Deliberately lives in dansal, not dansal_web: images are already owned
// end-to-end here (ImagesDir, getEventImage, series_images.go/org_images.go)
// and dansal_web only ever proxies relative "/api/v1/*-images/..." URLs — in
// production that proxying happens at the nginx layer (location /api/ in
// deploy/nginx/dansal.conf), not in dansal_web's own router. Following the
// same pattern means dansal_web needs no new Go code at all, just a template
// change to point at this URL.

// eventBannerWidth is deliberately much smaller than the 1024×1024 upload
// target (images.go) — this is a low-quality preview/fallback image for
// crawlers and link previews, not something a visitor examines closely.
const eventBannerWidth = 768

const eventBannerJPEGQuality = 75

const (
	bannerBoxPadding = 16.0
	bannerLineHeight = 34.0
	bannerFontSize   = 24.0
	bannerTitleSize  = 28.0
	bannerMaxTitle   = 70 // runes, before an ellipsis is appended
)

var (
	bannerFontRegular *opentype.Font
	bannerFontBold    *opentype.Font
)

func init() {
	var err error
	bannerFontRegular, err = opentype.Parse(goregular.TTF)
	if err != nil {
		log.Fatalf("event-banner: parse embedded regular font: %v", err)
	}
	bannerFontBold, err = opentype.Parse(gobold.TTF)
	if err != nil {
		log.Fatalf("event-banner: parse embedded bold font: %v", err)
	}
}

// eventBannerTier reports which generated-banner fallback tier applies to
// event, or "" when the event has its own image. An event with no own image
// gets the series or org banner when one exists, and otherwise the "plain"
// tier (#1897): a neutral canvas carrying the event's title, date and venue,
// so no two events share one fallback image. Shared between the serving
// handler and the prune-images validity check so the two can never drift.
func eventBannerTier(event Event) string {
	if hasImage(event.ID) {
		return ""
	}
	if event.SeriesID != nil && hasSeriesImage(*event.SeriesID) {
		return "series"
	}
	if event.OrganizationID != nil && hasOrgImage(*event.OrganizationID) {
		return "org"
	}
	return "plain"
}

// plainBannerBackground is the site's theme colour (base.html theme-color),
// the canvas behind a plain-tier banner.
var plainBannerBackground = color.RGBA{0x1a, 0x6e, 0xb5, 0xff}

// plainBannerWidth/Height is the plain-tier canvas: close to the 1.91:1 social
// card ratio, so link previews crop it little.
const (
	plainBannerWidth  = eventBannerWidth
	plainBannerHeight = 402
)

// plainBannerHash fingerprints the text drawn on a plain-tier banner. It is
// part of the cache file name, so a changed title, date or venue writes a new
// file and the old one is pruned as stale (#1897).
func plainBannerHash(event Event) string {
	venue := ""
	if event.Location != nil {
		venue = event.Location.Location
	}
	sum := sha256.Sum256([]byte(event.Title + "\x00" + eventBannerDateTime(event) + "\x00" + venue))
	return hex.EncodeToString(sum[:6])
}

// plainBannerCachePath is the cache file for event's plain-tier banner.
func plainBannerCachePath(event Event) string {
	return filepath.Join(config.Server.ImagesDir, fmt.Sprintf("banner-plain-%d-%s.jpg", event.ID, plainBannerHash(event)))
}

// eventBannerCachePathFor returns the cache file for event under tier.
func eventBannerCachePathFor(event Event, tier string) string {
	if tier == "plain" {
		return plainBannerCachePath(event)
	}
	return eventBannerCachePath(event.ID, tier)
}

// plainBannerFileValid reports whether a banner-plain-{id}-{hash}.jpg file
// still matches the event's current text (#1897).
func plainBannerFileValid(id int, hash string) bool {
	event, err := getEventByID(db, id)
	if err != nil {
		return false
	}
	return eventBannerTier(event) == "plain" && plainBannerHash(event) == hash
}

// eventBannerCachePath returns the on-disk path for event's generated banner
// under the given tier. Naming ("banner-" / "banner-org-" prefix) is also
// relied on by adminPruneImages to recognize and validate these files.
func eventBannerCachePath(eventID int, tier string) string {
	switch tier {
	case "series":
		return filepath.Join(config.Server.ImagesDir, fmt.Sprintf("banner-%d.jpg", eventID))
	case "org":
		return filepath.Join(config.Server.ImagesDir, fmt.Sprintf("banner-org-%d.jpg", eventID))
	default:
		return ""
	}
}

// eventBannerFileStillValid is used by adminPruneImages to decide whether an
// existing banner-*.jpg / banner-org-*.jpg file still corresponds to the
// event's current fallback tier (event reassigned to another series,
// removed from its series, series/org banner deleted, etc.) — see the
// dansal-prune-images timer.
func eventBannerFileStillValid(id int, tier string) bool {
	event, err := getEventByID(db, id)
	if err != nil {
		return false
	}
	return eventBannerTier(event) == tier
}

// GET /api/v1/event-banner/{event_id}
func getEventBannerImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("event_id")
	if !validNumericPathID(w, idStr, "event") {
		return
	}
	id, _ := strconv.Atoi(idStr)
	event, err := getEventByID(db, id)
	if err != nil {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	}
	tier := eventBannerTier(event)
	if tier == "" {
		writeError(w, "No banner available", http.StatusNotFound)
		return
	}
	cachePath := eventBannerCachePathFor(event, tier)
	if _, err := os.Stat(cachePath); err != nil {
		if err := generateEventBannerImage(event, tier, cachePath); err != nil {
			logging.Errorf("event-banner %d: generate: %v", id, err)
			writeError(w, "Banner generation failed", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, cachePath)
}

// generateEventBannerImage renders the series- or org-banner-derived
// fallback image for event and writes it to cachePath as a JPEG.
func generateEventBannerImage(event Event, tier, cachePath string) error {
	var canvas *image.RGBA
	switch tier {
	case "series", "org":
		var srcDir string
		var srcID int
		if tier == "series" {
			srcDir, srcID = seriesImagesDir, *event.SeriesID
		} else {
			srcDir, srcID = orgImagesDir, *event.OrganizationID
		}
		srcPath, _, found := imagePathForID(srcDir, strconv.Itoa(srcID))
		if !found {
			return fmt.Errorf("source image for tier %s id %d not found", tier, srcID)
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		img, err := decodeImageSafely(data)
		if err != nil {
			return err
		}
		canvas = prepareBannerCanvas(img, eventBannerWidth)
	case "plain":
		canvas = image.NewRGBA(image.Rect(0, 0, plainBannerWidth, plainBannerHeight))
		draw.Draw(canvas, canvas.Bounds(), image.NewUniform(plainBannerBackground), image.Point{}, draw.Src)
	default:
		return fmt.Errorf("unknown banner tier %q", tier)
	}

	dt := eventBannerDateTime(event)
	var lines []string
	boldFirst := false
	if tier == "series" {
		if dt != "" {
			lines = []string{dt}
		}
	} else {
		boldFirst = true
		lines = append(lines, truncateRunes(event.Title, bannerMaxTitle))
		if dt != "" {
			lines = append(lines, dt)
		}
		if event.Location != nil && event.Location.Location != "" {
			lines = append(lines, event.Location.Location)
		}
	}
	if err := drawBannerOverlay(canvas, lines, boldFirst); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(cachePath)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, canvas, &jpeg.Options{Quality: eventBannerJPEGQuality})
}

// prepareBannerCanvas returns img resized to fit targetW wide (aspect ratio
// preserved, never upscaled) as a drawable *image.RGBA.
func prepareBannerCanvas(img image.Image, targetW int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= targetW {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
		return dst
	}
	scale := float64(targetW) / float64(w)
	newH := int(float64(h) * scale)
	dst := image.NewRGBA(image.Rect(0, 0, targetW, newH))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// eventBannerDateTime formats event's start time in a fixed, locale-neutral
// format. It deliberately does not follow the viewer's UI language (unlike
// the page itself) — the generated image is one static file shared across
// all 12 UI languages, and per-language variants would multiply the cache
// for no real benefit to crawlers/link-preview consumers.
func eventBannerDateTime(event Event) string {
	t, err := time.Parse(time.RFC3339, event.StartTime)
	if err != nil {
		return ""
	}
	return t.In(instanceTimezone).Format("2 Jan 2006 · 15:04")
}

// truncateRunes shortens s to at most max runes, appending an ellipsis if it
// was cut, so an unusually long event title can't blow out the banner layout.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// drawBannerOverlay paints a semi-opaque band across the bottom of canvas
// and centers each of lines within it, first line in bold when boldFirst
// (the org tier's title line).
func drawBannerOverlay(canvas *image.RGBA, lines []string, boldFirst bool) error {
	if len(lines) == 0 {
		return nil
	}
	regularFace, err := bannerFace(bannerFontRegular, bannerFontSize)
	if err != nil {
		return err
	}
	boldFace, err := bannerFace(bannerFontBold, bannerTitleSize)
	if err != nil {
		return err
	}

	boxH := int(bannerBoxPadding*2 + bannerLineHeight*float64(len(lines)))
	b := canvas.Bounds()
	boxTop := max(b.Max.Y-boxH, b.Min.Y)
	box := image.Rect(b.Min.X, boxTop, b.Max.X, b.Max.Y)
	draw.Draw(canvas, box, image.NewUniform(color.RGBA{0, 0, 0, 165}), image.Point{}, draw.Over)

	for i, line := range lines {
		face := regularFace
		if i == 0 && boldFirst {
			face = boldFace
		}
		baseline := boxTop + int(bannerBoxPadding+bannerLineHeight*float64(i+1)-8)
		drawCenteredLine(canvas, face, line, baseline, color.White)
	}
	return nil
}

// bannerFace builds a font.Face at size points/72dpi from an already-parsed
// embedded font.
func bannerFace(f *opentype.Font, size float64) (font.Face, error) {
	return opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

// drawCenteredLine draws text horizontally centered on dst with its
// baseline at y.
func drawCenteredLine(dst *image.RGBA, face font.Face, text string, y int, col color.Color) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(col), Face: face}
	w := d.MeasureString(text).Ceil()
	x := max((dst.Bounds().Dx()-w)/2, 8)
	d.Dot = fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)}
	d.DrawString(text)
}
