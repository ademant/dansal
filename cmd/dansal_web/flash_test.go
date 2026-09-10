package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestImageUploadErrorKey covers the HTTP-status classification behind
// #1285: a 413 or 415 from the API gets its own specific i18n key, anything
// else (including a network-level error with no apiHTTPError at all) falls
// back to the generic admin_save_error key.
func TestImageUploadErrorKey(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"too large", &apiHTTPError{StatusCode: http.StatusRequestEntityTooLarge, Message: "image too large (max 1 MB)"}, "image_too_large"},
		{"unsupported format", &apiHTTPError{StatusCode: http.StatusUnsupportedMediaType, Message: "File is not an image"}, "image_invalid_format"},
		{"other API status", &apiHTTPError{StatusCode: http.StatusBadRequest, Message: "bad request"}, "admin_save_error"},
		{"network error, no apiHTTPError", errors.New("connection refused"), "admin_save_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := imageUploadErrorKey(c.err); got != c.want {
				t.Errorf("imageUploadErrorKey(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// TestImageUploadErrorFlash checks the widget name rides along unchanged
// next to the classified key.
func TestImageUploadErrorFlash(t *testing.T) {
	err := &apiHTTPError{StatusCode: http.StatusRequestEntityTooLarge}
	flash := imageUploadErrorFlash("avatar", err)
	if flash.ImageUploadError != "image_too_large" {
		t.Errorf("ImageUploadError = %q, want image_too_large", flash.ImageUploadError)
	}
	if flash.ImageUploadWidget != "avatar" {
		t.Errorf("ImageUploadWidget = %q, want avatar", flash.ImageUploadWidget)
	}
}

// TestFlashRedirectImageUploadRoundTrip checks the one-time flash carries
// ImageUploadError/ImageUploadWidget through a store/take cycle just like
// the pre-existing board/booking fields, and that a second take (simulating
// a reloaded or bookmarked URL) comes back empty rather than repeating the
// stale error (#985's whole point).
func TestFlashRedirectImageUploadRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/series/1", nil)

	msg := imageUploadErrorFlash("image", &apiHTTPError{StatusCode: http.StatusRequestEntityTooLarge})
	flashRedirect(rec, req, "/admin/series/1", "test-token-1285", msg)

	got := flashTake("test-token-1285")
	if got.ImageUploadError != "image_too_large" || got.ImageUploadWidget != "image" {
		t.Fatalf("flashTake after redirect = %+v, want image_too_large/image", got)
	}

	again := flashTake("test-token-1285")
	if again.ImageUploadError != "" {
		t.Errorf("second flashTake (stale/reloaded URL) = %+v, want empty", again)
	}
}
