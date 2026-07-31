package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// avatarSet manages JPEG avatars (400×400 max) for one entity type.
// Files are named "{id}.jpg" in a dedicated directory.
type avatarSet struct {
	mu      sync.RWMutex
	ids     map[int]struct{}
	dir     string
	urlBase string // e.g. "/api/v1/org-avatars/"
}

func newAvatarSet(dir, urlBase string) *avatarSet {
	s := &avatarSet{ids: make(map[int]struct{}), dir: dir, urlBase: urlBase}
	entries, _ := os.ReadDir(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jpg") {
			continue
		}
		if id, err := strconv.Atoi(strings.TrimSuffix(name, ".jpg")); err == nil {
			s.ids[id] = struct{}{}
		}
	}
	return s
}

func (s *avatarSet) has(id int) bool {
	s.mu.RLock()
	_, ok := s.ids[id]
	s.mu.RUnlock()
	return ok
}

func (s *avatarSet) add(id int) {
	s.mu.Lock()
	s.ids[id] = struct{}{}
	s.mu.Unlock()
}

func (s *avatarSet) remove(id int) {
	s.mu.Lock()
	delete(s.ids, id)
	s.mu.Unlock()
}

func (s *avatarSet) url(id int) string {
	if s.has(id) {
		return s.urlBase + strconv.Itoa(id)
	}
	return ""
}

func (s *avatarSet) path(idStr string) (string, bool) {
	p := filepath.Join(s.dir, idStr+".jpg")
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	return "", false
}

var (
	orgAvatars        *avatarSet
	musicianAvatars   *avatarSet
	instructorAvatars *avatarSet
)

func initAvatarCaches(imagesDir string) {
	orgAvatars = newAvatarSet(imagesDir+"/org-avatars", "/api/v1/org-avatars/")
	musicianAvatars = newAvatarSet(imagesDir+"/musician-avatars", "/api/v1/musician-avatars/")
	instructorAvatars = newAvatarSet(imagesDir+"/instructor-avatars", "/api/v1/instructor-avatars/")
}

func avatarUploadHandler(s *avatarSet, entityTable, entityLabel string, checkAccess func(callerID int, entityID int) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		callerID, userRole := callerFromRequest(r)
		if userRole != RoleAdmin && userRole != RoleUser {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(60 * time.Second))
		idStr := r.PathValue("id")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, fmt.Sprintf("Invalid %s ID", entityLabel), http.StatusBadRequest)
			return
		}
		if userRole != RoleAdmin && !checkAccess(callerID, id) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		var exists int
		if err := db.QueryRow(fmt.Sprintf("SELECT id FROM %s WHERE id=?", entityTable), id).Scan(&exists); err != nil {
			writeError(w, fmt.Sprintf("%s not found", entityLabel), http.StatusNotFound)
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
		if err := saveAvatarToDir(id, s.dir, file); err != nil {
			if errors.Is(err, errNotImage) {
				writeError(w, "File is not an image", http.StatusUnsupportedMediaType)
			} else {
				writeInternalError(w, err)
			}
			return
		}
		s.add(id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func avatarDeleteHandler(s *avatarSet, entityLabel string, checkAccess func(callerID int, entityID int) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		callerID, userRole := callerFromRequest(r)
		if userRole != RoleAdmin && userRole != RoleUser {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		idStr := r.PathValue("id")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, fmt.Sprintf("Invalid %s ID", entityLabel), http.StatusBadRequest)
			return
		}
		if userRole != RoleAdmin && !checkAccess(callerID, id) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		p, found := s.path(idStr)
		if !found {
			writeError(w, "Avatar not found", http.StatusNotFound)
			return
		}
		if err := os.Remove(p); err != nil {
			writeInternalError(w, err)
			return
		}
		s.remove(id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func avatarGetHandler(s *avatarSet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		for _, c := range idStr {
			if c < '0' || c > '9' {
				writeError(w, "Invalid ID", http.StatusBadRequest)
				return
			}
		}
		p, found := s.path(idStr)
		if !found {
			writeError(w, "Avatar not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, p)
	}
}
