package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCORSPreflightAllowsPatch covers #1678: the REST API has PATCH routes, so
// the preflight must advertise PATCH or browsers never send those requests.
func TestCORSPreflightAllowsPatch(t *testing.T) {
	oldConfig := config
	config = &Config{Server: ServerConfig{BaseURL: "https://example.test"}}
	t.Cleanup(func() { config = oldConfig })

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/events/1", nil)
	req.Header.Set("Origin", "https://example.org")
	req.Header.Set("Access-Control-Request-Method", "PATCH")
	rec := httptest.NewRecorder()
	CORSMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("preflight must not reach the handler")
	})).ServeHTTP(rec, req)

	methods := rec.Header().Get("Access-Control-Allow-Methods")
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if !strings.Contains(methods, m) {
			t.Errorf("Access-Control-Allow-Methods %q lacks %s", methods, m)
		}
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}
