package main

import (
	"bytes"
	"compress/gzip"
	"log"

	"github.com/andybalholm/brotli"
	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/js"
)

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
)

func init() {
	m := minify.New()
	m.AddFunc("application/javascript", js.Minify)
	min, err := m.Bytes("application/javascript", baseJS)
	if err != nil {
		log.Printf("minify base.js: %v -- serving unminified", err)
		min = baseJS
	}
	baseJSMin = min
	baseJSMinGzip = mustGzip(baseJSMin)
	baseJSMinBrotli = mustBrotli(baseJSMin)
	qrcodeJSGzip = mustGzip(qrcodeJS)
	qrcodeJSBrotli = mustBrotli(qrcodeJS)
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
