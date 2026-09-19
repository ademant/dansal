package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func bigPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{uint8(rng.Intn(256)), uint8(y * 5), uint8(x ^ y), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAIBadgeHandlerDownscalesAndNegotiates(t *testing.T) {
	aiBadgeCache = nil
	dir := t.TempDir()
	orig := bigPNG(t, 1254, 1254)
	// Stored under .svg like the real prod file.
	if err := os.WriteFile(filepath.Join(dir, "ai-badge.svg"), orig, 0o644); err != nil {
		t.Fatal(err)
	}
	h := aiBadgeHandler(dir, []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"))

	get := func(accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/ai-badge", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}

	pngRec := get("image/png,*/*")
	if ct := pngRec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("png content-type = %q", ct)
	}
	img, _, err := image.Decode(bytes.NewReader(pngRec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != aiBadgeMaxPx || b.Dy() != aiBadgeMaxPx {
		t.Fatalf("png size = %v, want %dx%d", b, aiBadgeMaxPx, aiBadgeMaxPx)
	}
	if pngRec.Body.Len() >= len(orig)/10 {
		t.Fatalf("png fallback %d B not much smaller than original %d B", pngRec.Body.Len(), len(orig))
	}

	avifRec := get("image/avif,image/webp,*/*")
	if ct := avifRec.Header().Get("Content-Type"); ct != "image/avif" {
		t.Fatalf("avif content-type = %q", ct)
	}
	if avifRec.Body.Len() == 0 || avifRec.Body.Len() >= len(orig)/50 {
		t.Fatalf("avif %d B vs original %d B", avifRec.Body.Len(), len(orig))
	}
	if avifRec.Header().Get("Vary") != "Accept" {
		t.Fatalf("Vary = %q", avifRec.Header().Get("Vary"))
	}

	// Conditional request.
	req := httptest.NewRequest(http.MethodGet, "/ai-badge", nil)
	req.Header.Set("Accept", "image/avif")
	req.Header.Set("If-None-Match", avifRec.Header().Get("ETag"))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional GET = %d, want 304", rec.Code)
	}
}

func TestAIBadgeHandlerPassesThroughSVGAndDefault(t *testing.T) {
	aiBadgeCache = nil
	svg := []byte("<svg xmlns='http://www.w3.org/2000/svg' width='10' height='10'/>")
	rec := httptest.NewRecorder()
	aiBadgeHandler(t.TempDir(), svg)(rec, httptest.NewRequest(http.MethodGet, "/ai-badge", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Fatalf("content-type = %q", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), svg) {
		t.Fatal("default SVG was modified")
	}
}

func TestDetectAssetMIMEPNGContainingSVGText(t *testing.T) {
	data := append([]byte("\x89PNG\r\n\x1a\n"), []byte("....<svg xmlns=...")...)
	if got := detectAssetMIME(data); got != "image/png" {
		t.Fatalf("detectAssetMIME = %q, want image/png", got)
	}
}
