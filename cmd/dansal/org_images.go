package main

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

var orgImagesDir string

// orgImageCache tracks a mimeType per id rather than just presence (an org
// image can be AVIF or JPEG depending on when it was uploaded relative to a
// server config change), so it layers its own value type on the shared
// scanImageIDs scan (image_cache.go) instead of embedding imageIDCache
// directly (#1010).
type orgImageCache struct {
	mu       sync.RWMutex
	mimeType map[int]string // image/avif or image/jpeg
}

var orgImgCache = &orgImageCache{mimeType: make(map[int]string)}

func initOrgImageCache(dir string) {
	orgImagesDir = dir
	orgImgCache.mu.Lock()
	defer orgImgCache.mu.Unlock()
	scanImageIDs(dir, func(id int, ext string) {
		mime := "image/avif"
		if ext == ".jpeg" {
			mime = "image/jpeg"
		}
		orgImgCache.mimeType[id] = mime
	})
}

func hasOrgImage(id int) bool {
	orgImgCache.mu.RLock()
	_, ok := orgImgCache.mimeType[id]
	orgImgCache.mu.RUnlock()
	return ok
}

func orgImageMediaType(id int) string {
	orgImgCache.mu.RLock()
	mt := orgImgCache.mimeType[id]
	orgImgCache.mu.RUnlock()
	return mt
}

func orgImageURL(id int) string {
	if hasOrgImage(id) {
		return "/api/v1/org-images/" + strconv.Itoa(id)
	}
	return ""
}

func (c *orgImageCache) add(id int, mime string) {
	c.mu.Lock()
	c.mimeType[id] = mime
	c.mu.Unlock()
}

func (c *orgImageCache) remove(id int) {
	c.mu.Lock()
	delete(c.mimeType, id)
	c.mu.Unlock()
}

// GET /api/v1/org-images/{id}
func getOrgImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if !validNumericPathID(w, idStr, "organization") {
		return
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
var uploadOrgImage = imageUploadHandler(imageUploadSpec{
	pathParam:     "id",
	idLabel:       "organization ID",
	roles:         []string{RoleAdmin, RoleUser},
	writeDeadline: 170 * time.Second,
	checkAccess: func(w http.ResponseWriter, r *http.Request, callerID int, userRole string, id int) bool {
		if userRole != RoleAdmin && !isOrgMember(callerID, id) {
			writeError(w, "Forbidden: you must be a member of this organization", http.StatusForbidden)
			return false
		}
		var exists int
		if err := db.QueryRow("SELECT id FROM organizations WHERE id=?", id).Scan(&exists); err != nil {
			writeError(w, "Organization not found", http.StatusNotFound)
			return false
		}
		return true
	},
	save: func(id int, r io.Reader) error { return saveImageToDir(id, orgImagesDir, r, false) },
	cacheAdd: func(id int) {
		_, mime := imageExtFromConfig()
		orgImgCache.add(id, mime)
	},
})

// DELETE /api/v1/org-images/{id}
func deleteOrgImage(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	idStr := r.PathValue("id")
	if !validNumericPathID(w, idStr, "organization") {
		return
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
