package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
)

func writeError(w http.ResponseWriter, msg string, code int) {
	if code >= 500 {
		log.Printf("error %d: %s", code, msg)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeInternalError logs the raw error detail server-side and sends a generic
// 500 message to the client, preventing internal DB/system details from leaking.
func writeInternalError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]string{"error": "Internal server error"})
}

// readBodyOrError reads r.Body fully, writing a 413 with guidance to reduce
// the payload size (e.g. splitting a bulk-create array into multiple smaller
// requests) when the body exceeds config.Server.MaxBodyBytes (enforced by
// MaxBodyMiddleware's MaxBytesReader), or a plain 400 for any other read
// error. Returns ok=false after already writing the error response.
func readBodyOrError(w http.ResponseWriter, r *http.Request) (body []byte, ok bool) {
	body, err := io.ReadAll(r.Body)
	if err == nil {
		return body, true
	}
	if errors.As(err, new(*http.MaxBytesError)) {
		writeError(w, fmt.Sprintf(
			"request body exceeds the %d MB limit — reduce the payload size (e.g. split a bulk-create array into multiple smaller requests)",
			config.Server.MaxBodyBytes>>20,
		), http.StatusRequestEntityTooLarge)
		return nil, false
	}
	writeError(w, err.Error(), http.StatusBadRequest)
	return nil, false
}

// writeJSON sets Content-Type and encodes v as JSON with HTTP 200.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// newErrorID returns a short random hex string suitable for use as an error ID.
func newErrorID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%08x", b)
}

// shouldCaptureError returns true for status codes that warrant an error_id:
// anything that is not 2xx, 3xx, 401, 403, or 404.
func shouldCaptureError(code int) bool {
	if code < 400 {
		return false
	}
	return code != 401 && code != 403 && code != 404
}

// errorIDWriter buffers the response body for qualifying status codes so that
// an error_id can be injected before anything is written to the wire.
type errorIDWriter struct {
	http.ResponseWriter
	req       *http.Request
	code      int
	buf       bytes.Buffer
	capture   bool
	committed bool
}

func (e *errorIDWriter) WriteHeader(code int) {
	e.code = code
	e.capture = shouldCaptureError(code)
	if !e.capture {
		e.committed = true
		e.ResponseWriter.WriteHeader(code)
	}
}

func (e *errorIDWriter) Write(b []byte) (int, error) {
	if e.capture {
		return e.buf.Write(b)
	}
	// If Write is called without WriteHeader (implicit 200), mark committed.
	if !e.committed {
		e.committed = true
	}
	return e.ResponseWriter.Write(b)
}

func (e *errorIDWriter) flush() {
	if !e.capture {
		return
	}

	errorID := newErrorID()

	// Log anonymised context (no IP, no username; user_id only when present).
	userID, _ := strconv.Atoi(e.req.Header.Get("X-User-ID"))
	if userID > 0 {
		log.Printf("error_id=%s status=%d method=%s path=%s user_id=%d",
			errorID, e.code, e.req.Method, e.req.URL.Path, userID)
	} else {
		log.Printf("error_id=%s status=%d method=%s path=%s",
			errorID, e.code, e.req.Method, e.req.URL.Path)
	}

	// Inject error_id into JSON body when possible; fall back to original body.
	body := e.buf.Bytes()
	var v map[string]any
	if json.Unmarshal(bytes.TrimSpace(body), &v) == nil {
		v["error_id"] = errorID
		if modified, err := json.Marshal(v); err == nil {
			body = append(modified, '\n')
		}
	}

	e.ResponseWriter.WriteHeader(e.code)
	e.ResponseWriter.Write(body)
}

// ErrorIDMiddleware intercepts error responses (non-2xx/3xx/401/403/404), assigns
// each a unique error_id, logs it anonymised, and injects the error_id into
// the JSON body. It must sit inside GzipMiddleware so it sees uncompressed JSON.
func ErrorIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eiw := &errorIDWriter{ResponseWriter: w, req: r}
		next.ServeHTTP(eiw, r)
		eiw.flush()
	})
}
