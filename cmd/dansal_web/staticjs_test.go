package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	"github.com/andybalholm/brotli"
)

// assertCompressedCorrectly checks that gz/br each decompress to exactly
// min -- shared by every asset that gets the minify+precompute treatment
// (base.js, the vendored Leaflet/markercluster assets, #1323/#1329).
func assertCompressedCorrectly(t *testing.T, name string, min, gz, br []byte) {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("%s: gzip.NewReader: %v", name, err)
	}
	gotGzip, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("%s: read gzip: %v", name, err)
	}
	if !bytes.Equal(gotGzip, min) {
		t.Errorf("%s: gzip does not decompress to the minified bytes", name)
	}

	brr := brotli.NewReader(bytes.NewReader(br))
	gotBrotli, err := io.ReadAll(brr)
	if err != nil {
		t.Fatalf("%s: read brotli: %v", name, err)
	}
	if !bytes.Equal(gotBrotli, min) {
		t.Errorf("%s: brotli does not decompress to the minified bytes", name)
	}
}

func TestBaseJSMinifiedAndCompressedCorrectly(t *testing.T) {
	if len(baseJSMin) == 0 {
		t.Fatal("baseJSMin is empty")
	}
	if len(baseJSMin) >= len(baseJS) {
		t.Fatalf("baseJSMin (%d bytes) is not smaller than baseJS (%d bytes)", len(baseJSMin), len(baseJS))
	}
	assertCompressedCorrectly(t, "base.js", baseJSMin, baseJSMinGzip, baseJSMinBrotli)
}

// TestVendoredLeafletMinifiedAndCompressedCorrectly covers #1329: the
// self-hosted Leaflet/markercluster assets get minified smaller than the
// original vendored bytes, and their precomputed gzip/brotli decompress
// back to exactly the minified bytes.
func TestVendoredLeafletMinifiedAndCompressedCorrectly(t *testing.T) {
	cases := []struct {
		name     string
		raw, min []byte
		gz, br   []byte
	}{
		{"leaflet.js", leafletJS, leafletJSMin, leafletJSMinGzip, leafletJSMinBrotli},
		{"leaflet.css", leafletCSS, leafletCSSMin, leafletCSSMinGzip, leafletCSSMinBrotli},
		{"leaflet.markercluster.js", markerclusterJS, markerclusterJSMin, markerclusterJSGzip, markerclusterJSBrotli},
		{"MarkerCluster.Default.css", markerclusterCSS, markerclusterCSSMin, markerclusterCSSGzip, markerclusterCSSBrotli},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if len(c.min) == 0 {
				t.Fatalf("%s: minified bytes are empty", c.name)
			}
			if len(c.min) >= len(c.raw) {
				t.Errorf("%s: minified (%d bytes) is not smaller than raw (%d bytes)", c.name, len(c.min), len(c.raw))
			}
			assertCompressedCorrectly(t, c.name, c.min, c.gz, c.br)
		})
	}
}

func TestQrcodeJSCompressedCorrectly(t *testing.T) {
	gr, err := gzip.NewReader(bytes.NewReader(qrcodeJSGzip))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	gotGzip, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if !bytes.Equal(gotGzip, qrcodeJS) {
		t.Error("qrcodeJSGzip does not decompress to qrcodeJS")
	}

	br := brotli.NewReader(bytes.NewReader(qrcodeJSBrotli))
	gotBrotli, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read brotli: %v", err)
	}
	if !bytes.Equal(gotBrotli, qrcodeJS) {
		t.Error("qrcodeJSBrotli does not decompress to qrcodeJS")
	}
}
