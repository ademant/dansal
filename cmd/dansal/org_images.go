package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var orgImagesDir string

type orgImageCache struct {
	mu  sync.RWMutex
	ids map[int]struct{}
}

var orgImgCache = &orgImageCache{ids: make(map[int]struct{})}

func initOrgImageCache(dir string) {
	orgImagesDir = dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	orgImgCache.mu.Lock()
	defer orgImgCache.mu.Unlock()
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
			orgImgCache.ids[id] = struct{}{}
		}
	}
}

func hasOrgImage(id int) bool {
	orgImgCache.mu.RLock()
	_, ok := orgImgCache.ids[id]
	orgImgCache.mu.RUnlock()
	return ok
}

func orgImageURL(id int) string {
	if hasOrgImage(id) {
		return "/api/v1/org-images/" + strconv.Itoa(id)
	}
	return ""
}

func (c *orgImageCache) add(id int) {
	c.mu.Lock()
	c.ids[id] = struct{}{}
	c.mu.Unlock()
}

func (c *orgImageCache) remove(id int) {
	c.mu.Lock()
	delete(c.ids, id)
	c.mu.Unlock()
}

// GET /api/v1/org-images/{id}
func getOrgImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	for _, c := range idStr {
		if c < '0' || c > '9' {
			writeError(w, "Invalid organization ID", http.StatusBadRequest)
			return
		}
	}
	imgPath, contentType, found := imagePathForID(orgImagesDir, idStr)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, imgPath)
}

// POST /api/v1/org-images/{id}
func uploadOrgImage(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	// AVIF re-encode (WASM-based) can take well over the server's default
	// 30s WriteTimeout for a detailed photo — extend the deadline for this
	// request rather than raising it server-wide.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(170 * time.Second))
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, "Invalid organization ID", http.StatusBadRequest)
		return
	}
	if userRole != RoleAdmin {
		if !isOrgMember(callerID, id) {
			writeError(w, "Forbidden: you must be a member of this organization", http.StatusForbidden)
			return
		}
	}
	var exists int
	if err := db.QueryRow("SELECT id FROM organizations WHERE id=?", id).Scan(&exists); err != nil {
		writeError(w, "Organization not found", http.StatusNotFound)
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
	if err := saveImageToDir(id, orgImagesDir, file); err != nil {
		if errors.Is(err, errNotImage) {
			writeError(w, "File is not an image", http.StatusUnsupportedMediaType)
		} else {
			writeInternalError(w, err)
		}
		return
	}
	orgImgCache.add(id)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/org-images/{id}
func deleteOrgImage(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	idStr := r.PathValue("id")
	for _, c := range idStr {
		if c < '0' || c > '9' {
			writeError(w, "Invalid organization ID", http.StatusBadRequest)
			return
		}
	}
	id, _ := strconv.Atoi(idStr)
	if userRole != RoleAdmin {
		if !isOrgMember(callerID, id) {
			writeError(w, "Forbidden: you must be a member of this organization", http.StatusForbidden)
			return
		}
	}
	imgPath, _, found := imagePathForID(orgImagesDir, idStr)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	if err := os.Remove(imgPath); err != nil {
		writeInternalError(w, err)
		return
	}
	orgImgCache.remove(id)
	w.WriteHeader(http.StatusNoContent)
}
