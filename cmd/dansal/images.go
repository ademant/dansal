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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

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

// imageCache is an in-memory set of event IDs that have an image on disk.
// It avoids an os.Stat syscall per event row when building list responses.
type imageCache struct {
	mu  sync.RWMutex
	ids map[int]struct{}
}

var imgCache = &imageCache{ids: make(map[int]struct{})}

// initImageCache populates the cache by scanning the images directory.
// Called once at startup; the cache is kept up-to-date via add/remove.
func initImageCache(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // directory may not exist yet
	}
	imgCache.mu.Lock()
	defer imgCache.mu.Unlock()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		var base string
		if strings.HasSuffix(name, ".avif") {
			base = strings.TrimSuffix(name, ".avif")
		} else if strings.HasSuffix(name, ".jpeg") {
			base = strings.TrimSuffix(name, ".jpeg")
		} else {
			continue
		}
		if id, err := strconv.Atoi(base); err == nil {
			imgCache.ids[id] = struct{}{}
		}
	}
}

func hasImage(id int) bool {
	imgCache.mu.RLock()
	_, ok := imgCache.ids[id]
	imgCache.mu.RUnlock()
	return ok
}

func (c *imageCache) add(id int) {
	c.mu.Lock()
	c.ids[id] = struct{}{}
	c.mu.Unlock()
}

func (c *imageCache) remove(id int) {
	c.mu.Lock()
	delete(c.ids, id)
	c.mu.Unlock()
}

var errNotImage = errors.New("data is not an image")

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
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		return fmt.Errorf("read: %w", err)
	}
	if !strings.HasPrefix(http.DetectContentType(head[:n]), "image/") && !isAVIF(head[:n]) {
		return errNotImage
	}
	img, _, err := image.Decode(io.MultiReader(bytes.NewReader(head[:n]), r))
	if err != nil {
		return fmt.Errorf("decode: %w", err)
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

	// URI attachment
	resp, err := fetchClient.Get(prop.Value)
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
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !orgID.Valid || !isOrgMember(callerID, int(orgID.Int64)) {
			writeError(w, "Forbidden: not a member of the event's organization", http.StatusForbidden)
			return
		}
	}

	imgPath, _, found := imagePathForID(config.Server.ImagesDir, eventID)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	if err := os.Remove(imgPath); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, _ := strconv.Atoi(eventID)
	imgCache.remove(id)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/images/{event_id}
func uploadEventImage(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser && userRole != RolePublisher {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	eventID := r.PathValue("event_id")

	var id int
	var orgID sql.NullInt64
	err := db.QueryRow("SELECT id, organization_id FROM events WHERE id = ?", eventID).Scan(&id, &orgID)
	if err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if userRole != RoleAdmin {
		if !orgID.Valid || !isOrgMember(callerID, int(orgID.Int64)) {
			writeError(w, "Forbidden: not a member of the event's organization", http.StatusForbidden)
			return
		}
	}

	if err := r.ParseMultipartForm(config.Server.MaxBodyBytes); err != nil {
		writeError(w, "Failed to parse multipart form", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("image")
	if err != nil {
		writeError(w, "Missing or unreadable 'image' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if err := saveImageFromReader(id, file); err != nil {
		if errors.Is(err, errNotImage) {
			writeError(w, "File is not an image", http.StatusUnsupportedMediaType)
		} else {
			writeError(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	ext, _ := imageExtFromConfig()
	outPath := filepath.Join(config.Server.ImagesDir, eventID+ext)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"path": outPath})
}
