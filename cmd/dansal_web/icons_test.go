package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func fetchIcon(t *testing.T, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d", path, rec.Code)
	}
	return rec
}

func TestAppleTouchIconFromDefaultSVG(t *testing.T) {
	iconCur = nil
	rec := fetchIcon(t, appleTouchIconHandler(t.TempDir(), faviconSVG), "/apple-touch-icon.png")
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q", ct)
	}
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 180 || b.Dy() != 180 {
		t.Fatalf("size = %v, want 180x180", b)
	}
	// Corner is outside the circle: must be opaque white, not transparent.
	r, g, b, a := img.At(1, 1).RGBA()
	if r>>8 != 255 || g>>8 != 255 || b>>8 != 255 || a>>8 != 255 {
		t.Fatalf("corner pixel = %v,%v,%v,%v, want opaque white", r>>8, g>>8, b>>8, a>>8)
	}
	// Something must actually have been drawn (dark stroke of the circle).
	dark := 0
	for y := 0; y < 180; y++ {
		for x := 0; x < 180; x++ {
			if c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA); c.R < 100 && c.B < 120 {
				dark++
			}
		}
	}
	if dark < 200 {
		t.Fatalf("only %d dark pixels: SVG not rasterized", dark)
	}
}

func TestFaviconICOContainer(t *testing.T) {
	iconCur = nil
	rec := fetchIcon(t, faviconICOHandler(t.TempDir(), faviconSVG), "/favicon.ico")
	if ct := rec.Header().Get("Content-Type"); ct != "image/x-icon" {
		t.Fatalf("content-type = %q", ct)
	}
	d := rec.Body.Bytes()
	var hdr [3]uint16
	binary.Read(bytes.NewReader(d[:6]), binary.LittleEndian, &hdr)
	if hdr != [3]uint16{0, 1, 2} {
		t.Fatalf("ICO header = %v", hdr)
	}
	for i, want := range []int{32, 48} {
		e := d[6+16*i : 6+16*(i+1)]
		if int(e[0]) != want || int(e[1]) != want {
			t.Fatalf("entry %d is %dx%d, want %d", i, e[0], e[1], want)
		}
		size := binary.LittleEndian.Uint32(e[8:12])
		off := binary.LittleEndian.Uint32(e[12:16])
		img, err := png.Decode(bytes.NewReader(d[off : off+size]))
		if err != nil || img.Bounds().Dx() != want {
			t.Fatalf("entry %d PNG: %v %v", i, err, img)
		}
	}
}

func TestIconFromRasterUpload(t *testing.T) {
	iconCur = nil
	dir := writeBannerLike(t)
	rec := fetchIcon(t, appleTouchIconHandler(dir, faviconSVG), "/apple-touch-icon.png")
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || img.Bounds().Dx() != 180 {
		t.Fatalf("decode: %v", err)
	}
	var _ image.Image = img
}

// writeBannerLike stores a wide JPEG as favicon.jpg to exercise the raster path.
func writeBannerLike(t *testing.T) string {
	t.Helper()
	dir := writeBanner(t, 400, 200)
	if err := os.Rename(filepath.Join(dir, "banner.jpg"), filepath.Join(dir, "favicon.jpg")); err != nil {
		t.Fatal(err)
	}
	return dir
}
