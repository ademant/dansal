package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	s.mu.Lock()
	defer s.mu.Unlock()
	scanIDFiles(dir, ".jpg", func(id int) { s.ids[id] = struct{}{} })
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

// avatarUploadHandler builds the upload endpoint for one avatar set (org,
// musician, or instructor) on top of the shared imageUploadHandler skeleton
// (#1009) — only the entity-specific access/existence check and save target
// differ between them.
func avatarUploadHandler(s *avatarSet, entityTable, entityLabel string, checkAccess func(callerID int, entityID int) bool) http.HandlerFunc {
	return imageUploadHandler(imageUploadSpec{
		pathParam:     "id",
		idLabel:       entityLabel + " ID",
		roles:         []string{RoleAdmin, RoleUser},
		writeDeadline: 60 * time.Second,
		checkAccess: func(w http.ResponseWriter, r *http.Request, callerID int, userRole string, id int) bool {
			if userRole != RoleAdmin && !checkAccess(callerID, id) {
				writeError(w, "Forbidden", http.StatusForbidden)
				return false
			}
			var exists int
			if err := db.QueryRow(fmt.Sprintf("SELECT id FROM %s WHERE id=?", entityTable), id).Scan(&exists); err != nil {
				writeError(w, fmt.Sprintf("%s not found", entityLabel), http.StatusNotFound)
				return false
			}
			return true
		},
		save:     func(id int, r io.Reader) error { return saveAvatarToDir(id, s.dir, r) },
		cacheAdd: s.add,
	})
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
