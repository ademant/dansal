package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeAPI records what the web tier forwarded and replies with a canned
// response, standing in for the dansal API.
type forwardedImageReq struct {
	path        string
	rawQuery    string
	ifNoneMatch string
}

func newFakeImageAPI(t *testing.T, got *forwardedImageReq, status int, body, contentType string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.rawQuery = r.URL.RawQuery
		got.ifNoneMatch = r.Header.Get("If-None-Match")
		if status == http.StatusNotModified {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(status)
		io.WriteString(w, body) //nolint:errcheck
	}))
}

// TestImageProxyServesEveryAPIRoute walks the whole prefix table. The web
// routes must mirror the API's own path-value names ({id} vs {event_id} vs
// {img_id}); a mismatch would 404 at the mux and the image would never reach
// the API at all (#1374).
func TestImageProxyServesEveryAPIRoute(t *testing.T) {
	for _, p := range apiImagePrefixes {
		t.Run(p.apiPrefix, func(t *testing.T) {
			var got forwardedImageReq
			upstream := newFakeImageAPI(t, &got, http.StatusOK, "AVIFBYTES", "image/avif")
			defer upstream.Close()

			mux := http.NewServeMux()
			registerImageProxy(mux, &DansalClient{BaseURL: upstream.URL, HTTP: upstream.Client()})

			front := httptest.NewServer(mux)
			defer front.Close()

			resp, err := front.Client().Get(front.URL + p.apiPrefix + "42")
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d, want 200 (route not matched?)", resp.StatusCode)
			}
			if got.path != p.apiPrefix+"42" {
				t.Errorf("upstream saw path %q, want %q", got.path, p.apiPrefix+"42")
			}
			body, _ := io.ReadAll(resp.Body)
			if string(body) != "AVIFBYTES" {
				t.Errorf("body %q, want %q", body, "AVIFBYTES")
			}
			if ct := resp.Header.Get("Content-Type"); ct != "image/avif" {
				t.Errorf("Content-Type %q, want image/avif", ct)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=86400" {
				t.Errorf("Cache-Control %q was not passed through", cc)
			}
		})
	}
}

// TestImageProxyForwardsQueryString guards the variant selectors. ?format=jpeg
// is the Fediverse JPEG sibling (#1054) and ?thumb=sq|wide the grid-thumbnail
// crops (#1158); without the query string the canonical image comes back and
// callers silently get the wrong variant.
func TestImageProxyForwardsQueryString(t *testing.T) {
	var got forwardedImageReq
	upstream := newFakeImageAPI(t, &got, http.StatusOK, "JPEGBYTES", "image/jpeg")
	defer upstream.Close()

	mux := http.NewServeMux()
	registerImageProxy(mux, &DansalClient{BaseURL: upstream.URL, HTTP: upstream.Client()})
	front := httptest.NewServer(mux)
	defer front.Close()

	resp, err := front.Client().Get(front.URL + "/api/v1/images/7?format=jpeg&thumb=sq")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if got.rawQuery != "format=jpeg&thumb=sq" {
		t.Errorf("upstream saw query %q, want %q", got.rawQuery, "format=jpeg&thumb=sq")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type %q, want image/jpeg", ct)
	}
}

// TestImageProxyPreservesNotFound keeps a missing image a 404. Turning it
// into 200 with an empty body is what produces an invisible 0×0 <img> instead
// of a diagnosable failure.
func TestImageProxyPreservesNotFound(t *testing.T) {
	var got forwardedImageReq
	upstream := newFakeImageAPI(t, &got, http.StatusNotFound, `{"error":"Image not found"}`, "application/json")
	defer upstream.Close()

	mux := http.NewServeMux()
	registerImageProxy(mux, &DansalClient{BaseURL: upstream.URL, HTTP: upstream.Client()})
	front := httptest.NewServer(mux)
	defer front.Close()

	resp, err := front.Client().Get(front.URL + "/api/v1/images/999")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
}

// TestImageProxyForwardsConditionalRequest checks revalidation survives the
// hop, so a cached image is answered with 304 and no body.
func TestImageProxyForwardsConditionalRequest(t *testing.T) {
	var got forwardedImageReq
	upstream := newFakeImageAPI(t, &got, http.StatusNotModified, "", "")
	defer upstream.Close()

	mux := http.NewServeMux()
	registerImageProxy(mux, &DansalClient{BaseURL: upstream.URL, HTTP: upstream.Client()})
	front := httptest.NewServer(mux)
	defer front.Close()

	req, err := http.NewRequest(http.MethodGet, front.URL+"/api/v1/org-images/3", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-None-Match", `"abc123"`)
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if got.ifNoneMatch != `"abc123"` {
		t.Errorf("upstream saw If-None-Match %q, want %q", got.ifNoneMatch, `"abc123"`)
	}
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("status %d, want 304", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("304 carried a %d-byte body", len(body))
	}
}

// TestImageProxyUpstreamDownIs502 documents the failure mode when the API is
// unreachable: the web tier reports a bad gateway rather than pretending an
// empty image is a success.
func TestImageProxyUpstreamDownIs502(t *testing.T) {
	mux := http.NewServeMux()
	registerImageProxy(mux, &DansalClient{BaseURL: "http://127.0.0.1:1", HTTP: http.DefaultClient})
	front := httptest.NewServer(mux)
	defer front.Close()

	resp, err := front.Client().Get(front.URL + "/api/v1/images/1")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
}
