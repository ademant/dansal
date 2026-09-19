package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"golang.org/x/image/draw"
)

// iconSource is the current favicon asset (uploaded favicon.* in images_dir,
// else the embedded default) with lazily rendered, cached PNG variants
// (#1344). Rebuilt whenever the asset's path/mtime/size changes.
type iconSource struct {
	key     string
	svg     []byte      // non-nil: vector source
	img     image.Image // raster source
	modTime time.Time
	mu      sync.Mutex
	cache   map[string][]byte
}

var (
	iconMu  sync.Mutex
	iconCur *iconSource
)

func loadIconSource(imagesDir string, fallbackSVG []byte) *iconSource {
	path, fi := findSiteAssetFile(imagesDir, "favicon")
	key := "default"
	var mod time.Time
	if fi != nil {
		key = path + "|" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "|" + strconv.FormatInt(fi.Size(), 10)
		mod = fi.ModTime()
	}
	iconMu.Lock()
	defer iconMu.Unlock()
	if iconCur != nil && iconCur.key == key {
		return iconCur
	}
	data := fallbackSVG
	if fi != nil {
		if d, err := os.ReadFile(path); err == nil && len(d) > 0 {
			data = d
		}
	}
	s := &iconSource{key: key, modTime: mod, cache: map[string][]byte{}}
	if detectAssetMIME(data) == "image/svg+xml" {
		s.svg = data
	} else if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
		s.img = img
	} else {
		s.svg = fallbackSVG
	}
	iconCur = s
	return s
}

// render draws the favicon fitted (aspect kept, centered) into a size x size
// image, optionally flattened onto opaque white (iOS paints transparent
// apple-touch-icon pixels black).
func (s *iconSource) render(size int, opaque bool) (image.Image, error) {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	if opaque {
		draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	}
	if s.svg != nil {
		icon, err := oksvg.ReadIconStream(bytes.NewReader(s.svg), oksvg.WarnErrorMode)
		if err != nil {
			return nil, fmt.Errorf("parse svg: %w", err)
		}
		vw, vh := icon.ViewBox.W, icon.ViewBox.H
		if vw <= 0 || vh <= 0 {
			return nil, fmt.Errorf("svg has no viewBox")
		}
		scale := min(float64(size)/vw, float64(size)/vh)
		w, h := vw*scale, vh*scale
		icon.SetTarget((float64(size)-w)/2, (float64(size)-h)/2, w, h)
		rgba := image.NewRGBA(image.Rect(0, 0, size, size))
		scanner := rasterx.NewScannerGV(size, size, rgba, rgba.Bounds())
		icon.Draw(rasterx.NewDasher(size, size, scanner), 1)
		draw.Draw(dst, dst.Bounds(), rgba, image.Point{}, draw.Over)
		return dst, nil
	}
	b := s.img.Bounds()
	scale := min(float64(size)/float64(b.Dx()), float64(size)/float64(b.Dy()))
	w, h := max(int(float64(b.Dx())*scale), 1), max(int(float64(b.Dy())*scale), 1)
	r := image.Rect((size-w)/2, (size-h)/2, (size-w)/2+w, (size-h)/2+h)
	draw.CatmullRom.Scale(dst, r, s.img, b, draw.Over, nil)
	return dst, nil
}

func (s *iconSource) png(size int, opaque bool) ([]byte, error) {
	k := strconv.Itoa(size) + strconv.FormatBool(opaque)
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.cache[k]; ok {
		return b, nil
	}
	img, err := s.render(size, opaque)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	s.cache[k] = buf.Bytes()
	return s.cache[k], nil
}

// icoFromPNGs wraps square PNG images (sizes < 256) in an ICO container;
// PNG-in-ICO is supported by every browser and OS that still asks for
// /favicon.ico.
func icoFromPNGs(sizes []int, pngs [][]byte) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, uint16(len(pngs))})
	offset := 6 + 16*len(pngs)
	for i, p := range pngs {
		buf.Write([]byte{byte(sizes[i]), byte(sizes[i]), 0, 0})
		binary.Write(&buf, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&buf, binary.LittleEndian, [2]uint32{uint32(len(p)), uint32(offset)})
		offset += len(p)
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return buf.Bytes()
}

// faviconICOHandler serves /favicon.ico (32x32 + 48x48).
func faviconICOHandler(imagesDir string, fallbackSVG []byte) http.HandlerFunc {
	return iconHandler(imagesDir, fallbackSVG, "image/x-icon", func(s *iconSource) ([]byte, error) {
		sizes := []int{32, 48}
		var pngs [][]byte
		for _, sz := range sizes {
			p, err := s.png(sz, false)
			if err != nil {
				return nil, err
			}
			pngs = append(pngs, p)
		}
		return icoFromPNGs(sizes, pngs), nil
	})
}

// appleTouchIconHandler serves /apple-touch-icon.png (180x180, opaque).
func appleTouchIconHandler(imagesDir string, fallbackSVG []byte) http.HandlerFunc {
	return iconHandler(imagesDir, fallbackSVG, "image/png", func(s *iconSource) ([]byte, error) {
		return s.png(180, true)
	})
}

func iconHandler(imagesDir string, fallbackSVG []byte, contentType string, build func(*iconSource) ([]byte, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := loadIconSource(imagesDir, fallbackSVG)
		data, err := build(s)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		// Same 1h as /favicon.svg: the URL isn't versioned.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		etag := `"` + strconv.Itoa(len(data)) + "-" + strconv.FormatInt(s.modTime.UnixNano(), 36) + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write(data)
	}
}
