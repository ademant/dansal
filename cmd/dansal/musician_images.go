package main

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

var musicianImagesDir string

var musicianImgCache = newImageIDCache()

func initMusicianImageCache(dir string) {
	musicianImagesDir = dir
	musicianImgCache.init(dir)
}

func hasMusicianImage(id int) bool {
	return musicianImgCache.has(id)
}

func musicianImageURL(id int) string {
	if hasMusicianImage(id) {
		return "/api/v1/musician-images/" + strconv.Itoa(id)
	}
	return ""
}

// GET /api/v1/musician-images/{id}
func getMusicianImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if !validNumericPathID(w, idStr, "musician") {
		return
	}
	// Grid-thumbnail variant (#1158): ?thumb=sq for the musician grid's
	// square crop. Falls back to the canonical full-size image (and the
	// caller's CSS object-fit:cover) when no variant exists yet.
	var thumbSuffix string
	if r.URL.Query().Get("thumb") == "sq" {
		thumbSuffix = ".sq"
	}
	imgPath, contentType, _, found := imagePathForIDVariant(musicianImagesDir, idStr, thumbSuffix)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, imgPath)
}

// POST /api/v1/musician-images/{id}
// Note: previously read role from the X-User-Role header directly instead
// of callerFromRequest like its siblings (#1009) — now uniform. callerID is
// unused here since musician images have no org-membership check.
var uploadMusicianImage = imageUploadHandler(imageUploadSpec{
	pathParam:     "id",
	idLabel:       "musician ID",
	roles:         []string{RoleAdmin, RoleUser},
	writeDeadline: 170 * time.Second,
	checkAccess: func(w http.ResponseWriter, r *http.Request, callerID int, userRole string, id int) bool {
		var exists int
		if err := db.QueryRow("SELECT id FROM musicians WHERE id=?", id).Scan(&exists); err != nil {
			writeError(w, "Musician not found", http.StatusNotFound)
			return false
		}
		return true
	},
	save:     func(id int, r io.Reader) error { return saveImageToDir(id, musicianImagesDir, r, true) },
	cacheAdd: musicianImgCache.add,
})

// DELETE /api/v1/musician-images/{id}
func deleteMusicianImage(w http.ResponseWriter, r *http.Request) {
	userRole := r.Header.Get("X-User-Role")
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	idStr := r.PathValue("id")
	if !validNumericPathID(w, idStr, "musician") {
		return
	}
	id, _ := strconv.Atoi(idStr)
	imgPath, _, found := imagePathForID(musicianImagesDir, idStr)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	if err := os.Remove(imgPath); err != nil {
		writeInternalError(w, err)
		return
	}
	musicianImgCache.remove(id)
	w.WriteHeader(http.StatusNoContent)
}
