package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	ics "github.com/arran4/golang-ical"
	"github.com/gen2brain/avif"
	xdraw "golang.org/x/image/draw"
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
		return nil, fmt.Errorf("decode: %w", err)
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
	end := len(head)
	if end > 128 {
		end = 128
	}
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

// saveImageToDir decodes image data from r, resizes, and stores as AVIF in the given directory.
// The file is named "{id}.avif". The caller is responsible for updating any cache.
func saveImageToDir(id int, dir string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	img, err := decodeImageSafely(data)
	if err != nil {
		return err
	}
	img = fitImage(img, config.Server.ImageXMax, config.Server.ImageYMax)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	ext, _ := imageExtFromConfig()
	outPath := filepath.Join(dir, fmt.Sprintf("%d%s", id, ext))
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer f.Close()
	if ext == ".jpeg" {
		if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
			return fmt.Errorf("encode jpeg: %w", err)
		}
	} else {
		if err := avif.Encode(f, img); err != nil {
			return fmt.Errorf("encode avif: %w", err)
		}
		// Keep a JPEG sibling alongside the AVIF so ActivityPub consumers can
		// request ?format=jpeg and receive an honestly-declared image/jpeg;
		// declaring image/jpeg while serving the AVIF file breaks Mastodon
		// previews (#1054).
		jpegPath := filepath.Join(dir, fmt.Sprintf("%d.jpeg", id))
		jf, err := os.Create(jpegPath)
		if err != nil {
			return fmt.Errorf("create jpeg sibling: %w", err)
		}
		defer jf.Close()
		if err := jpeg.Encode(jf, img, &jpeg.Options{Quality: 85}); err != nil {
			return fmt.Errorf("encode jpeg sibling: %w", err)
		}
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
	if err := saveImageToDir(eventID, config.Server.ImagesDir, r); err != nil {
		return err
	}
	imgCache.add(eventID)
	return nil
}

// attachImagesFromICalEvent downloads and stores the first image ATTACH from a vevent.
// Skips silently if an image already exists for the event.
func attachImagesFromICalEvent(eventID int, vevent *ics.VEvent) {
	if hasImage(eventID) {
		return
	}
	for _, prop := range vevent.GetProperties(ics.ComponentPropertyAttach) {
		fmttype := prop.ICalParameters["FMTTYPE"]
		if len(fmttype) == 0 || !strings.HasPrefix(fmttype[0], "image/") {
			continue
		}
		if tryAttachImage(eventID, prop) {
			return
		}
	}
}

func tryAttachImage(eventID int, prop *ics.IANAProperty) bool {
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
			log.Printf("iCal ATTACH base64 decode for event %d: %v", eventID, err)
			return false
		}
		if err := saveImageFromReader(eventID, bytes.NewReader(data)); err != nil {
			log.Printf("iCal ATTACH save for event %d: %v", eventID, err)
			return false
		}
		return true
	}

	// URI attachment — use safeClient to block SSRF to private/internal addresses.
	if u, err2 := url.Parse(prop.Value); err2 != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	resp, err := safeClient.Get(prop.Value)
	if err != nil {
		log.Printf("iCal ATTACH fetch for event %d: %v", eventID, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("iCal ATTACH HTTP %d for event %d", resp.StatusCode, eventID)
		return false
	}
	if err := saveImageFromReader(eventID, resp.Body); err != nil {
		log.Printf("iCal ATTACH save for event %d: %v", eventID, err)
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

	// Validate event_id is a plain integer to prevent path traversal
	for _, c := range eventID {
		if c < '0' || c > '9' {
			writeError(w, "Invalid event ID", http.StatusBadRequest)
			return
		}
	}

	imgPath, contentType, found := imagePathForID(config.Server.ImagesDir, eventID)
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
		serveJpegVariant(w, r, imgPath, eventID)
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
				log.Printf("image %s: jpeg variant: %v", idStr, err)
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

// DELETE /api/v1/images/{event_id}
func deleteEventImage(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	eventID := r.PathValue("event_id")
	for _, c := range eventID {
		if c < '0' || c > '9' {
			writeError(w, "Invalid event ID", http.StatusBadRequest)
			return
		}
	}

	if userRole != RoleAdmin {
		var orgID sql.NullInt64
		if err := db.QueryRow("SELECT organization_id FROM events WHERE id = ?", eventID).Scan(&orgID); err == sql.ErrNoRows {
			writeError(w, "Event not found", http.StatusNotFound)
			return
		} else if err != nil {
			writeInternalError(w, err)
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
		var orgID sql.NullInt64
		err := db.QueryRow("SELECT organization_id FROM events WHERE id = ?", id).Scan(&orgID)
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
	save:     func(id int, r io.Reader) error { return saveImageToDir(id, config.Server.ImagesDir, r) },
	cacheAdd: imgCache.add,
	respond: func(w http.ResponseWriter, id int) {
		ext, _ := imageExtFromConfig()
		outPath := filepath.Join(config.Server.ImagesDir, fmt.Sprintf("%d%s", id, ext))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"path": outPath})
	},
})
