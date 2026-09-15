package main

import (
	"bytes"
	"compress/gzip"
	"log"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/js"
)

// serveNegotiatedStatic writes whichever of plain/gz/br precomputed
// representations matches the request's Accept-Encoding (br preferred,
// gzip fallback, plain otherwise) -- shared by every static asset that gets
// the minify+precompute treatment (base.js, qrcode.min.js, the vendored
// Leaflet/markercluster assets, #1323/#1329).
func serveNegotiatedStatic(w http.ResponseWriter, r *http.Request, contentType string, plain, gz, br []byte) {
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=604800")
	switch ae := r.Header.Get("Accept-Encoding"); {
	case strings.Contains(ae, "br"):
		w.Header().Set("Content-Encoding", "br")
		w.Write(br)
	case strings.Contains(ae, "gzip"):
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(gz)
	default:
		w.Write(plain)
	}
}

// minifier is package-level so every minify call (base.js, the vendored
// Leaflet/markercluster assets) shares one instance instead of registering
// the same two mimetypes repeatedly.
var minifier = func() *minify.M {
	m := minify.New()
	m.AddFunc("application/javascript", js.Minify)
	m.AddFunc("text/css", css.Minify)
	return m
}()

// minifyBytes minifies raw as mediatype, falling back to serving it
// unminified (logged, not fatal) on any minifier error -- matches this
// file's existing defensive style for base.js.
func minifyBytes(mediatype, label string, raw []byte) []byte {
	min, err := minifier.Bytes(mediatype, raw)
	if err != nil {
		log.Printf("minify %s: %v -- serving unminified", label, err)
		return raw
	}
	return min
}

// baseJSMin/baseJSMinGzip/baseJSMinBrotli and qrcodeJSGzip/qrcodeJSBrotli are
// precomputed once at startup, not per request (#1323). base.js's doc
// comments (rationale for #1149, #1269, #1322, etc.) stay in the checked-in
// source -- only this in-memory served copy is minified. qrcode.min.js is a
// third-party file that's already minified, so it skips that step and only
// gets the same compression-negotiation treatment for consistency.
//
// Brotli at BestCompression (quality 11) is used unconditionally rather than
// a faster/lower setting: decompression speed on the client (including a
// mobile device) is essentially independent of the quality level used to
// compress, so there's no runtime cost to paying for the smaller output --
// and because this compresses a fixed static file exactly once at startup
// instead of per request, the (slower) encode time at quality 11 doesn't
// matter either.
var (
	baseJSMin       []byte
	baseJSMinGzip   []byte
	baseJSMinBrotli []byte
	qrcodeJSGzip    []byte
	qrcodeJSBrotli  []byte

	// Vendored Leaflet/markercluster (#1329) get the same minify+precompute
	// treatment as base.js -- these are the exact same bytes unpkg.com used
	// to serve, just minified and compressed once at startup instead of
	// per request.
	leafletJSMin           []byte
	leafletJSMinGzip       []byte
	leafletJSMinBrotli     []byte
	leafletCSSMin          []byte
	leafletCSSMinGzip      []byte
	leafletCSSMinBrotli    []byte
	markerclusterJSMin     []byte
	markerclusterJSGzip    []byte
	markerclusterJSBrotli  []byte
	markerclusterCSSMin    []byte
	markerclusterCSSGzip   []byte
	markerclusterCSSBrotli []byte
)

func init() {
	baseJSMin = minifyBytes("application/javascript", "base.js", baseJS)
	baseJSMinGzip = mustGzip(baseJSMin)
	baseJSMinBrotli = mustBrotli(baseJSMin)
	qrcodeJSGzip = mustGzip(qrcodeJS)
	qrcodeJSBrotli = mustBrotli(qrcodeJS)

	leafletJSMin = minifyBytes("application/javascript", "leaflet.js", leafletJS)
	leafletJSMinGzip = mustGzip(leafletJSMin)
	leafletJSMinBrotli = mustBrotli(leafletJSMin)
	leafletCSSMin = minifyBytes("text/css", "leaflet.css", leafletCSS)
	leafletCSSMinGzip = mustGzip(leafletCSSMin)
	leafletCSSMinBrotli = mustBrotli(leafletCSSMin)
	markerclusterJSMin = minifyBytes("application/javascript", "leaflet.markercluster.js", markerclusterJS)
	markerclusterJSGzip = mustGzip(markerclusterJSMin)
	markerclusterJSBrotli = mustBrotli(markerclusterJSMin)
	markerclusterCSSMin = minifyBytes("text/css", "MarkerCluster.Default.css", markerclusterCSS)
	markerclusterCSSGzip = mustGzip(markerclusterCSSMin)
	markerclusterCSSBrotli = mustBrotli(markerclusterCSSMin)
}

func mustGzip(b []byte) []byte {
	var buf bytes.Buffer
	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	gw.Write(b)
	gw.Close()
	return buf.Bytes()
}

func mustBrotli(b []byte) []byte {
	var buf bytes.Buffer
	bw := brotli.NewWriterLevel(&buf, brotli.BestCompression)
	bw.Write(b)
	bw.Close()
	return buf.Bytes()
}
