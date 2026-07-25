package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A location's site-plan image (#877) — only meaningful on a top-level
// location (building), showing where its rooms are. Mirrors org_images.go.
var locationImagesDir string

type locationImageCache struct {
	mu  sync.RWMutex
	ids map[int]struct{}
}

var locationImgCache = &locationImageCache{ids: make(map[int]struct{})}

func initLocationImageCache(dir string) {
	locationImagesDir = dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	locationImgCache.mu.Lock()
	defer locationImgCache.mu.Unlock()
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
			locationImgCache.ids[id] = struct{}{}
		}
	}
}

func hasLocationImage(id int) bool {
	locationImgCache.mu.RLock()
	_, ok := locationImgCache.ids[id]
	locationImgCache.mu.RUnlock()
	return ok
}

func (c *locationImageCache) add(id int) {
	c.mu.Lock()
	c.ids[id] = struct{}{}
	c.mu.Unlock()
}

func (c *locationImageCache) remove(id int) {
	c.mu.Lock()
	delete(c.ids, id)
	c.mu.Unlock()
}

// GET /api/v1/location-images/{id}
func getLocationImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	for _, c := range idStr {
		if c < '0' || c > '9' {
			writeError(w, "Invalid location ID", http.StatusBadRequest)
			return
		}
	}
	imgPath, contentType, found := imagePathForID(locationImagesDir, idStr)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, imgPath)
}

// POST /api/v1/locations/{id}/site-plan
func uploadLocationSitePlan(w http.ResponseWriter, r *http.Request) {
	callerID, requesterRole := callerFromRequest(r)
	idStr := r.PathValue("id")
	if !checkLocationWriteAccess(w, callerID, requesterRole, idStr) {
		return
	}
	// AVIF re-encode (WASM-based) can take well over the server's default
	// 30s WriteTimeout for a detailed site-plan photo — extend the deadline
	// for this request rather than raising it server-wide.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(90 * time.Second))
	id, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, "Invalid location ID", http.StatusBadRequest)
		return
	}
	var parentID sql.NullInt64
	if err := db.QueryRow("SELECT parent_id FROM locations WHERE id=?", id).Scan(&parentID); err != nil {
		writeError(w, "Location not found", http.StatusNotFound)
		return
	}
	if parentID.Valid {
		writeError(w, "a site plan can only be set on a top-level location, not a room", http.StatusBadRequest)
		return
	}
	if err := r.ParseMultipartForm(config.Server.MaxBodyBytes); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, fmt.Sprintf("image too large (max %d MB)", config.Server.MaxBodyBytes>>20), http.StatusRequestEntityTooLarge)
		} else {
			writeError(w, "Failed to parse form", http.StatusBadRequest)
		}
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		writeError(w, "Missing image field", http.StatusBadRequest)
		return
	}
	defer file.Close()
	if err := saveImageToDir(id, locationImagesDir, file); err != nil {
		if errors.Is(err, errNotImage) {
			writeError(w, "File is not an image", http.StatusUnsupportedMediaType)
		} else {
			writeInternalError(w, err)
		}
		return
	}
	locationImgCache.add(id)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/locations/{id}/site-plan
func deleteLocationSitePlan(w http.ResponseWriter, r *http.Request) {
	callerID, requesterRole := callerFromRequest(r)
	idStr := r.PathValue("id")
	if !checkLocationWriteAccess(w, callerID, requesterRole, idStr) {
		return
	}
	id, _ := strconv.Atoi(idStr)
	imgPath, _, found := imagePathForID(locationImagesDir, idStr)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	if err := os.Remove(imgPath); err != nil {
		writeInternalError(w, err)
		return
	}
	locationImgCache.remove(id)
	w.WriteHeader(http.StatusNoContent)
}
