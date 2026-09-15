package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMusicBrainzSearchHandler covers #1313: proxied server-side with a
// compliant User-Agent (impossible for the browser's own fetch() to set),
// admin-login-gated since this is only used from admin_musician_edit.html.
func TestMusicBrainzSearchHandler(t *testing.T) {
	oldBase := musicBrainzBaseURL
	t.Cleanup(func() { musicBrainzBaseURL = oldBase })

	var gotUA, gotPath, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"artists":[{"id":"abc","name":"Test Artist"}]}`))
	}))
	defer upstream.Close()
	musicBrainzBaseURL = upstream.URL

	cfg := &Config{Domain: "example.test"}
	handler := musicBrainzSearchHandler(cfg)

	t.Run("without login: rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/musicbrainz/search?q=Test", nil)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("expected non-200 without a session, got 200")
		}
	})

	t.Run("with login: proxies and returns the artist", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/musicbrainz/search?q=Test+Artist&limit=8", nil)
		req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
		rec := httptest.NewRecorder()
		handler(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if gotUA == "" || strings.Contains(gotUA, "Go-http-client") {
			t.Errorf("User-Agent not set to a compliant value, got %q", gotUA)
		}
		if gotPath != "/ws/2/artist" {
			t.Errorf("upstream path = %q, want /ws/2/artist", gotPath)
		}
		if !strings.Contains(gotQuery, "query=Test") || !strings.Contains(gotQuery, "fmt=json") {
			t.Errorf("upstream query = %q, missing expected params", gotQuery)
		}
		if !strings.Contains(rec.Body.String(), "Test Artist") {
			t.Errorf("response body not passed through: %s", rec.Body.String())
		}
	})
}

// TestMusicBrainzArtistHandlerVariants covers the variant->inc mapping and
// that an unknown variant is rejected rather than passed through as a raw
// `inc=` value.
func TestMusicBrainzArtistHandlerVariants(t *testing.T) {
	oldBase := musicBrainzBaseURL
	t.Cleanup(func() { musicBrainzBaseURL = oldBase })

	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"abc"}`))
	}))
	defer upstream.Close()
	musicBrainzBaseURL = upstream.URL

	cfg := &Config{Domain: "example.test"}
	handler := musicBrainzArtistHandler(cfg)

	t.Run("releases variant maps to inc=release-groups", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/musicbrainz/artist/abc?variant=releases", nil)
		req.SetPathValue("mbid", "abc")
		req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !strings.Contains(gotQuery, "inc=release-groups") {
			t.Errorf("upstream query = %q, want inc=release-groups", gotQuery)
		}
	})

	t.Run("unknown variant is rejected, not passed through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/musicbrainz/artist/abc?variant=anything-goes", nil)
		req.SetPathValue("mbid", "abc")
		req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for an unrecognized variant", rec.Code)
		}
	})
}

// TestDiscogsSearchHandlerSetsRealUserAgent covers the actual bug fixed by
// #1313: the old client-side code tried headers:{'User-Agent':'dansal/1.0'}
// on a fetch(), which browsers silently ignore (User-Agent is a forbidden
// header) — every request went out generically identified. The server-side
// proxy can and does set a real one.
func TestDiscogsSearchHandlerSetsRealUserAgent(t *testing.T) {
	oldBase := discogsBaseURL
	t.Cleanup(func() { discogsBaseURL = oldBase })

	var gotUA, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"Test Artist"}]}`))
	}))
	defer upstream.Close()
	discogsBaseURL = upstream.URL

	cfg := &Config{Domain: "example.test"}
	handler := discogsSearchHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/discogs/search?q=Test+Artist", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotUA == "" || gotUA == "dansal/1.0" || strings.Contains(gotUA, "Go-http-client") {
		t.Errorf("User-Agent not set to a real, distinct value, got %q", gotUA)
	}
	if !strings.Contains(gotQuery, "q=Test") || !strings.Contains(gotQuery, "type=artist") {
		t.Errorf("upstream query = %q, missing expected params", gotQuery)
	}
}

// TestWikidataQIDHandler covers the fixed-query SPARQL proxy: unknown
// `prop` values and unsafe `value` input are rejected locally (never
// forwarded to Wikidata at all), and a genuine single-match response is
// unwrapped to a plain {"qid":...}.
func TestWikidataQIDHandler(t *testing.T) {
	oldBase := wikidataBaseURL
	t.Cleanup(func() { wikidataBaseURL = oldBase })

	var upstreamCalled bool
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":{"bindings":[{"item":{"value":"http://www.wikidata.org/entity/Q123"}}]}}`))
	}))
	defer upstream.Close()
	wikidataBaseURL = upstream.URL

	cfg := &Config{Domain: "example.test"}
	handler := wikidataQIDHandler(cfg)

	t.Run("unknown prop is rejected without calling upstream", func(t *testing.T) {
		upstreamCalled = false
		req := httptest.NewRequest(http.MethodGet, "/admin/api/wikidata/qid?prop=anything&value=x", nil)
		req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
		rec := httptest.NewRecorder()
		handler(rec, req)
		if upstreamCalled {
			t.Error("upstream was called for an unknown prop")
		}
		if strings.TrimSpace(rec.Body.String()) != "{}" {
			t.Errorf("body = %q, want {}", rec.Body.String())
		}
	})

	t.Run("value containing a quote is rejected without calling upstream", func(t *testing.T) {
		upstreamCalled = false
		req := httptest.NewRequest(http.MethodGet, `/admin/api/wikidata/qid?prop=website&value=%22%20}%20UNION%20{`, nil)
		req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
		rec := httptest.NewRecorder()
		handler(rec, req)
		if upstreamCalled {
			t.Error("upstream was called for a value containing a quote")
		}
	})

	t.Run("website prop with a real match returns the qid", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/wikidata/qid?prop=website&value=https://example.test", nil)
		req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(gotQuery, "P856") {
			t.Errorf("upstream query = %q, want the P856 property", gotQuery)
		}
		if strings.TrimSpace(rec.Body.String()) != `{"qid":"Q123"}` {
			t.Errorf("body = %q, want {\"qid\":\"Q123\"}", rec.Body.String())
		}
	})

	t.Run("musicbrainz_artist prop uses P434", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/wikidata/qid?prop=musicbrainz_artist&value=abc-123", nil)
		req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !strings.Contains(gotQuery, "P434") {
			t.Errorf("upstream query = %q, want the P434 property", gotQuery)
		}
	})
}
