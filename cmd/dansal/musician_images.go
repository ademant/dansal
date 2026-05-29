package main

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

var musicianImagesDir string

type musicianImageCache struct {
	mu  sync.RWMutex
	ids map[int]struct{}
}

var musicianImgCache = &musicianImageCache{ids: make(map[int]struct{})}

func initMusicianImageCache(dir string) {
	musicianImagesDir = dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	musicianImgCache.mu.Lock()
	defer musicianImgCache.mu.Unlock()
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
			musicianImgCache.ids[id] = struct{}{}
		}
	}
}

func hasMusicianImage(id int) bool {
	musicianImgCache.mu.RLock()
	_, ok := musicianImgCache.ids[id]
	musicianImgCache.mu.RUnlock()
	return ok
}

func musicianImageURL(id int) string {
	if hasMusicianImage(id) {
		return "/api/v1/musician-images/" + strconv.Itoa(id)
	}
	return ""
}

func (c *musicianImageCache) add(id int) {
	c.mu.Lock()
	c.ids[id] = struct{}{}
	c.mu.Unlock()
}

func (c *musicianImageCache) remove(id int) {
	c.mu.Lock()
	delete(c.ids, id)
	c.mu.Unlock()
}

// GET /api/v1/musician-images/{id}
func getMusicianImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	for _, c := range idStr {
		if c < '0' || c > '9' {
			writeError(w, "Invalid musician ID", http.StatusBadRequest)
			return
		}
	}
	imgPath, contentType, found := imagePathForID(musicianImagesDir, idStr)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeFile(w, r, imgPath)
}

// POST /api/v1/musician-images/{id}
func uploadMusicianImage(w http.ResponseWriter, r *http.Request) {
	userRole := r.Header.Get("X-User-Role")
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, "Invalid musician ID", http.StatusBadRequest)
		return
	}
	var exists int
	if err := db.QueryRow("SELECT id FROM musicians WHERE id=?", id).Scan(&exists); err != nil {
		writeError(w, "Musician not found", http.StatusNotFound)
		return
	}
	if err := r.ParseMultipartForm(config.Server.MaxBodyBytes); err != nil {
		writeError(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		writeError(w, "Missing image field", http.StatusBadRequest)
		return
	}
	defer file.Close()
	if err := saveImageToDir(id, musicianImagesDir, file); err != nil {
		if errors.Is(err, errNotImage) {
			writeError(w, "File is not an image", http.StatusUnsupportedMediaType)
		} else {
			writeError(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	musicianImgCache.add(id)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/musician-images/{id}
func deleteMusicianImage(w http.ResponseWriter, r *http.Request) {
	userRole := r.Header.Get("X-User-Role")
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	idStr := r.PathValue("id")
	for _, c := range idStr {
		if c < '0' || c > '9' {
			writeError(w, "Invalid musician ID", http.StatusBadRequest)
			return
		}
	}
	id, _ := strconv.Atoi(idStr)
	imgPath, _, found := imagePathForID(musicianImagesDir, idStr)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	if err := os.Remove(imgPath); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	musicianImgCache.remove(id)
	w.WriteHeader(http.StatusNoContent)
}
