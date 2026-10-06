package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// searchStub stands up a minimal API upstream for /search/results. The events
// body holds `body` events while X-Total-Count advertises `total`, so a capped
// scenario (total > searchMaxResults) can be exercised without shipping
// 500+ fixtures through the renderer.
func searchStub(t *testing.T, total, body int) *httptest.Server {
	t.Helper()
	lat, lng := 48.1173, -1.6778
	events := make([]Event, 0, body)
	for i := 0; i < body; i++ {
		events = append(events, Event{
			ID:        7000 + i,
			Title:     "Capped Stub Event",
			StartTime: "2030-01-01T20:00:00Z",
			EndTime:   "2030-01-01T23:00:00Z",
			Location:  &Location{ID: 1, Location: "Hall", Town: "Testville", Country: "France", Latitude: &lat, Longitude: &lng},
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/events"):
			w.Header().Set("X-Total-Count", itoa(total))
			json.NewEncoder(w).Encode(events)
		case strings.HasPrefix(r.URL.Path, "/api/v1/organizations"),
			strings.HasPrefix(r.URL.Path, "/api/v1/tags"):
			w.Write([]byte("[]"))
		default:
			w.Write([]byte("[]"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func callSearchResults(t *testing.T, srv *httptest.Server) searchResultsResponse {
	t.Helper()
	client := &DansalClient{BaseURL: srv.URL, HTTP: http.DefaultClient}
	h := searchResultsHandler(loadTemplates(), loadI18n(""), client)
	req := httptest.NewRequest(http.MethodGet, "/search/results?from=2030-01-01&to=2030-01-31", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out searchResultsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

// TestSearchResultsTooManyIsAdvisory covers #1373. Past searchMaxResults the
// handler used to answer with too_many=true and an empty payload, and the page
// then hid the table, the empty state and the map — a blank screen on any
// usefully-wide date range, even though the client-side town/type/dance filters
// need that very batch to narrow down. too_many must now be advisory: rows and
// geo always come back, alongside the counts for the banner.
func TestSearchResultsTooManyIsAdvisory(t *testing.T) {
	prev := searchThrottle
	searchThrottle = newSubmissionThrottle(600, time.Minute)
	t.Cleanup(func() { searchThrottle = prev })

	t.Run("under cap is uncapped", func(t *testing.T) {
		got := callSearchResults(t, searchStub(t, 3, 3))
		if got.TooMany {
			t.Errorf("TooMany=true for total=3 (cap %d)", searchMaxResults)
		}
		if got.Total != 3 {
			t.Errorf("Total=%d, want 3", got.Total)
		}
		if got.Shown != 3 {
			t.Errorf("Shown=%d, want 3", got.Shown)
		}
		if got.RowsHTML == "" {
			t.Error("RowsHTML is empty")
		}
	})

	t.Run("over cap still returns rows and geo", func(t *testing.T) {
		const total = 628
		got := callSearchResults(t, searchStub(t, total, 2))

		if !got.TooMany {
			t.Errorf("TooMany=false for total=%d (cap %d)", total, searchMaxResults)
		}
		if got.Total != total {
			t.Errorf("Total=%d, want %d", got.Total, total)
		}
		// Shown is what actually arrived, so the banner can say "first N of M".
		if got.Shown != 2 {
			t.Errorf("Shown=%d, want 2 (the fetched batch size)", got.Shown)
		}
		// The regression: these two used to be empty on a capped response.
		if got.RowsHTML == "" {
			t.Error("RowsHTML is empty on a capped response — page would render blank")
		}
		if !strings.Contains(got.RowsHTML, "event-row") {
			t.Errorf("RowsHTML has no event rows: %.200s", got.RowsHTML)
		}
		if len(got.Geo) != 2 {
			t.Errorf("len(Geo)=%d, want 2 — map would render with no markers", len(got.Geo))
		}
	})
}

// TestSearchCapInvariants guards the phase-15 cap choice (#1373). The cap was
// raised 100 → 500, which still sits *below* the 628 matches a 60-day window
// returns on the shared dev instance — deliberately, since rendering every row
// on a wide range is not worth it. That remainder is exactly what the advisory
// banner is for, so the invariants that matter are structural: the fetch limit
// must clear the cap (so the whole displayable page arrives in one request) and
// must stay within the API's own 1000 ceiling.
func TestSearchCapInvariants(t *testing.T) {
	if searchMaxResults < 500 {
		t.Errorf("searchMaxResults=%d, want >=500 (the agreed phase-15 cap; was 100)", searchMaxResults)
	}
	if searchLimit <= searchMaxResults {
		t.Errorf("searchLimit=%d must sit above searchMaxResults=%d so the full page arrives in one request",
			searchLimit, searchMaxResults)
	}
	if searchLimit > 1000 {
		t.Errorf("searchLimit=%d exceeds the API's own 1000 ceiling (applyListPagination)", searchLimit)
	}
}

// TestSearchPageHandlerDeepLink covers #1436: a deep link (e.g. a venue
// page's nearby-radius fallback) pre-fills the geo filter and date range,
// but an invalid/unrecognized lat or radius is dropped rather than passed
// through to break the page's JS with garbage.
func TestSearchPageHandlerDeepLink(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	srv := searchStub(t, 0, 0)
	client := &DansalClient{BaseURL: srv.URL, HTTP: http.DefaultClient}
	h := searchPageHandler(&Config{Domain: "example.test"}, loadTemplates(), client, loadI18n(""))

	t.Run("valid deep link pre-fills the geo filter and date range", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/search?lat=50.73&lng=7.1&radius=100&from=2026-10-06&to=2027-01-06&label=Tanzplatte+Rhein", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{
			`var initLat    = "50.73"`,
			`var initLng    = "7.1"`,
			`var initRadius = "100"`,
			`var dateFrom = "2026-10-06"`,
			`var dateTo   = "2027-01-06"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %q in rendered page", want)
			}
		}
	})

	t.Run("an unrecognized radius is dropped, not passed through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/search?lat=50.73&lng=7.1&radius=37&from=2026-10-06&to=2027-01-06", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body := rec.Body.String()
		if !strings.Contains(body, `var initRadius = ""`) {
			t.Error("an unlisted radius (37) must be dropped, not echoed into the page")
		}
	})

	t.Run("a non-numeric lat is dropped", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/search?lat=not-a-number&lng=7.1&radius=100", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body := rec.Body.String()
		if !strings.Contains(body, `var initLat    = ""`) {
			t.Error("a non-numeric lat must be dropped, not echoed into the page")
		}
	})

	t.Run("an inverted date range falls back to the current week", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/search?from=2026-10-06&to=2026-10-01", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body := rec.Body.String()
		if strings.Contains(body, `var dateFrom = "2026-10-06"`) {
			t.Error("an inverted (to < from) range must not override the current-week default")
		}
	})
}
