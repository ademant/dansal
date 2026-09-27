package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tinyWebP is a valid 8×6 lossy WebP. Go's standard library ships no WebP
// decoder, so decoding it only works through the golang.org/x/image/webp
// registration in images.go — without that, uploads of this format failed with
// a 500 (#1372).
const tinyWebP = "UklGRjgAAABXRUJQVlA4ICwAAACwAQCdASoIAAYAAUAmJaACdLoABdQAAJv8b4gvavH/3wJ/7nz/7nz/seAAAA=="

func decodeWebPBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(tinyWebP)
	if err != nil {
		t.Fatalf("decode webp fixture: %v", err)
	}
	return raw
}

// TestDecodeImageSafelyAcceptsWebP guards #1372: WebP is a perfectly ordinary
// format for a browser-side upload, and http.DetectContentType already
// recognises it as image/webp. The upload path must decode it rather than
// failing, which used to surface as a 500.
func TestDecodeImageSafelyAcceptsWebP(t *testing.T) {
	img, err := decodeImageSafely(decodeWebPBytes(t))
	if err != nil {
		t.Fatalf("webp should decode, got error: %v", err)
	}
	if got := img.Bounds(); got.Dx() != 8 || got.Dy() != 6 {
		t.Fatalf("webp decoded to %dx%d, want 8x6", got.Dx(), got.Dy())
	}
}

// TestDecodeImageSafelyClassifiesInput pins the three outcomes the upload
// handler maps to different status codes: not-an-image at all, a file that
// sniffs as an image but cannot be decoded, and success.
func TestDecodeImageSafelyClassifiesInput(t *testing.T) {
	// PNG magic followed by garbage: sniffs as image/png, never decodes.
	corruptPNG := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x7f}, 64)...)
	// RIFF/WEBP header with a garbage payload: sniffs as image/webp, never decodes.
	corruptWebP := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 garbage"), bytes.Repeat([]byte{0x00}, 32)...)

	cases := []struct {
		name string
		data []byte
		want string // "", "notImage", or "undecodable"
	}{
		{"plain text", []byte("this is plain text, definitely not an image"), "notImage"},
		{"truncated PNG", corruptPNG, "undecodable"},
		{"corrupt WebP", corruptWebP, "undecodable"},
		{"valid WebP", decodeWebPBytes(t), ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img, err := decodeImageSafely(tc.data)
			switch tc.want {
			case "":
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				if img == nil {
					t.Fatal("want an image, got nil")
				}
			case "notImage":
				if !errors.Is(err, errNotImage) {
					t.Fatalf("want errNotImage, got %v", err)
				}
			case "undecodable":
				// Must be distinguishable from errNotImage so the handler
				// can report it as an unsupported/corrupt upload, and must
				// not leak out as a generic internal error.
				if !errors.Is(err, errUndecodableImage) {
					t.Fatalf("want errUndecodableImage, got %v", err)
				}
				if errors.Is(err, errNotImage) {
					t.Fatal("undecodable image must not match errNotImage")
				}
			}
		})
	}
}

// TestSaveImageToDirWebp checks the whole upload step rather than just the
// decoder: a WebP upload must produce the stored image instead of an error.
func TestSaveImageToDirWebp(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()
	config = &Config{}
	config.Server.ImageXMax = 1024
	config.Server.ImageYMax = 1024
	config.Server.ImageFormat = "jpeg" // avoid the slow avif encoder in tests

	dir := t.TempDir()
	if err := saveImageToDir(1, dir, bytes.NewReader(decodeWebPBytes(t)), true); err != nil {
		t.Fatalf("saveImageToDir(webp): %v", err)
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	for _, want := range []string{"1.jpeg", "1.sq.jpeg", "1.wide.jpeg"} {
		if !slices.Contains(names, want) {
			t.Errorf("missing %s, got %v", want, names)
		}
	}
}

// multipartUpload builds a multipart/form-data body with a single "image"
// file part, the shape imageUploadHandler expects.
func multipartUpload(t *testing.T, filename string, data []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("image", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

// TestImageUploadHandlerStatusCodes pins the handler's mapping of save
// failures to status codes. An upload the server cannot accept is the
// client's mistake and must be reported as 4xx; only genuine server faults
// may become 500 (#1372).
func TestImageUploadHandlerStatusCodes(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()
	config = &Config{}
	config.Server.MaxBodyBytes = 1 << 20

	cases := []struct {
		name     string
		saveErr  error
		wantCode int
		wantBody string
	}{
		{"success", nil, http.StatusNoContent, ""},
		{"not an image", errNotImage, http.StatusUnsupportedMediaType, "not an image"},
		{"undecodable image", errUndecodableImage, http.StatusUnsupportedMediaType, "unsupported or corrupt image"},
		{"server fault", errors.New("disk on fire"), http.StatusInternalServerError, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := imageUploadHandler(imageUploadSpec{
				pathParam: "id",
				idLabel:   "test ID",
				save: func(int, io.Reader) error {
					return tc.saveErr
				},
				cacheAdd: func(int) {},
			})

			body, ctype := multipartUpload(t, "x.png", []byte("whatever"))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/images/1", body)
			req.Header.Set("Content-Type", ctype)
			req.SetPathValue("id", "1")
			rec := httptest.NewRecorder()

			handler(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("got status %d (%s), want %d", rec.Code, strings.TrimSpace(rec.Body.String()), tc.wantCode)
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body %q does not mention %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

// TestImageUploadHandlerWebPUpload drives a real WebP upload through the
// handler and a real saveImageToDir, so the x/image/webp registration and the
// status mapping are covered together.
func TestImageUploadHandlerWebPUpload(t *testing.T) {
	oldConfig := config
	defer func() { config = oldConfig }()
	config = &Config{}
	config.Server.ImageXMax = 1024
	config.Server.ImageYMax = 1024
	config.Server.ImageFormat = "jpeg"
	config.Server.ImagesDir = t.TempDir()
	config.Server.MaxBodyBytes = 1 << 20

	handler := imageUploadHandler(imageUploadSpec{
		pathParam: "id",
		idLabel:   "test ID",
		save: func(id int, r io.Reader) error {
			return saveImageToDir(id, config.Server.ImagesDir, r, true)
		},
		cacheAdd: func(int) {},
	})

	body, ctype := multipartUpload(t, "photo.webp", decodeWebPBytes(t))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/images/1", body)
	req.Header.Set("Content-Type", ctype)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("webp upload got status %d (%s), want 204", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if _, err := os.Stat(filepath.Join(config.Server.ImagesDir, "1.jpeg")); err != nil {
		t.Fatalf("expected stored image: %v", err)
	}
}
