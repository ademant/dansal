package main

import (
	"database/sql"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

var seriesImagesDir string

// seriesImageCache tracks a mimeType per series id, mirroring orgImageCache.
type seriesImageCache struct {
	mu       sync.RWMutex
	mimeType map[int]string // image/avif or image/jpeg
}

var seriesImgCache = &seriesImageCache{mimeType: make(map[int]string)}

func initSeriesImageCache(dir string) {
	seriesImagesDir = dir
	seriesImgCache.mu.Lock()
	defer seriesImgCache.mu.Unlock()
	scanImageIDs(dir, func(id int, ext string) {
		mime := "image/avif"
		if ext == ".jpeg" {
			mime = "image/jpeg"
		}
		seriesImgCache.mimeType[id] = mime
	})
}

func hasSeriesImage(id int) bool {
	seriesImgCache.mu.RLock()
	_, ok := seriesImgCache.mimeType[id]
	seriesImgCache.mu.RUnlock()
	return ok
}

func seriesImageMediaType(id int) string {
	seriesImgCache.mu.RLock()
	mt := seriesImgCache.mimeType[id]
	seriesImgCache.mu.RUnlock()
	return mt
}

func seriesImageURL(id int) string {
	if hasSeriesImage(id) {
		return "/api/v1/series-images/" + strconv.Itoa(id)
	}
	return ""
}

func (c *seriesImageCache) add(id int, mime string) {
	c.mu.Lock()
	c.mimeType[id] = mime
	c.mu.Unlock()
}

func (c *seriesImageCache) remove(id int) {
	c.mu.Lock()
	delete(c.mimeType, id)
	c.mu.Unlock()
}

// checkSeriesImageAccess checks whether callerID may manage images for seriesID.
// Returns false and writes a 403/404 if access is denied.
func checkSeriesImageAccess(w http.ResponseWriter, callerID int, userRole string, id int) bool {
	if userRole == RoleAdmin {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM event_series WHERE id=?", id).Scan(&n); err != nil || n == 0 {
			writeError(w, "Series not found", http.StatusNotFound)
			return false
		}
		return true
	}
	var orgID, musicianID, instructorID sql.NullInt64
	if err := db.QueryRow("SELECT organization_id, musician_id, instructor_id FROM event_series WHERE id=?", id).Scan(&orgID, &musicianID, &instructorID); err != nil {
		writeError(w, "Series not found", http.StatusNotFound)
		return false
	}
	if orgID.Valid && isOrgMember(callerID, int(orgID.Int64)) {
		return true
	}
	if musicianID.Valid && isMusicianOwner(callerID, int(musicianID.Int64)) {
		return true
	}
	if instructorID.Valid && isInstructorOwner(callerID, int(instructorID.Int64)) {
		return true
	}
	writeError(w, "Forbidden", http.StatusForbidden)
	return false
}

// GET /api/v1/series-images/{id}
func getSeriesImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	for _, c := range idStr {
		if c < '0' || c > '9' {
			writeError(w, "Invalid series ID", http.StatusBadRequest)
			return
		}
	}
	imgPath, contentType, found := imagePathForID(seriesImagesDir, idStr)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, imgPath)
}

// POST /api/v1/series-images/{id}
var uploadSeriesImage = imageUploadHandler(imageUploadSpec{
	pathParam:     "id",
	idLabel:       "series ID",
	roles:         []string{RoleAdmin, RoleUser},
	writeDeadline: 170 * time.Second,
	checkAccess: func(w http.ResponseWriter, r *http.Request, callerID int, userRole string, id int) bool {
		return checkSeriesImageAccess(w, callerID, userRole, id)
	},
	save:     func(id int, r io.Reader) error { return saveImageToDir(id, seriesImagesDir, r) },
	cacheAdd: func(id int) { _, mime := imageExtFromConfig(); seriesImgCache.add(id, mime) },
})

// DELETE /api/v1/series-images/{id}
func deleteSeriesImage(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	idStr := r.PathValue("id")
	for _, c := range idStr {
		if c < '0' || c > '9' {
			writeError(w, "Invalid series ID", http.StatusBadRequest)
			return
		}
	}
	id, _ := strconv.Atoi(idStr)
	if !checkSeriesImageAccess(w, callerID, userRole, id) {
		return
	}
	imgPath, _, found := imagePathForID(seriesImagesDir, idStr)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	if err := os.Remove(imgPath); err != nil {
		writeInternalError(w, err)
		return
	}
	seriesImgCache.remove(id)
	w.WriteHeader(http.StatusNoContent)
}
