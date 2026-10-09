package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestQRCodeJSHandler covers #1448 (compliance G11): users.html's QR code
// library is self-hosted now instead of loaded from unpkg.com.
func TestQRCodeJSHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/static/qrcode.min.js", nil)
	rec := httptest.NewRecorder()
	qrcodeJSHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/javascript" {
		t.Errorf("Content-Type = %q, want application/javascript", ct)
	}
	if !strings.Contains(rec.Body.String(), "QRCode") {
		t.Error("expected the QRCode library source in the response body")
	}
}
