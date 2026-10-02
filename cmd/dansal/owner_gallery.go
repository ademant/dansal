package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Picture gallery (#1362): uploaded pictures attached to an owner. Only
// musicians are wired up; owner_gallery is polymorphic like owner_media, so
// orgs/venues can follow without a schema change. Each picture is stored
// through the regular upload pipeline (safe decode, resize, AVIF/JPEG —
// re-encoding drops EXIF, so phone GPS data is never published) as
// <images_dir>/gallery/{id}.<ext> plus a ".sq" square thumbnail.

const ownerGallerySchema = `CREATE TABLE IF NOT EXISTS owner_gallery (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_type   TEXT NOT NULL CHECK(owner_type IN ('musician','organization','location')),
	owner_id     INTEGER NOT NULL,
	sort_order   INTEGER NOT NULL DEFAULT 0,
	caption      TEXT NOT NULL DEFAULT '',
	ai_generated INTEGER NOT NULL DEFAULT 0,
	created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
)`

const maxGalleryCaptionLen = 200

// GalleryImage is one picture as returned by the API.
type GalleryImage struct {
	ID          int    `json:"id"`
	Caption     string `json:"caption"`
	AIGenerated bool   `json:"ai_generated"`
	URL         string `json:"url"`
	ThumbURL    string `json:"thumb_url"`
}

// GalleryItemUpdate is one entry of a GalleryUpdateRequest.
type GalleryItemUpdate struct {
	ID          int    `json:"id"`
	Caption     string `json:"caption"`
	AIGenerated bool   `json:"ai_generated"`
}

// GalleryUpdateRequest is the body of PUT /api/v1/musicians/{id}/gallery:
// the owner's whole gallery in display order. Captions and AI flags are
// replaced; pictures left out are deleted.
type GalleryUpdateRequest struct {
	Items []GalleryItemUpdate `json:"items"`
}

var galleryImagesDir string

func initGalleryImages(dir string) { galleryImagesDir = dir }

// galleryMaxImages is the per-owner picture limit (server.gallery_max_images,
// default 8 — also when no config is loaded, as in some tests).
func galleryMaxImages() int {
	if config == nil || config.Server.GalleryMaxImages <= 0 {
		return 8
	}
	return config.Server.GalleryMaxImages
}

func galleryImageURL(id int) string { return "/api/v1/gallery-images/" + strconv.Itoa(id) }

// loadOwnerGallery returns an owner's pictures in display order.
func loadOwnerGallery(q querier, ownerType string, ownerID int) []GalleryImage {
	rows, err := q.Query(
		`SELECT id, caption, ai_generated FROM owner_gallery
		 WHERE owner_type = ? AND owner_id = ? ORDER BY sort_order, id`,
		ownerType, ownerID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []GalleryImage
	for rows.Next() {
		var g GalleryImage
		if rows.Scan(&g.ID, &g.Caption, &g.AIGenerated) == nil {
			g.URL = galleryImageURL(g.ID)
			g.ThumbURL = g.URL + "?thumb=sq"
			out = append(out, g)
		}
	}
	return out
}

// removeGalleryFiles deletes a picture's files (full size, thumbnails, any
// other variant). The glob "{id}.*" can't match another id's files.
func removeGalleryFiles(id int) {
	if galleryImagesDir == "" {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(galleryImagesDir, strconv.Itoa(id)+".*"))
	for _, m := range matches {
		os.Remove(m)
	}
}

// deleteOwnerGallery drops an owner's pictures, rows and files. owner_gallery
// is polymorphic (no foreign key), so owner delete handlers must call this.
func deleteOwnerGallery(q querier, ownerType string, ownerID int) {
	for _, g := range loadOwnerGallery(q, ownerType, ownerID) {
		removeGalleryFiles(g.ID)
	}
	q.Exec("DELETE FROM owner_gallery WHERE owner_type = ? AND owner_id = ?", ownerType, ownerID)
}

// galleryOwnerSpec is the per-owner-type part of the gallery endpoints.
type galleryOwnerSpec struct {
	ownerType string
	label     string // "Musician" — used in "<label> not found"
	table     string
	roles     []string
}

var musicianGallery = galleryOwnerSpec{
	ownerType: ownerTypeMusician,
	label:     "Musician",
	table:     "musicians",
	roles:     []string{RoleAdmin, RoleUser}, // same as the musician image (#1362)
}

// owner resolves and checks the {id} path owner; writes the error response
// and returns false when the caller may not touch it.
func (s galleryOwnerSpec) owner(w http.ResponseWriter, r *http.Request) (int, bool) {
	_, role := callerFromRequest(r)
	if !requireRole(w, role, s.roles...) {
		return 0, false
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "Invalid "+strings.ToLower(s.label)+" ID", http.StatusBadRequest)
		return 0, false
	}
	var exists int
	if err := db.QueryRow("SELECT id FROM "+s.table+" WHERE id = ?", id).Scan(&exists); err != nil {
		writeError(w, s.label+" not found", http.StatusNotFound)
		return 0, false
	}
	return id, true
}

func normalizeGalleryCaption(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > maxGalleryCaptionLen {
		return "", fmt.Errorf("caption too long (max %d characters)", maxGalleryCaptionLen)
	}
	return s, nil
}

// POST /api/v1/musicians/{id}/gallery-images — multipart: image, caption,
// ai_generated ("1"/"true"). 201 with the new picture; 409 when the gallery
// already holds gallery_max_images pictures.
func (s galleryOwnerSpec) upload(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := s.owner(w, r)
	if !ok {
		return
	}
	// Same reason as imageUploadHandler: the AVIF encode can be slow.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(170 * time.Second))

	if err := r.ParseMultipartForm(config.Server.MaxBodyBytes); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, fmt.Sprintf("image too large (max %d MB)", config.Server.MaxBodyBytes>>20), http.StatusRequestEntityTooLarge)
		} else {
			writeError(w, "Failed to parse multipart form", http.StatusBadRequest)
		}
		return
	}
	caption, err := normalizeGalleryCaption(r.FormValue("caption"))
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	ai := r.FormValue("ai_generated") == "1" || r.FormValue("ai_generated") == "true"
	file, _, err := r.FormFile("image")
	if err != nil {
		writeError(w, "Missing or unreadable 'image' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Reserve the row first (its id names the file); the count check is part
	// of the INSERT so two concurrent uploads can't both pass it.
	res, err := db.Exec(
		`INSERT INTO owner_gallery (owner_type, owner_id, sort_order, caption, ai_generated)
		 SELECT ?, ?, COALESCE(MAX(sort_order), -1) + 1, ?, ? FROM owner_gallery
		 WHERE owner_type = ? AND owner_id = ?
		 HAVING COUNT(*) < ?`,
		s.ownerType, ownerID, caption, ai, s.ownerType, ownerID, galleryMaxImages(),
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, fmt.Sprintf("gallery is full (max %d pictures)", galleryMaxImages()), http.StatusConflict)
		return
	}
	gid64, _ := res.LastInsertId()
	gid := int(gid64)

	if err := saveImageToDir(gid, galleryImagesDir, file, true); err != nil {
		removeGalleryFiles(gid)
		db.Exec("DELETE FROM owner_gallery WHERE id = ?", gid)
		switch {
		case errors.Is(err, errNotImage):
			writeError(w, "File is not an image", http.StatusUnsupportedMediaType)
		case errors.Is(err, errUndecodableImage):
			writeError(w, err.Error(), http.StatusUnsupportedMediaType)
		default:
			writeInternalError(w, err)
		}
		return
	}
	url := galleryImageURL(gid)
	w.Header().Set("Location", url)
	writeJSONStatus(w, http.StatusCreated, GalleryImage{
		ID: gid, Caption: caption, AIGenerated: ai, URL: url, ThumbURL: url + "?thumb=sq",
	})
}

// PUT /api/v1/musicians/{id}/gallery — reorder, re-caption and drop
// pictures in one call (see GalleryUpdateRequest). Returns the new list.
func (s galleryOwnerSpec) update(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := s.owner(w, r)
	if !ok {
		return
	}
	var req GalleryUpdateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	current := map[int]bool{}
	for _, g := range loadOwnerGallery(db, s.ownerType, ownerID) {
		current[g.ID] = true
	}
	seen := map[int]bool{}
	for i, it := range req.Items {
		if !current[it.ID] {
			writeError(w, fmt.Sprintf("items[%d]: picture %d is not in this gallery", i, it.ID), http.StatusBadRequest)
			return
		}
		if seen[it.ID] {
			writeError(w, fmt.Sprintf("items[%d]: picture %d listed twice", i, it.ID), http.StatusBadRequest)
			return
		}
		seen[it.ID] = true
		c, err := normalizeGalleryCaption(it.Caption)
		if err != nil {
			writeError(w, fmt.Sprintf("items[%d]: %v", i, err), http.StatusBadRequest)
			return
		}
		req.Items[i].Caption = c
	}

	tx, err := db.Begin()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback()
	for i, it := range req.Items {
		if _, err := tx.Exec(
			"UPDATE owner_gallery SET sort_order = ?, caption = ?, ai_generated = ? WHERE id = ?",
			i, it.Caption, it.AIGenerated, it.ID,
		); err != nil {
			writeInternalError(w, err)
			return
		}
	}
	var dropped []int
	for id := range current {
		if !seen[id] {
			if _, err := tx.Exec("DELETE FROM owner_gallery WHERE id = ?", id); err != nil {
				writeInternalError(w, err)
				return
			}
			dropped = append(dropped, id)
		}
	}
	if err := tx.Commit(); err != nil {
		writeInternalError(w, err)
		return
	}
	for _, id := range dropped {
		removeGalleryFiles(id)
	}
	writeJSON(w, nonNilGallery(loadOwnerGallery(db, s.ownerType, ownerID)))
}

// DELETE /api/v1/musicians/{id}/gallery/{gid}
func (s galleryOwnerSpec) remove(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := s.owner(w, r)
	if !ok {
		return
	}
	gid, err := strconv.Atoi(r.PathValue("gid"))
	if err != nil {
		writeError(w, "Invalid picture ID", http.StatusBadRequest)
		return
	}
	res, err := db.Exec("DELETE FROM owner_gallery WHERE id = ? AND owner_type = ? AND owner_id = ?", gid, s.ownerType, ownerID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, "Picture not found", http.StatusNotFound)
		return
	}
	removeGalleryFiles(gid)
	w.WriteHeader(http.StatusNoContent)
}

func nonNilGallery(g []GalleryImage) []GalleryImage {
	if g == nil {
		return []GalleryImage{}
	}
	return g
}

// GET /api/v1/gallery-images/{id} (?thumb=sq for the square thumbnail)
func getGalleryImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if !validNumericPathID(w, idStr, "picture") {
		return
	}
	var suffix string
	if r.URL.Query().Get("thumb") == "sq" {
		suffix = ".sq"
	}
	// Only serve pictures that still have a row: a file left behind by a
	// failed cleanup must not stay reachable.
	var exists int
	if err := db.QueryRow("SELECT id FROM owner_gallery WHERE id = ?", idStr).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, "Image not found", http.StatusNotFound)
		} else {
			writeInternalError(w, err)
		}
		return
	}
	imgPath, contentType, _, found := imagePathForIDVariant(galleryImagesDir, idStr, suffix)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, imgPath)
}
