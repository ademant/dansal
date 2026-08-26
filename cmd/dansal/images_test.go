package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestCropToAspect(t *testing.T) {
	cases := []struct {
		name       string
		srcW, srcH int
		targetW    int
		targetH    int
	}{
		{"wide source, square target", 800, 400, 400, 400},
		{"tall source, square target", 400, 800, 400, 400},
		{"wide source, wide target", 1000, 300, 480, 270},
		{"already-target aspect", 480, 270, 480, 270},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := image.NewRGBA(image.Rect(0, 0, tc.srcW, tc.srcH))
			out := cropToAspect(src, tc.targetW, tc.targetH)
			b := out.Bounds()
			if b.Dx() != tc.targetW || b.Dy() != tc.targetH {
				t.Fatalf("got %dx%d, want %dx%d", b.Dx(), b.Dy(), tc.targetW, tc.targetH)
			}
		})
	}
}

// TestSaveImageToDirGenThumbs guards #1158: when genThumbs is true,
// saveImageToDir must produce the canonical file plus .sq/.wide grid-
// thumbnail variants; when false (the default for image types without an
// active grid-crop complaint), it must produce only the canonical file.
func TestSaveImageToDirGenThumbs(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()
	config = &Config{}
	config.Server.ImageXMax = 1024
	config.Server.ImageYMax = 1024
	config.Server.ImageFormat = "jpeg" // avoid the cgo avif encoder in tests

	// Minimal valid JPEG source image.
	src := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 800; x++ {
			src.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, nil); err != nil {
		t.Fatal(err)
	}

	t.Run("genThumbs=true", func(t *testing.T) {
		dir := t.TempDir()
		if err := saveImageToDir(1, dir, bytes.NewReader(buf.Bytes()), true); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"1.jpeg", "1.sq.jpeg", "1.wide.jpeg"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
				t.Errorf("expected %s to exist: %v", f, err)
			}
		}
	})

	t.Run("genThumbs=false", func(t *testing.T) {
		dir := t.TempDir()
		if err := saveImageToDir(2, dir, bytes.NewReader(buf.Bytes()), false); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "2.jpeg")); err != nil {
			t.Errorf("expected canonical file to exist: %v", err)
		}
		for _, f := range []string{"2.sq.jpeg", "2.wide.jpeg"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				t.Errorf("did not expect %s to exist", f)
			}
		}
	})
}
