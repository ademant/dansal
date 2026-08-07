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
	"runtime/debug"
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

// decodeJSONBody decodes r.Body's JSON into dst, writing a uniform error
// response and returning false on failure — a 413 (via readBodyOrError's
// wording) when the body exceeds config.Server.MaxBodyBytes, a plain 400
// otherwise. Callers should `return` immediately when this returns false.
// Consolidates the ~67 hand-rolled json.NewDecoder(r.Body).Decode sites that
// previously wrote ad-hoc "invalid request body" errors and, unlike this
// helper, didn't map *http.MaxBytesError to 413 (#1011).
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			writeError(w, fmt.Sprintf(
				"request body exceeds the %d MB limit — reduce the payload size (e.g. split a bulk-create array into multiple smaller requests)",
				config.Server.MaxBodyBytes>>20,
			), http.StatusRequestEntityTooLarge)
			return false
		}
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

// intPathValue parses the named path value as an integer via strconv.Atoi.
// Moved here from register.go and reimplemented on Atoi instead of
// fmt.Sscan, which silently accepted "+5"/leading-whitespace/partial
// parses (#1013).
func intPathValue(r *http.Request, key string) (int, error) {
	v := r.PathValue(key)
	if v == "" {
		return 0, fmt.Errorf("missing path value %s", key)
	}
	return strconv.Atoi(v)
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

// Unwrap lets http.ResponseController (e.g. SetWriteDeadline) see through
// this wrapper to the underlying ResponseWriter, per its documented contract.
func (e *errorIDWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }

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

// PanicRecoveryMiddleware recovers from a panic anywhere downstream, logs the
// stack trace with the request method/path, and writes a generic JSON 500
// instead of letting net/http kill the connection with a bare stack trace
// (#991). It must sit inside ErrorIDMiddleware so the 500 still gets an
// error_id assigned and logged the same way as any other error response.
func PanicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v method=%s path=%s\n%s", rec, r.Method, r.URL.Path, debug.Stack())
				writeError(w, "Internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
