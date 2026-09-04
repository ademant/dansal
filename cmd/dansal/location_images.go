package main

import (
	"database/sql"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// A location's site-plan image (#877) — only meaningful on a top-level
// location (building), showing where its rooms are. Mirrors org_images.go.
var locationImagesDir string

var locationImgCache = newImageIDCache()

func initLocationImageCache(dir string) {
	locationImagesDir = dir
	locationImgCache.init(dir)
}

func hasLocationImage(id int) bool {
	return locationImgCache.has(id)
}

// GET /api/v1/location-images/{id}
func getLocationImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if !validNumericPathID(w, idStr, "location") {
		return
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
// roles is left empty: checkLocationWriteAccess does its own role gate
// (admin unrestricted, user/publisher must be an org member of the
// location) which doesn't map onto the generic allow-list gate.
var uploadLocationSitePlan = imageUploadHandler(imageUploadSpec{
	pathParam:     "id",
	idLabel:       "location ID",
	writeDeadline: 170 * time.Second,
	checkAccess: func(w http.ResponseWriter, r *http.Request, callerID int, userRole string, id int) bool {
		idStr := strconv.Itoa(id)
		if !checkLocationWriteAccess(w, callerID, userRole, idStr) {
			return false
		}
		var parentID sql.NullInt64
		if err := db.QueryRow("SELECT parent_id FROM locations WHERE id=?", id).Scan(&parentID); err != nil {
			writeError(w, "Location not found", http.StatusNotFound)
			return false
		}
		if parentID.Valid {
			writeError(w, "a site plan can only be set on a top-level location, not a room", http.StatusBadRequest)
			return false
		}
		return true
	},
	save:     func(id int, r io.Reader) error { return saveImageToDir(id, locationImagesDir, r, false) },
	cacheAdd: locationImgCache.add,
})

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
