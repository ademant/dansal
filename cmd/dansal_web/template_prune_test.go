package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// fakeListAPI serves the organisation and location lists the prune reads.
func fakeListAPI(t *testing.T, orgIDs, locIDs []int, fail bool) (*DansalClient, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/api/v1/organizations":
			var out []map[string]any
			for _, id := range orgIDs {
				out = append(out, map[string]any{"id": id, "name": "org"})
			}
			json.NewEncoder(w).Encode(out)
		case "/api/v1/locations":
			var out []map[string]any
			for _, id := range locIDs {
				out = append(out, map[string]any{"id": id, "location": "loc"})
			}
			json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	return &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}, srv.Close
}

func setupTemplateDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_, err = conn.Exec(`CREATE TABLE event_templates (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, org_id INTEGER,
		musician_id INTEGER, instructor_id INTEGER, name TEXT NOT NULL, data TEXT NOT NULL DEFAULT '{}')`)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestPruneOrphanTemplatesRemovesOnlyTemplatesWhoseOrgOrLocationIsGone(t *testing.T) {
	db := setupTemplateDB(t)
	client, stop := fakeListAPI(t, []int{5}, []int{3}, false)
	defer stop()

	ins := func(name string, org any, data string) {
		if _, err := db.Exec("INSERT INTO event_templates (user_id, org_id, name, data) VALUES (1, ?, ?, ?)", org, name, data); err != nil {
			t.Fatal(err)
		}
	}
	ins("org kept", 5, `{}`)
	ins("org gone", 9, `{}`)
	ins("loc kept", nil, `{"loc_id":3}`)
	ins("loc gone", nil, `{"loc_id":7}`)
	ins("no org, no loc", nil, `{}`)

	removed, err := pruneOrphanTemplates(context.Background(), db, client)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed=%d, want 2", removed)
	}
	var names []string
	rows, _ := db.Query("SELECT name FROM event_templates ORDER BY name")
	defer rows.Close()
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	want := []string{"loc kept", "no org, no loc", "org kept"}
	if len(names) != len(want) {
		t.Fatalf("remaining=%v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("remaining=%v, want %v", names, want)
		}
	}
}

func TestPruneOrphanTemplatesKeepsEverythingWhenAPIFails(t *testing.T) {
	db := setupTemplateDB(t)
	client, stop := fakeListAPI(t, nil, nil, true)
	defer stop()
	if _, err := db.Exec("INSERT INTO event_templates (user_id, org_id, name) VALUES (1, 9, 'orphan')"); err != nil {
		t.Fatal(err)
	}
	if _, err := pruneOrphanTemplates(context.Background(), db, client); err == nil {
		t.Fatalf("expected the failed API lookup to be reported")
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM event_templates").Scan(&n)
	if n != 1 {
		t.Fatalf("template was pruned although the API lookup failed")
	}
}
