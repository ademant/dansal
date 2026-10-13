package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestResolveCaller_StripsForgedHeadersOnNoAuth covers #1533: a request with
// no Authorization header must never let a client-forged X-User-ID/
// X-User-Role/X-Session-ID survive to a handler. OptionalTokenMiddleware
// calls the handler even when resolveCaller reports noAuth=true, so those
// headers have to be cleared unconditionally rather than only being set on
// a successful credential check.
func TestResolveCaller_StripsForgedHeadersOnNoAuth(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/events", nil)
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	req.Header.Set("X-Session-ID", "999")

	w := httptest.NewRecorder()
	ok, noAuth := resolveCaller(w, req)

	if ok || !noAuth {
		t.Fatalf("resolveCaller(no Authorization header) = (%v, %v), want (false, true)", ok, noAuth)
	}
	for _, h := range []string{"X-User-ID", "X-User-Role", "X-Session-ID"} {
		if v := req.Header.Get(h); v != "" {
			t.Errorf("forged header %s survived resolveCaller: %q", h, v)
		}
	}
}

// TestOptionalTokenMiddleware_IgnoresForgedRoleHeader is the end-to-end
// shape of #1533: an anonymous request (no Authorization header) that sets
// X-User-Role: admin by hand must reach the handler with that header gone,
// exactly as if it had never been sent.
func TestOptionalTokenMiddleware_IgnoresForgedRoleHeader(t *testing.T) {
	var sawRole string
	handler := OptionalTokenMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRole = r.Header.Get("X-User-Role")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/v1/events", nil)
	req.Header.Set("X-User-Role", RoleAdmin)
	req.Header.Set("X-User-ID", "1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if sawRole != "" {
		t.Errorf("handler saw forged X-User-Role = %q, want empty", sawRole)
	}
}
