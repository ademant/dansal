package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/avif"
	"golang.org/x/image/draw"
)

// aiBadgeMaxPx is the longer-side size of the served badge: the templates
// display it at max 64 CSS px, so 128 covers 2x HiDPI (#1341).
const aiBadgeMaxPx = 128

type aiBadgeVariant struct {
	key        string // path|mtime|size of the source asset
	avifData   []byte
	pngData    []byte
	modTime    time.Time
	passthru   []byte // non-nil: serve as-is (SVG, embedded default)
	passthruCT string
}

var (
	aiBadgeMu    sync.Mutex
	aiBadgeCache *aiBadgeVariant
)

// findSiteAssetFile is findSiteAssetOnDisk's path-returning counterpart.
func findSiteAssetFile(dir, key string) (string, os.FileInfo) {
	if dir == "" {
		return "", nil
	}
	for _, ext := range siteAssetExts {
		p := filepath.Join(dir, key+ext)
		if fi, err := os.Stat(p); err == nil {
			return p, fi
		}
	}
	return "", nil
}

// buildAIBadgeVariant downscales a raster upload to aiBadgeMaxPx and encodes
// it as AVIF plus a small PNG fallback. SVG (and anything undecodable) is
// passed through unchanged.
func buildAIBadgeVariant(data []byte) (*aiBadgeVariant, error) {
	if detectAssetMIME(data) == "image/svg+xml" {
		return &aiBadgeVariant{passthru: data, passthruCT: "image/svg+xml"}, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > aiBadgeMaxPx || h > aiBadgeMaxPx {
		if w >= h {
			h, w = h*aiBadgeMaxPx/w, aiBadgeMaxPx
		} else {
			w, h = w*aiBadgeMaxPx/h, aiBadgeMaxPx
		}
		w, h = max(w, 1), max(h, 1)
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
		src = dst
	}
	var avifBuf, pngBuf bytes.Buffer
	if err := avif.Encode(&avifBuf, src, avif.Options{Quality: 50, QualityAlpha: 60, Speed: 6}); err != nil {
		return nil, fmt.Errorf("avif encode: %w", err)
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&pngBuf, src); err != nil {
		return nil, fmt.Errorf("png encode: %w", err)
	}
	return &aiBadgeVariant{avifData: avifBuf.Bytes(), pngData: pngBuf.Bytes()}, nil
}

// aiBadgeHandler serves /ai-badge as a small, cached variant of the uploaded
// asset instead of the raw upload, which can be an ~800 KB full-size PNG for
// a 64px badge (#1341). Variants are rebuilt whenever the asset's
// mtime/size changes, so a fresh upload needs no restart.
func aiBadgeHandler(imagesDir string, fallback []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path, fi := findSiteAssetFile(imagesDir, "ai-badge")
		key := "default"
		var mod time.Time
		if fi != nil {
			key = path + "|" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "|" + strconv.FormatInt(fi.Size(), 10)
			mod = fi.ModTime()
		}

		aiBadgeMu.Lock()
		v := aiBadgeCache
		if v == nil || v.key != key {
			v = nil
			var data []byte
			if fi != nil {
				data, _ = os.ReadFile(path)
			}
			if len(data) == 0 {
				data = fallback
			}
			nv, err := buildAIBadgeVariant(data)
			if err != nil {
				// Unreadable/undecodable upload: keep serving it as before.
				nv = &aiBadgeVariant{passthru: data, passthruCT: detectAssetMIME(data)}
				if nv.passthruCT == "" {
					nv.passthruCT = "image/svg+xml"
				}
			}
			nv.key, nv.modTime = key, mod
			aiBadgeCache = nv
			v = nv
		}
		aiBadgeMu.Unlock()

		body, ct := v.passthru, v.passthruCT
		if body == nil {
			w.Header().Add("Vary", "Accept")
			if strings.Contains(r.Header.Get("Accept"), "image/avif") {
				body, ct = v.avifData, "image/avif"
			} else {
				body, ct = v.pngData, "image/png"
			}
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		etag := `"` + strconv.FormatUint(uint64(len(body)), 36) + "-" + strconv.FormatInt(v.modTime.UnixNano(), 36) + `"`
		if ct == "image/avif" {
			etag = `"avif-` + etag[1:]
		}
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write(body)
	}
}
