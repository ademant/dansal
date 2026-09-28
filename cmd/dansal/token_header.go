package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// bearerFromHeader returns the credential carried by an "Authorization:
// Bearer <token>" header, or "" if the header is absent or malformed.
//
// Unlike resolveCaller this performs no lookup and no validation: the
// callers treat the value as an opaque single-use token and do their own
// hashed comparison. It therefore must not be used to decide whether a
// request is authenticated — a non-empty return only means a value was
// supplied.
func bearerFromHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.Split(h, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		return ""
	}
	return parts[1]
}

// noStoreTokenResponse marks a response that carries or consumes single-use
// token material. Two independent leaks are at stake:
//
//   - Cache-Control: no-store keeps a session token or a consumed-token
//     confirmation out of browser and proxy caches. Nothing in the API set
//     this globally; the only Cache-Control values present were on image and
//     ETag routes.
//   - Referrer-Policy: no-referrer stops the token being echoed onward in a
//     Referer header. nginx sets strict-origin-when-cross-origin
//     (deploy/nginx/dansal.conf:139), which still leaks a full path on
//     same-origin navigation, and these are the token-in-path routes where
//     that matters.
//
// The API's SecurityHeadersMiddleware deliberately leaves Referrer-Policy to
// nginx (#1142), so it is set per-route here rather than in the middleware.
func noStoreTokenResponse(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
}

// verifyTokenFromRequest resolves a single-use token from a POST body,
// accepting either an Authorization: Bearer header or a JSON {"token": …}
// field. The header is preferred so the value never has to be serialized
// into a body that a logger might capture.
func verifyTokenFromRequest(r *http.Request) string {
	if tok := bearerFromHeader(r); tok != "" {
		return tok
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil {
		return ""
	}
	var req struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &req) == nil {
		return strings.TrimSpace(req.Token)
	}
	// Form-encoded fallback, so a plain HTML form can post the token too.
	if vals, err := url.ParseQuery(string(body)); err == nil {
		return strings.TrimSpace(vals.Get("token"))
	}
	return ""
}
