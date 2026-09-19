package main

import (
	"bytes"
	"fmt"
	"html/template"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/avif"
	"golang.org/x/image/draw"
)

// bannerWidths are the only ?w= values /banner.avif accepts (#1343): a fixed
// allowlist so query strings can't be used to fill the variant cache.
var bannerWidths = []int{480, 720, 960}

// bannerSource is the decoded current banner plus its lazily encoded
// variants, rebuilt whenever the asset's path/mtime/size changes.
type bannerSource struct {
	key      string
	img      image.Image // nil: SVG/undecodable, no variants possible
	w        int
	modTime  time.Time
	mu       sync.Mutex
	variants map[string][]byte
}

var (
	bannerMu  sync.Mutex
	bannerCur *bannerSource
)

func loadBannerSource(imagesDir string, fallback []byte) *bannerSource {
	path, fi := findSiteAssetFile(imagesDir, "banner")
	key := "default"
	var mod time.Time
	if fi != nil {
		key = path + "|" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "|" + strconv.FormatInt(fi.Size(), 10)
		mod = fi.ModTime()
	}
	bannerMu.Lock()
	defer bannerMu.Unlock()
	if bannerCur != nil && bannerCur.key == key {
		return bannerCur
	}
	data := fallback
	if fi != nil {
		if d, err := os.ReadFile(path); err == nil && len(d) > 0 {
			data = d
		}
	}
	s := &bannerSource{key: key, modTime: mod, variants: map[string][]byte{}}
	if detectAssetMIME(data) != "image/svg+xml" {
		if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
			s.img, s.w = img, img.Bounds().Dx()
		}
	}
	bannerCur = s
	return s
}

// bannerSrcset builds the <img srcset> value: every allowlisted width below
// the source's own width, plus the untouched original at its real width.
// Empty when no variants are possible (SVG banner, undecodable, tiny).
func bannerSrcset(imagesDir string) template.Srcset {
	s := loadBannerSource(imagesDir, bannerAVIF)
	if s.img == nil {
		return ""
	}
	var parts []string
	for _, w := range bannerWidths {
		if w < s.w {
			parts = append(parts, fmt.Sprintf("/banner.avif?w=%d %dw", w, w))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	parts = append(parts, fmt.Sprintf("/banner.avif %dw", s.w))
	return template.Srcset(strings.Join(parts, ", "))
}

func (s *bannerSource) encode(width int, wantAVIF bool) ([]byte, error) {
	k := strconv.Itoa(width) + "/jpeg"
	if wantAVIF {
		k = strconv.Itoa(width) + "/avif"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.variants[k]; ok {
		return b, nil
	}
	b := s.img.Bounds()
	h := max(b.Dy()*width/b.Dx(), 1)
	dst := image.NewNRGBA(image.Rect(0, 0, width, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), s.img, b, draw.Over, nil)
	var buf bytes.Buffer
	var err error
	if wantAVIF {
		err = avif.Encode(&buf, dst, avif.Options{Quality: 55, QualityAlpha: 60, Speed: 6})
	} else {
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80})
	}
	if err != nil {
		return nil, err
	}
	s.variants[k] = buf.Bytes()
	return s.variants[k], nil
}

// bannerHandler serves /banner.avif. Without ?w= it is the unchanged raw
// asset; with an allowlisted ?w= it serves a downscaled variant (AVIF via
// Accept negotiation, JPEG fallback) for narrow screens (#1343).
func bannerHandler(imagesDir string, fallback []byte) http.HandlerFunc {
	raw := dynamicSVGHandler(imagesDir, "banner", fallback)
	return func(w http.ResponseWriter, r *http.Request) {
		wq := r.URL.Query().Get("w")
		if wq == "" {
			raw(w, r)
			return
		}
		width, err := strconv.Atoi(wq)
		ok := false
		for _, aw := range bannerWidths {
			ok = ok || aw == width
		}
		if err != nil || !ok {
			http.Error(w, "unsupported width", http.StatusBadRequest)
			return
		}
		s := loadBannerSource(imagesDir, fallback)
		if s.img == nil || width >= s.w {
			raw(w, r)
			return
		}
		wantAVIF := strings.Contains(r.Header.Get("Accept"), "image/avif")
		data, err := s.encode(width, wantAVIF)
		if err != nil {
			raw(w, r)
			return
		}
		ct := "image/jpeg"
		if wantAVIF {
			ct = "image/avif"
		}
		w.Header().Add("Vary", "Accept")
		w.Header().Set("Content-Type", ct)
		// Same 1h as the raw asset: the URL isn't versioned, so a fresh
		// upload must show up promptly.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		etag := `"` + ct[6:] + "-" + strconv.Itoa(width) + "-" + strconv.FormatInt(s.modTime.UnixNano(), 36) + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write(data)
	}
}
