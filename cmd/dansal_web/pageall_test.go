package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// #1827: an instance with more rows than one API page (1000) must not lose
// the rest. The fake API caps each page at apiListLimit, like the real one.
func TestGetOrganizationsReadsEveryPage(t *testing.T) {
	const total = 2500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations" {
			http.NotFound(w, r)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if limit <= 0 || limit > apiListLimit {
			limit = apiListLimit
		}
		var out []map[string]any
		for id := offset + 1; id <= total && len(out) < limit; id++ {
			out = append(out, map[string]any{"id": id, "name": fmt.Sprintf("org %05d", id)})
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	c := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	orgs, err := c.GetOrganizations(context.Background())
	if err != nil {
		t.Fatalf("GetOrganizations: %v", err)
	}
	if len(orgs) != total {
		t.Fatalf("got %d organisations, want %d (a page was lost)", len(orgs), total)
	}
}

func TestPageAllStopsOnShortPage(t *testing.T) {
	calls := 0
	rows, err := pageAll("/api/v1/x?a=1", func(u string, out *[]int) error {
		calls++
		if calls == 1 && u != "/api/v1/x?a=1&limit=1000&offset=0" {
			t.Errorf("first page url = %q", u)
		}
		*out = []int{1, 2, 3}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(rows) != 3 {
		t.Fatalf("calls=%d rows=%d, want 1 call and 3 rows", calls, len(rows))
	}
}
