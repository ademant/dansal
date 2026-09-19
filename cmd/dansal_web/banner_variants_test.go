package main

import (
	"bytes"
	"html/template"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeBanner(t *testing.T, w, h int) string {
	t.Helper()
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 7)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "banner.jpg"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	bannerCur = nil
	return dir
}

func TestBannerSrcsetAndVariants(t *testing.T) {
	dir := writeBanner(t, 1280, 512)

	if got, want := string(bannerSrcset(dir)), "/banner.avif?w=480 480w, /banner.avif?w=720 720w, /banner.avif?w=960 960w, /banner.avif 1280w"; got != want {
		t.Fatalf("srcset = %q, want %q", got, want)
	}

	h := bannerHandler(dir, nil)
	get := func(url, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Accept", accept)
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}

	jpg := get("/banner.avif?w=480", "*/*")
	if jpg.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("content-type = %q", jpg.Header().Get("Content-Type"))
	}
	img, _, err := image.Decode(bytes.NewReader(jpg.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 480 || b.Dy() != 192 {
		t.Fatalf("size = %v, want 480x192 (aspect kept)", b)
	}

	av := get("/banner.avif?w=480", "image/avif,*/*")
	if av.Header().Get("Content-Type") != "image/avif" || av.Body.Len() == 0 || av.Header().Get("Vary") != "Accept" {
		t.Fatalf("avif response: ct=%q len=%d vary=%q", av.Header().Get("Content-Type"), av.Body.Len(), av.Header().Get("Vary"))
	}

	if rec := get("/banner.avif?w=481", "*/*"); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-allowlisted width = %d, want 400", rec.Code)
	}
	if rec := get("/banner.avif?w=abc", "*/*"); rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage width = %d, want 400", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/banner.avif?w=480", nil)
	req.Header.Set("Accept", "image/avif")
	req.Header.Set("If-None-Match", av.Header().Get("ETag"))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional GET = %d, want 304", rec.Code)
	}
}

func TestBannerSmallSourceHasNoVariants(t *testing.T) {
	dir := writeBanner(t, 400, 160)
	if s := bannerSrcset(dir); s != "" {
		t.Fatalf("srcset for 400px banner = %q, want empty", s)
	}
	// A w= request for a width >= the source falls back to the raw asset.
	rec := httptest.NewRecorder()
	bannerHandler(dir, nil)(rec, httptest.NewRequest(http.MethodGet, "/banner.avif?w=480", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("raw fallback: code=%d ct=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestSrcsetAttributeNotMangledByTemplate(t *testing.T) {
	tpl := template.Must(template.New("x").Parse(`<img srcset="{{.}}">`))
	var buf bytes.Buffer
	v := template.Srcset("/banner.avif?w=480 480w, /banner.avif 1280w")
	if err := tpl.Execute(&buf, v); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), `<img srcset="/banner.avif?w=480 480w, /banner.avif 1280w">`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
