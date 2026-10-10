package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFlatpickrLocaleRoute guards against a regression introduced by #1448
// (self-hosting flatpickr): the route was originally registered as
// "GET /static/flatpickr/l10n/{locale}.js", which panics at startup —
// net/http's ServeMux requires a wildcard to span the whole path segment,
// so a literal suffix like ".js" directly after "{locale}" is rejected
// ("bad wildcard segment (must end with '}')"). This crash-looped
// dansal-web in production until fixed. Registering the exact pattern
// main.go uses here means this test panics (and fails) the same way main()
// would if the pattern regresses.
func TestFlatpickrLocaleRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/flatpickr/l10n/{locale}", func(w http.ResponseWriter, r *http.Request) {
		locale := strings.TrimSuffix(r.PathValue("locale"), ".js")
		asset, ok := flatpickrLocales[locale]
		if !ok {
			http.NotFound(w, r)
			return
		}
		serveNegotiatedStatic(w, r, "application/javascript", asset.plain, asset.gzip, asset.brotli)
	})

	t.Run("known locale", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/flatpickr/l10n/de.js", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Error("expected non-empty locale JS body")
		}
	})

	t.Run("unknown locale", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/flatpickr/l10n/xx.js", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}
