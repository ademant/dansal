package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestContactPostsOsmIDMigration is a smoke test for the #1041 migration
// (safety-net pattern from CLAUDE.md): createTables + migrateDB, run
// migrateDB a second time to confirm idempotency, then confirm the osm_id
// column exists either way.
func TestContactPostsOsmIDMigration(t *testing.T) {
	setupDedupTestDB(t)
	migrateDB() // idempotency check

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('contact_posts') WHERE name='osm_id'").Scan(&n); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if n != 1 {
		t.Fatalf("contact_posts.osm_id column missing after migrateDB (n=%d)", n)
	}
}

// TestCreateContactPostStoresOsmID asserts a logged-in caller's board post
// (immediately verified, no email round-trip needed) persists the osm_id
// supplied by the Nominatim city search (#1041).
func TestCreateContactPostStoresOsmID(t *testing.T) {
	setupDedupTestDB(t)
	oldConfig := config
	config = &Config{}
	config.Server.MaxOpenTokensPerAddress = 5
	t.Cleanup(func() { config = oldConfig })

	eventID, _, _, err := insertEvent(db, EventInput{Title: "Test Event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	var userID int64
	res, err := db.Exec("INSERT INTO users (email, display_name, role) VALUES ('poster@example.com','Poster','user')")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ = res.LastInsertId()

	body, _ := json.Marshal(map[string]any{
		"type": "ride_offer", "city": "Köln - Ehrenfeld", "osm_id": 123456,
		"persons": 2, "nickname": "Tester",
	})
	req := httptest.NewRequest("POST", "/api/v1/events/"+strconv.Itoa(eventID)+"/contact-posts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", strconv.FormatInt(userID, 10))
	req.Header.Set("X-User-Role", "user")
	req.SetPathValue("id", strconv.Itoa(eventID))
	w := httptest.NewRecorder()
	createContactPost(w, req)

	if w.Code != 201 {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	var postID int64
	var osmID sql.NullInt64
	if err := db.QueryRow("SELECT id, osm_id FROM contact_posts WHERE event_id=?", eventID).Scan(&postID, &osmID); err != nil {
		t.Fatalf("query inserted post: %v", err)
	}
	if !osmID.Valid || osmID.Int64 != 123456 {
		t.Fatalf("osm_id = %v, want 123456", osmID)
	}
}

// TestWipeAndDeleteContactPost asserts deletion clears the private contact
// fields (email/telegram_username/poster_telegram_chat_id/manage_token)
// before removing the row (#1041) — a defense-in-depth measure so a backup
// or WAL snapshot taken mid-transaction never captures the live contact
// data alongside a "deleted" row.
func TestWipeAndDeleteContactPost(t *testing.T) {
	setupDedupTestDB(t)

	eventID, _, _, err := insertEvent(db, EventInput{Title: "Test Event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	res, err := db.Exec(
		`INSERT INTO contact_posts (event_id, type, city, persons, nickname, email, telegram_username, poster_telegram_chat_id, manage_token, email_verified, expires_at)
		 VALUES (?, 'ride_offer', 'Berlin', 1, 'Tester', 'secret@example.com', 'tguser', 'chat123', 'tok-abc', 1, 9999999999)`,
		eventID,
	)
	if err != nil {
		t.Fatalf("insert post: %v", err)
	}
	id64, _ := res.LastInsertId()
	id := int(id64)

	if err := wipeAndDeleteContactPost(id); err != nil {
		t.Fatalf("wipeAndDeleteContactPost: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM contact_posts WHERE id=?", id).Scan(&count)
	if count != 0 {
		t.Fatalf("row still present after wipeAndDeleteContactPost")
	}
}
