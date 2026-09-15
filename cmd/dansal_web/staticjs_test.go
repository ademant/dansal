package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestBaseJSMinifiedAndCompressedCorrectly(t *testing.T) {
	if len(baseJSMin) == 0 {
		t.Fatal("baseJSMin is empty")
	}
	if len(baseJSMin) >= len(baseJS) {
		t.Fatalf("baseJSMin (%d bytes) is not smaller than baseJS (%d bytes)", len(baseJSMin), len(baseJS))
	}

	gr, err := gzip.NewReader(bytes.NewReader(baseJSMinGzip))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	gotGzip, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if !bytes.Equal(gotGzip, baseJSMin) {
		t.Error("baseJSMinGzip does not decompress to baseJSMin")
	}

	br := brotli.NewReader(bytes.NewReader(baseJSMinBrotli))
	gotBrotli, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read brotli: %v", err)
	}
	if !bytes.Equal(gotBrotli, baseJSMin) {
		t.Error("baseJSMinBrotli does not decompress to baseJSMin")
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
