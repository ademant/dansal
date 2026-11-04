package main

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// setupDeleteEntityDB gives each test its own in-memory calendar DB and
// enables the delete command, as a dev instance would.
func setupDeleteEntityDB(t *testing.T) func() {
	t.Helper()
	old := db
	oldCfg := config
	conn, err := sql.Open("sqlite3", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	db = conn
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()
	config = &Config{Server: ServerConfig{AllowAdminDelete: true}}
	return func() {
		conn.Close()
		db = old
		config = oldCfg
	}
}

func mustExec(t *testing.T, q string, args ...any) int64 {
	t.Helper()
	res, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestDeleteOrganisationDetachesEventsAndDropsFetchURLs(t *testing.T) {
	defer setupDeleteEntityDB(t)()
	org := mustExec(t, "INSERT INTO organizations (name) VALUES ('Org A')")
	other := mustExec(t, "INSERT INTO organizations (name) VALUES ('Org B')")
	mustExec(t, "INSERT INTO fetch_sources (url, organization_id) VALUES ('https://feed.example/a', ?)", org)
	start := time.Date(2030, 1, 1, 20, 0, 0, 0, time.UTC).Format(time.RFC3339)
	ev := mustExec(t, "INSERT INTO events (title, start_time, end_time, organization_id) VALUES ('Ball', ?, ?, ?)", start, start, org)
	series := mustExec(t, "INSERT INTO event_series (title, slug, organization_id) VALUES ('Monthly', 'monthly', ?)", org)
	mustExec(t, "UPDATE events SET series_id = ? WHERE id = ?", series, ev)

	// Preview: nothing changes.
	preview := adminDeleteEntity(adminRequest{Cmd: "delete-entity", Kind: "organisation", EntityID: int(org)})
	if !preview.OK {
		t.Fatalf("preview failed: %s", preview.Error)
	}
	if n := count(t, "SELECT COUNT(*) FROM organizations WHERE id = ?", org); n != 1 {
		t.Fatalf("preview changed the organisation: count=%d", n)
	}
	if n := count(t, "SELECT COUNT(*) FROM fetch_sources WHERE organization_id = ?", org); n != 1 {
		t.Fatalf("preview changed fetch URLs: count=%d", n)
	}

	// Confirmed: org and its fetch URLs go; the event and series stay, org-free.
	resp := adminDeleteEntity(adminRequest{Cmd: "delete-entity", Kind: "organisation", EntityID: int(org), Confirm: true})
	if !resp.OK {
		t.Fatalf("delete failed: %s", resp.Error)
	}
	if n := count(t, "SELECT COUNT(*) FROM organizations WHERE id = ?", org); n != 0 {
		t.Fatalf("organisation still present")
	}
	if n := count(t, "SELECT COUNT(*) FROM fetch_sources WHERE organization_id = ?", org); n != 0 {
		t.Fatalf("fetch URLs of the organisation still present")
	}
	if n := count(t, "SELECT COUNT(*) FROM events WHERE id = ? AND organization_id IS NULL AND series_id = ?", ev, series); n != 1 {
		t.Fatalf("event should stay in its series, org-free")
	}
	if n := count(t, "SELECT COUNT(*) FROM event_series WHERE id = ? AND organization_id IS NULL", series); n != 1 {
		t.Fatalf("series should stay, org-free")
	}
	if n := count(t, "SELECT COUNT(*) FROM organizations WHERE id = ?", other); n != 1 {
		t.Fatalf("unrelated organisation was touched")
	}
}

func TestDeleteLocationDetachesEventsAndTimetableAndRemovesRooms(t *testing.T) {
	defer setupDeleteEntityDB(t)()
	loc := mustExec(t, "INSERT INTO locations (location) VALUES ('Hall')")
	room := mustExec(t, "INSERT INTO locations (location, parent_id) VALUES ('Room 1', ?)", loc)
	start := time.Date(2030, 1, 1, 20, 0, 0, 0, time.UTC).Format(time.RFC3339)
	ev := mustExec(t, "INSERT INTO events (title, start_time, end_time, location_id) VALUES ('Ball', ?, ?, ?)", start, start, loc)
	evRoom := mustExec(t, "INSERT INTO events (title, start_time, end_time, location_id) VALUES ('Workshop', ?, ?, ?)", start, start, room)
	mustExec(t, "INSERT INTO timetable_entries (event_id, title, location_id, start_time, end_time) VALUES (?, 'Dance', ?, '20:00', '21:00')", ev, loc)

	resp := adminDeleteEntity(adminRequest{Cmd: "delete-entity", Kind: "location", EntityID: int(loc), Confirm: true})
	if !resp.OK {
		t.Fatalf("delete failed: %s", resp.Error)
	}
	if n := count(t, "SELECT COUNT(*) FROM locations WHERE id IN (?, ?)", loc, room); n != 0 {
		t.Fatalf("location or its room still present")
	}
	if n := count(t, "SELECT COUNT(*) FROM events WHERE id IN (?, ?) AND location_id IS NULL", ev, evRoom); n != 2 {
		t.Fatalf("events should stay, location-free")
	}
	if n := count(t, "SELECT COUNT(*) FROM timetable_entries WHERE location_id IS NULL"); n != 1 {
		t.Fatalf("timetable entry should stay, location-free")
	}
}

func TestDeleteSeriesKeepsEvents(t *testing.T) {
	defer setupDeleteEntityDB(t)()
	series := mustExec(t, "INSERT INTO event_series (title, slug) VALUES ('Monthly', 'monthly-series')")
	start := time.Date(2030, 1, 1, 20, 0, 0, 0, time.UTC).Format(time.RFC3339)
	ev := mustExec(t, "INSERT INTO events (title, start_time, end_time, series_id) VALUES ('Ball', ?, ?, ?)", start, start, series)

	resp := adminDeleteEntity(adminRequest{Cmd: "delete-entity", Kind: "series", EntityID: int(series), Confirm: true})
	if !resp.OK {
		t.Fatalf("delete failed: %s", resp.Error)
	}
	if n := count(t, "SELECT COUNT(*) FROM events WHERE id = ? AND series_id IS NULL", ev); n != 1 {
		t.Fatalf("event should stay, outside the series")
	}
}

func TestDeleteRefusedWhenDisabled(t *testing.T) {
	defer setupDeleteEntityDB(t)()
	config.Server.AllowAdminDelete = false
	org := mustExec(t, "INSERT INTO organizations (name) VALUES ('Org A')")
	resp := adminDeleteEntity(adminRequest{Cmd: "delete-entity", Kind: "organisation", EntityID: int(org), Confirm: true})
	if resp.OK {
		t.Fatalf("delete should be refused when server.allow_admin_delete is off")
	}
	if n := count(t, "SELECT COUNT(*) FROM organizations WHERE id = ?", org); n != 1 {
		t.Fatalf("organisation was deleted despite the guard")
	}
}

func TestDeleteUnknownKindRefused(t *testing.T) {
	defer setupDeleteEntityDB(t)()
	resp := adminDeleteEntity(adminRequest{Cmd: "delete-entity", Kind: "event", EntityID: 1, Confirm: true})
	if resp.OK {
		t.Fatalf("unknown kind should be refused")
	}
}
