package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// imageUploadSpec describes one id-keyed image upload endpoint. The five
// image-upload handlers (event/musician/org/location/avatar) shared this
// skeleton — role gate, id parse, access/exists check, ParseMultipartForm,
// FormFile, save, cache add — but diverged inconsistently in write
// deadline, 413 handling, and response format (#1009). imageUploadHandler
// applies the skeleton uniformly; only the per-entity differences are
// supplied by the caller.
type imageUploadSpec struct {
	pathParam     string        // r.PathValue key: "id" or "event_id"
	idLabel       string        // used in "Invalid <idLabel>" error messages
	roles         []string      // roles allowed past the generic gate; empty skips it (checkAccess does its own)
	writeDeadline time.Duration // 0 = server default

	// checkAccess runs after the role gate and id parse. It must write its
	// own error response and return false to deny (403/404/400 as
	// appropriate for the entity); existence checks live here too.
	checkAccess func(w http.ResponseWriter, r *http.Request, callerID int, userRole string, id int) bool

	save     func(id int, r io.Reader) error
	cacheAdd func(id int)

	// respond writes the success response. Defaults to 204 No Content.
	respond func(w http.ResponseWriter, id int)
}

func imageUploadHandler(spec imageUploadSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		callerID, userRole := callerFromRequest(r)
		if len(spec.roles) > 0 && !slices.Contains(spec.roles, userRole) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		// AVIF re-encode (WASM-based) can take well over the server's
		// default 30s WriteTimeout for a detailed photo — extend the
		// deadline for this request rather than raising it server-wide.
		if spec.writeDeadline > 0 {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(spec.writeDeadline))
		}

		idStr := r.PathValue(spec.pathParam)
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, fmt.Sprintf("Invalid %s", spec.idLabel), http.StatusBadRequest)
			return
		}
		if spec.checkAccess != nil && !spec.checkAccess(w, r, callerID, userRole, id) {
			return
		}

		if err := r.ParseMultipartForm(config.Server.MaxBodyBytes); err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				writeError(w, fmt.Sprintf("image too large (max %d MB)", config.Server.MaxBodyBytes>>20), http.StatusRequestEntityTooLarge)
			} else {
				writeError(w, "Failed to parse multipart form", http.StatusBadRequest)
			}
			return
		}
		file, _, err := r.FormFile("image")
		if err != nil {
			writeError(w, "Missing or unreadable 'image' field", http.StatusBadRequest)
			return
		}
		defer file.Close()

		if err := spec.save(id, file); err != nil {
			if errors.Is(err, errNotImage) {
				writeError(w, "File is not an image", http.StatusUnsupportedMediaType)
			} else {
				writeInternalError(w, err)
			}
			return
		}
		spec.cacheAdd(id)

		if spec.respond != nil {
			spec.respond(w, id)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
