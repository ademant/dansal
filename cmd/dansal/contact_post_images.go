package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// contactPostImagesDir returns the storage directory for contact-post images.
// Images are stored as {img_id}.avif (or .jpeg) named by their DB row id so
// each one has a globally unique, opaque URL — no per-post subdirectory.
func contactPostImagesDir() string {
	return filepath.Join(config.Server.ImagesDir, "contact_post_images")
}

// contactPostImageURL returns the public URL for a contact-post image.
func contactPostImageURL(imgID int) string {
	return fmt.Sprintf("/api/v1/contact-post-images/%d", imgID)
}

// contactPostImagesForPost returns a slice of {id, url} maps for all images
// belonging to postID, ordered by creation time.
func contactPostImagesForPost(postID int) []map[string]any {
	rows, err := db.Query(
		"SELECT id FROM contact_post_images WHERE contact_post_id=? ORDER BY id",
		postID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var imgs []map[string]any
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			continue
		}
		imgs = append(imgs, map[string]any{
			"id":  id,
			"url": contactPostImageURL(id),
		})
	}
	return imgs
}

// contactPostImageURLsForPost returns only the URLs for all images of postID.
// Used when building the public ContactPost JSON (no need to expose IDs).
func contactPostImageURLsForPost(postID int) []string {
	rows, err := db.Query(
		"SELECT id FROM contact_post_images WHERE contact_post_id=? ORDER BY id",
		postID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var urls []string
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			continue
		}
		urls = append(urls, contactPostImageURL(id))
	}
	return urls
}

// deleteContactPostImageFiles removes all image files on disk for postID.
// Called during post deletion; DB rows are cleaned up by ON DELETE CASCADE.
// No-ops when config is nil or ImagesDir is unset (e.g. in tests).
func deleteContactPostImageFiles(postID int) {
	if config == nil || config.Server.ImagesDir == "" {
		return
	}
	rows, err := db.Query(
		"SELECT id FROM contact_post_images WHERE contact_post_id=?", postID,
	)
	if err != nil {
		return
	}
	defer rows.Close()
	dir := contactPostImagesDir()
	ext, _ := imageExtFromConfig()
	altExt := ".avif"
	if ext == ".avif" {
		altExt = ".jpeg"
	}
	for rows.Next() {
		var id int
		rows.Scan(&id)
		os.Remove(filepath.Join(dir, fmt.Sprintf("%d%s", id, ext)))
		os.Remove(filepath.Join(dir, fmt.Sprintf("%d%s", id, altExt)))
	}
}

// GET /api/v1/contact-post-images/{img_id}
// Public. Serves the image file. The opaque numeric ID makes enumeration
// impractical; no additional auth is required (the URL is shared intentionally
// when included in a verified post's public JSON).
func getContactPostImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("img_id")
	if !validNumericPathID(w, idStr, "image") {
		return
	}
	imgPath, contentType, found := imagePathForID(contactPostImagesDir(), idStr)
	if !found {
		writeError(w, "Image not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, imgPath)
}

const contactPostImageCap = 5

// POST /api/v1/contact-posts/{id}/images?token={manage_token}
// Authorized by manage_token query param (anonymous callers).
// Only allowed for lost_item / found_item posts.
// Rejects uploads once the cap of contactPostImageCap images per post is reached.
func uploadContactPostImage(w http.ResponseWriter, r *http.Request) {
	postID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid post id", http.StatusBadRequest)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, "manage token required", http.StatusUnauthorized)
		return
	}

	var storedToken sql.NullString
	var postType string
	var expiresAt int64
	err = db.QueryRow(
		"SELECT COALESCE(manage_token,''), type, expires_at FROM contact_posts WHERE id=?", postID,
	).Scan(&storedToken, &postType, &expiresAt)
	if err == sql.ErrNoRows {
		writeError(w, "post not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !storedToken.Valid || storedToken.String != token {
		writeError(w, "invalid manage token", http.StatusForbidden)
		return
	}
	if time.Now().UTC().Unix() > expiresAt {
		writeError(w, "post expired", http.StatusGone)
		return
	}
	if postType != "lost_item" && postType != "found_item" {
		writeError(w, "images are only supported for lost/found posts", http.StatusBadRequest)
		return
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM contact_post_images WHERE contact_post_id=?", postID).Scan(&count)
	if count >= contactPostImageCap {
		writeError(w, fmt.Sprintf("maximum %d images per post", contactPostImageCap), http.StatusConflict)
		return
	}

	if err := r.ParseMultipartForm(config.Server.MaxBodyBytes); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, fmt.Sprintf("image too large (max %d MB)", config.Server.MaxBodyBytes>>20), http.StatusRequestEntityTooLarge)
		} else {
			writeError(w, "failed to parse form", http.StatusBadRequest)
		}
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		writeError(w, "missing or unreadable 'image' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Reserve a DB row first to get the stable img_id used as the filename.
	result, err := db.Exec(
		"INSERT INTO contact_post_images (contact_post_id) VALUES (?)", postID,
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	imgID, _ := result.LastInsertId()

	if err := saveImageToDir(int(imgID), contactPostImagesDir(), file, false); err != nil {
		db.Exec("DELETE FROM contact_post_images WHERE id=?", imgID) // roll back row
		if errors.Is(err, errNotImage) {
			writeError(w, "file is not an image", http.StatusUnsupportedMediaType)
		} else {
			log.Printf("contact_post_images: save failed post_id=%d img_id=%d: %v", postID, imgID, err)
			writeInternalError(w, err)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"id":  imgID,
		"url": contactPostImageURL(int(imgID)),
	})
}

// DELETE /api/v1/contact-posts/{id}/images/{img_id}?token={manage_token}
// Authorized by manage_token query param.
func deleteContactPostImage(w http.ResponseWriter, r *http.Request) {
	postID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid post id", http.StatusBadRequest)
		return
	}
	imgID, err := intPathValue(r, "img_id")
	if err != nil {
		writeError(w, "invalid image id", http.StatusBadRequest)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, "manage token required", http.StatusUnauthorized)
		return
	}

	var storedToken sql.NullString
	var expiresAt int64
	err = db.QueryRow(
		"SELECT COALESCE(manage_token,''), expires_at FROM contact_posts WHERE id=?", postID,
	).Scan(&storedToken, &expiresAt)
	if err == sql.ErrNoRows {
		writeError(w, "post not found", http.StatusNotFound)
		return
	}
	if !storedToken.Valid || storedToken.String != token {
		writeError(w, "invalid manage token", http.StatusForbidden)
		return
	}
	if time.Now().UTC().Unix() > expiresAt {
		writeError(w, "post expired", http.StatusGone)
		return
	}

	// Verify image belongs to this post.
	var dummy int
	err = db.QueryRow(
		"SELECT id FROM contact_post_images WHERE id=? AND contact_post_id=?", imgID, postID,
	).Scan(&dummy)
	if err == sql.ErrNoRows {
		writeError(w, "image not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}

	if _, err := db.Exec("DELETE FROM contact_post_images WHERE id=?", imgID); err != nil {
		writeInternalError(w, err)
		return
	}

	// Remove files — best-effort; stale files are harmless but waste disk space.
	dir := contactPostImagesDir()
	ext, _ := imageExtFromConfig()
	os.Remove(filepath.Join(dir, fmt.Sprintf("%d%s", imgID, ext)))
	if ext == ".avif" {
		os.Remove(filepath.Join(dir, fmt.Sprintf("%d.jpeg", imgID)))
	} else {
		os.Remove(filepath.Join(dir, fmt.Sprintf("%d.avif", imgID)))
	}

	w.WriteHeader(http.StatusNoContent)
}
