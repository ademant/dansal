package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/ademant/dansal/internal/logging"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
	"github.com/gen2brain/avif"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

func saveDataOn(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Save-Data"), "on")
}

// imageExtFromConfig returns the configured output file extension and MIME type.
func imageExtFromConfig() (ext, contentType string) {
	if config.Server.ImageFormat == "jpeg" {
		return ".jpeg", "image/jpeg"
	}
	return ".avif", "image/avif"
}

// imagePathForID finds the image file for a given ID in dir, trying the
// configured format first then the other, for backwards-compatibility.
func imagePathForID(dir, idStr string) (path, contentType string, found bool) {
	ext, ct := imageExtFromConfig()
	p := filepath.Join(dir, idStr+ext)
	if _, err := os.Stat(p); err == nil {
		return p, ct, true
	}
	if ext == ".avif" {
		ext, ct = ".jpeg", "image/jpeg"
	} else {
		ext, ct = ".avif", "image/avif"
	}
	p = filepath.Join(dir, idStr+ext)
	if _, err := os.Stat(p); err == nil {
		return p, ct, true
	}
	return "", "", false
}

// imagePathForIDVariant looks for a grid-thumbnail variant ("{id}.sq" /
// "{id}.wide", #1158) and falls back to the canonical full-size image when
// the variant doesn't exist — e.g. images uploaded before this feature
// shipped. Callers keep object-fit:cover as a CSS safety net for that case.
// resolvedID is idStr+suffix when the variant was found, else plain idStr —
// callers needing the on-disk basename (e.g. for the ?format=jpeg sibling)
// should use it instead of idStr.
func imagePathForIDVariant(dir, idStr, suffix string) (path, contentType, resolvedID string, found bool) {
	if suffix != "" {
		if p, ct, ok := imagePathForID(dir, idStr+suffix); ok {
			return p, ct, idStr + suffix, true
		}
	}
	p, ct, ok := imagePathForID(dir, idStr)
	return p, ct, idStr, ok
}

// imgCache is an in-memory set of event IDs that have an image on disk.
// See imageIDCache (image_cache.go) for the shared implementation.
var imgCache = newImageIDCache()

// initImageCache populates the cache by scanning the images directory.
// Called once at startup; the cache is kept up-to-date via add/remove.
func initImageCache(dir string) {
	imgCache.init(dir)
}

func hasImage(id int) bool {
	return imgCache.has(id)
}

var errNotImage = errors.New("data is not an image")

// errUndecodableImage marks input whose bytes sniff as an image but that no
// registered decoder can turn into pixels: a format we don't support, or a
// file that is corrupt/truncated. It is a client mistake, so callers map it
// to 415 rather than letting it surface as a 500 (#1372).
var errUndecodableImage = errors.New("unsupported or corrupt image")

// maxImageMegapixels caps decoded pixel dimensions before the full pixel
// buffer is allocated, comfortably above the 1024×1024 default resize
// target (fitImage runs after decode) while still rejecting a crafted
// image that declares e.g. 50000x50000 px to force a multi-GB allocation
// (decompression-bomb DoS, #990). 50MP covers legitimate large phone
// photos (~8000x6000).
const maxImageMegapixels = 50

// decodeImageSafely sniffs data's content type, rejects declared pixel
// dimensions above maxImageMegapixels via the cheap header-only
// image.DecodeConfig (no pixel allocation) before running the full
// image.Decode. Applies uniformly to JPEG/PNG/GIF/AVIF uploads and iCal
// ATTACH images, since all funnel through saveImageToDir/saveAvatarToDir.
func decodeImageSafely(data []byte) (image.Image, error) {
	if !strings.HasPrefix(http.DetectContentType(data), "image/") && !isAVIF(data) {
		return nil, errNotImage
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		if mp := (int64(cfg.Width) * int64(cfg.Height)) / 1_000_000; mp > maxImageMegapixels {
			return nil, fmt.Errorf("image dimensions %dx%d exceed the %dMP limit", cfg.Width, cfg.Height, maxImageMegapixels)
		}
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errUndecodableImage, err)
	}
	return img, nil
}

// isAVIF reports whether head contains an ISO BMFF ftyp box with an AVIF brand.
// http.DetectContentType misidentifies AVIF as video/mp4 because they share the
// ISO Base Media File Format container; this check catches the case it misses.
// It scans the major brand (offset 8) and all compatible brands (offset 16+),
// skipping the minor version at offset 12, so encoders that place "avif"/"avis"
// only in the compatible brands list are accepted.
func isAVIF(head []byte) bool {
	if len(head) < 12 {
		return false
	}
	if string(head[4:8]) != "ftyp" {
		return false
	}
	end := min(len(head), 128)
	for i := 8; i+4 <= end; i += 4 {
		if i == 12 {
			continue // minor version, not a brand
		}
		switch string(head[i : i+4]) {
		case "avif", "avis":
			return true
		}
	}
	return false
}

// Grid-thumbnail target sizes (#1158). Square feeds the musician grid
// (musicians.html, aspect-ratio:1); wide feeds event cards (index page,
// embed_event.html, ~16:9-ish). Deliberately small — these back small grid
// tiles, not full-bleed views.
const (
	thumbSquareSize = 400
	thumbWideW      = 480
	thumbWideH      = 270
)

// saveImageToDir decodes image data from r, resizes, and stores as AVIF in
// the given directory. The file is named "{id}.avif". When genThumbs is
// true, it additionally generates and stores center-cropped grid-thumbnail
// variants ("{id}.sq.avif", "{id}.wide.avif") from the original (pre-fit)
// image — used by event and musician images (#1158), whose grid views
// otherwise had to crop the full-size image blindly via CSS
// object-fit:cover. No focal-point/manual-crop control (deliberately out
// of scope, see #1158). The caller is responsible for updating any cache.
func saveImageToDir(id int, dir string, r io.Reader, genThumbs bool) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	orig, err := decodeImageSafely(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	fitted := fitImage(orig, config.Server.ImageXMax, config.Server.ImageYMax)
	if err := encodeImageVariant(fitted, dir, id, ""); err != nil {
		return err
	}
	if genThumbs {
		sq := cropToAspect(orig, thumbSquareSize, thumbSquareSize)
		if err := encodeImageVariant(sq, dir, id, ".sq"); err != nil {
			return err
		}
		wide := cropToAspect(orig, thumbWideW, thumbWideH)
		if err := encodeImageVariant(wide, dir, id, ".wide"); err != nil {
			return err
		}
	}
	return nil
}

// encodeImageVariant writes img to dir as "{id}{suffix}{ext}" using the
// configured server image format. When that format is AVIF, it also keeps a
// JPEG sibling ("{id}{suffix}.jpeg") so ActivityPub consumers requesting
// ?format=jpeg (or an AVIF-incapable client for a thumbnail variant) get an
// honestly-declared image/jpeg; declaring image/jpeg while serving the AVIF
// file breaks Mastodon previews (#1054).
func encodeImageVariant(img image.Image, dir string, id int, suffix string) error {
	ext, _ := imageExtFromConfig()
	outPath := filepath.Join(dir, fmt.Sprintf("%d%s%s", id, suffix, ext))
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer f.Close()
	if ext == ".jpeg" {
		if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
			return fmt.Errorf("encode jpeg: %w", err)
		}
		return nil
	}
	if err := avif.Encode(f, img); err != nil {
		return fmt.Errorf("encode avif: %w", err)
	}
	jpegPath := filepath.Join(dir, fmt.Sprintf("%d%s.jpeg", id, suffix))
	jf, err := os.Create(jpegPath)
	if err != nil {
		return fmt.Errorf("create jpeg sibling: %w", err)
	}
	defer jf.Close()
	if err := jpeg.Encode(jf, img, &jpeg.Options{Quality: 85}); err != nil {
		return fmt.Errorf("encode jpeg sibling: %w", err)
	}
	return nil
}

// saveAvatarToDir decodes r, resizes to fit 400×400, and stores as JPEG.
// Always uses JPEG regardless of server image format config. File is named "{id}.jpg".
func saveAvatarToDir(id int, dir string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	img, err := decodeImageSafely(data)
	if err != nil {
		return err
	}
	img = fitImage(img, 400, 400)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	outPath := filepath.Join(dir, fmt.Sprintf("%d.jpg", id))
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		return fmt.Errorf("encode jpeg: %w", err)
	}
	return nil
}

// saveImageFromReader decodes image data from r, resizes, and stores as AVIF for the given event ID.
func saveImageFromReader(eventID int, r io.Reader) error {
	if err := saveImageToDir(eventID, config.Server.ImagesDir, r, true); err != nil {
		return err
	}
	imgCache.add(eventID)
	return nil
}

// attachImagesFromICalEvent downloads and stores the first image ATTACH from a vevent.
// Skips silently if an image already exists for the event.
// icalImageMaxBytes caps a feed image (#1578). A larger file is refused.
const icalImageMaxBytes = 10 << 20

// icalImageAttachTimeout bounds the image downloads of one event (#1708). The
// attach runs after the import commits, so a slow image host delays only that
// event's picture, not the import.
const icalImageAttachTimeout = 20 * time.Second

// attachImagesFromICalEvent stores the first usable image of a VEVENT. The
// common non-standard IMAGE property is tried first, then ATTACH (#1578). It
// skips when the event already has an image, and writes no debug line for a
// VEVENT without an image property. It runs after the import transaction
// commits (#1708), so it never holds the write lock.
func attachImagesFromICalEvent(ctx context.Context, eventID int, vevent *ics.VEvent) {
	imageProps := vevent.GetProperties(ics.ComponentProperty("IMAGE"))
	var attachProps []*ics.IANAProperty
	for _, prop := range vevent.GetProperties(ics.ComponentPropertyAttach) {
		if fmttype := prop.ICalParameters["FMTTYPE"]; len(fmttype) > 0 && strings.HasPrefix(fmttype[0], "image/") {
			attachProps = append(attachProps, prop)
		}
	}
	if len(imageProps) == 0 && len(attachProps) == 0 {
		return
	}
	if hasImage(eventID) {
		logging.Debugf("image attach: event=%d outcome=exists", eventID)
		return
	}

	ctx, cancel := context.WithTimeout(ctx, icalImageAttachTimeout)
	defer cancel()
	start := time.Now()
	attached := false
	for _, prop := range imageProps {
		if len(prop.Value) > 0 && tryAttachImageURL(ctx, eventID, prop.Value) {
			attached = true
			break
		}
	}
	for _, prop := range attachProps {
		if attached {
			break
		}
		attached = tryAttachImage(ctx, eventID, prop)
	}
	outcome := "failed"
	switch {
	case attached:
		outcome = "attached"
	case ctx.Err() != nil:
		outcome = "timeout"
	}
	logging.Debugf("image attach: event=%d outcome=%s duration_ms=%d", eventID, outcome, time.Since(start).Milliseconds())
}

// imageRequest sends a HEAD or GET for a feed image under ctx, so the
// per-event timeout also stops a slow image host (#1708).
func imageRequest(ctx context.Context, method, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	return safeClient.Do(req)
}

func tryAttachImage(ctx context.Context, eventID int, prop *ics.IANAProperty) bool {
	valueType := ""
	if vt := prop.ICalParameters["VALUE"]; len(vt) > 0 {
		valueType = strings.ToUpper(vt[0])
	}

	if valueType == "BINARY" {
		enc := ""
		if e := prop.ICalParameters["ENCODING"]; len(e) > 0 {
			enc = strings.ToUpper(e[0])
		}
		if enc != "BASE64" {
			return false
		}
		data, err := base64.StdEncoding.DecodeString(prop.Value)
		if err != nil {
			logging.Errorf("iCal ATTACH base64 decode for event %d: %v", eventID, err)
			return false
		}
		if err := saveImageFromReader(eventID, bytes.NewReader(data)); err != nil {
			logging.Errorf("iCal ATTACH save for event %d: %v", eventID, err)
			return false
		}
		return true
	}

	return tryAttachImageURL(ctx, eventID, prop.Value)
}

// tryAttachImageURL fetches a feed image by URL and stores it. It uses
// safeClient (no private addresses), checks the type and size with a HEAD
// first where the server answers it, and caps the download at
// icalImageMaxBytes (#1578). A refusal is logged and reports false.
func tryAttachImageURL(ctx context.Context, eventID int, rawURL string) bool {
	if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	if head, err := imageRequest(ctx, http.MethodHead, rawURL); err == nil {
		head.Body.Close()
		if head.StatusCode >= 200 && head.StatusCode < 300 {
			if ct := head.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "image/") {
				logging.Warnf("iCal image for event %d refused: content type %q", eventID, ct)
				return false
			}
			if head.ContentLength > icalImageMaxBytes {
				logging.Warnf("iCal image for event %d refused: %d bytes is over the limit", eventID, head.ContentLength)
				return false
			}
		}
	}
	resp, err := imageRequest(ctx, http.MethodGet, rawURL)
	if err != nil {
		logging.Errorf("iCal image fetch for event %d: %v", eventID, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logging.Warnf("iCal image HTTP %d for event %d", resp.StatusCode, eventID)
		return false
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "image/") {
		logging.Warnf("iCal image for event %d refused: content type %q", eventID, ct)
		return false
	}
	if err := saveImageFromReader(eventID, io.LimitReader(resp.Body, icalImageMaxBytes)); err != nil {
		logging.Errorf("iCal image save for event %d: %v", eventID, err)
		return false
	}
	return true
}

// GET /api/v1/images/{event_id}
func maybeServePrecompressed(w http.ResponseWriter, r *http.Request, path, contentType string) bool {
	// Set Vary so caches know content varies by Accept-Encoding
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Cache-Control", "public, max-age=86400")

	// If client prefers brotli and a .br file exists, serve it
	if strings.Contains(r.Header.Get("Accept-Encoding"), "br") {
		if _, err := os.Stat(path + ".br"); err == nil {
			w.Header().Set("Content-Encoding", "br")
			w.Header().Set("Content-Type", contentType)
			http.ServeFile(w, r, path+".br")
			return true
		}
	}
	// If client accepts gzip and a .gz file exists, serve it
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		if _, err := os.Stat(path + ".gz"); err == nil {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", contentType)
			http.ServeFile(w, r, path+".gz")
			return true
		}
	}
	return false
}

func getEventImage(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("event_id")
	if !validNumericPathID(w, eventID, "event") {
		return
	}

	// Grid-thumbnail variant (#1158): ?thumb=sq for the musician-grid-style
	// square crop, ?thumb=wide for the event-card landscape crop. Falls back
	// to the canonical full-size image (and the caller's CSS
	// object-fit:cover) when no variant exists yet.
	var thumbSuffix string
	switch r.URL.Query().Get("thumb") {
	case "sq":
		thumbSuffix = ".sq"
	case "wide":
		thumbSuffix = ".wide"
	}
	imgPath, contentType, resolvedID, found := imagePathForIDVariant(config.Server.ImagesDir, eventID, thumbSuffix)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}

	// JPEG variant for ActivityPub consumers (#1054): many Fediverse clients
	// cannot render AVIF, and the canonical URL's real Content-Type is
	// image/avif, so note attachments point here (?format=jpeg) and declare
	// image/jpeg honestly. New uploads get a JPEG sibling at save time;
	// legacy files are converted on first request and cached on disk.
	if r.URL.Query().Get("format") == "jpeg" {
		serveJpegVariant(w, r, imgPath, resolvedID)
		return
	}

	// Respect Save-Data by preferring a "small" variant if available
	if saveDataOn(r) {
		ext := filepath.Ext(imgPath)
		smallPath := strings.TrimSuffix(imgPath, ext) + ".small" + ext
		if _, err := os.Stat(smallPath); err == nil {
			if maybeServePrecompressed(w, r, smallPath, contentType) {
				return
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			http.ServeFile(w, r, smallPath)
			return
		}
	}

	// Try serving precompressed variants of the canonical file
	if maybeServePrecompressed(w, r, imgPath, contentType) {
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, imgPath)
}

// serveJpegVariant serves the JPEG form of an event image with Content-Type
// image/jpeg. It prefers the sibling generated at upload time; for files that
// predate the variant it decodes the stored image and writes the JPEG once,
// so the conversion cost is paid only on the first request (#1054).
func serveJpegVariant(w http.ResponseWriter, r *http.Request, imgPath, idStr string) {
	jpegPath := imgPath
	if filepath.Ext(imgPath) != ".jpeg" {
		jpegPath = filepath.Join(filepath.Dir(imgPath), idStr+".jpeg")
		if _, err := os.Stat(jpegPath); err != nil {
			if err := generateJpegSibling(imgPath, jpegPath); err != nil {
				logging.Errorf("image %s: jpeg variant: %v", idStr, err)
				writeError(w, "Image conversion failed", http.StatusInternalServerError)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, jpegPath)
}

// generateJpegSibling decodes the stored image (AVIF or JPEG) and writes it as
// a JPEG file so future ?format=jpeg requests are a plain file read.
func generateJpegSibling(srcPath, jpegPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	img, err := decodeImageSafely(data)
	if err != nil {
		return err
	}
	f, err := os.Create(jpegPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		return err
	}
	return f.Close()
}

// fitImage scales img down to fit within maxW x maxH, preserving aspect ratio.
// Returns the original if it already fits.
func fitImage(img image.Image, maxW, maxH int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW && h <= maxH {
		return img
	}
	// Scale factor that fits both dimensions
	scaleW := float64(maxW) / float64(w)
	scaleH := float64(maxH) / float64(h)
	scale := scaleW
	if scaleH < scaleW {
		scale = scaleH
	}
	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// cropToAspect center-crops img to the targetW:targetH aspect ratio, then
// scales the crop to exactly targetW x targetH. Used to generate the
// square/landscape grid thumbnails (#1158) — deliberately a plain center
// crop with no focal-point/manual-crop control, matching the scope agreed
// for that issue.
func cropToAspect(img image.Image, targetW, targetH int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	targetRatio := float64(targetW) / float64(targetH)
	srcRatio := float64(w) / float64(h)
	var cropW, cropH int
	if srcRatio > targetRatio {
		// Source is relatively wider than the target: crop the sides.
		cropH = h
		cropW = int(float64(h) * targetRatio)
	} else {
		// Source is relatively taller (or equal): crop top/bottom.
		cropW = w
		cropH = int(float64(w) / targetRatio)
	}
	x0 := b.Min.X + (w-cropW)/2
	y0 := b.Min.Y + (h-cropH)/2
	cropRect := image.Rect(x0, y0, x0+cropW, y0+cropH)
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), img, cropRect, draw.Over, nil)
	return dst
}

// DELETE /api/v1/images/{event_id}
func deleteEventImage(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if !requireRole(w, userRole, RoleAdmin, RoleUser) {
		return
	}

	eventID := r.PathValue("event_id")
	if !validNumericPathID(w, eventID, "event") {
		return
	}

	if userRole != RoleAdmin {
		orgID, err := eventOrgID(db, eventID)
		if !rowOr404(w, err, "Event not found") {
			return
		}
		if !requireExistingOrgMember(w, callerID, orgID) {
			return
		}
	}

	imgPath, _, found := imagePathForID(config.Server.ImagesDir, eventID)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	if err := os.Remove(imgPath); err != nil {
		writeInternalError(w, err)
		return
	}
	id, _ := strconv.Atoi(eventID)
	imgCache.remove(id)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/images/{event_id}
var uploadEventImage = imageUploadHandler(imageUploadSpec{
	pathParam: "event_id",
	idLabel:   "event ID",
	roles:     []string{RoleAdmin, RoleUser, RolePublisher},
	checkAccess: func(w http.ResponseWriter, r *http.Request, callerID int, userRole string, id int) bool {
		orgID, err := eventOrgID(db, id)
		if err == sql.ErrNoRows {
			writeError(w, "Event not found", http.StatusNotFound)
			return false
		} else if err != nil {
			writeInternalError(w, err)
			return false
		}
		if userRole != RoleAdmin {
			return requireExistingOrgMember(w, callerID, orgID)
		}
		return true
	},
	save:     func(id int, r io.Reader) error { return saveImageToDir(id, config.Server.ImagesDir, r, true) },
	cacheAdd: imgCache.add,
	respond: func(w http.ResponseWriter, id int) {
		ext, _ := imageExtFromConfig()
		outPath := filepath.Join(config.Server.ImagesDir, fmt.Sprintf("%d%s", id, ext))
		writeJSONStatus(w, http.StatusCreated, map[string]string{"path": outPath})
	},
})
