package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1874: a multipart body over its cap is refused with 413, not spilled to disk.
func TestMultipartBodyOverCapIs413(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("image", "big.jpg")
	fw.Write([]byte(strings.Repeat("x", 64<<10)))
	mw.Close()

	r := httptest.NewRequest(http.MethodPost, "/admin/x", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()

	err := parseMultipartWithLimit(w, r, 4<<10)
	if err == nil {
		t.Fatal("expected an error for a body over the cap")
	}
	if got := multipartErrStatus(err); got != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", got)
	}
}

func TestMultipartBodyUnderCapParses(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("title", "ok")
	mw.Close()

	r := httptest.NewRequest(http.MethodPost, "/admin/x", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if err := parseMultipartWithLimit(httptest.NewRecorder(), r, 4<<10); err != nil {
		t.Fatalf("small form rejected: %v", err)
	}
	if r.FormValue("title") != "ok" {
		t.Errorf("title = %q", r.FormValue("title"))
	}
}
