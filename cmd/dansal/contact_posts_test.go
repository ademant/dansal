package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

	for _, col := range []string{"lat", "lon"} {
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('contact_posts') WHERE name=?", col).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info(%s): %v", col, err)
		}
		if n != 1 {
			t.Fatalf("contact_posts.%s column missing after migrateDB (n=%d)", col, n)
		}
	}
}

// TestCreateContactPostStoresLatLon asserts the lat/lon captured alongside
// osm_id from the Nominatim city search (#1077) is persisted and returned by
// GET /api/v1/events/{id}/contact-posts, so ride/accommodation posts can be
// plotted on a map.
func TestCreateContactPostStoresLatLon(t *testing.T) {
	setupDedupTestDB(t)
	oldConfig := config
	config = &Config{}
	config.Server.MaxOpenTokensPerAddress = 5
	t.Cleanup(func() { config = oldConfig })

	eventID, _, _, err := insertEvent(db, EventInput{Title: "Test Event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	res, err := db.Exec("INSERT INTO users (email, display_name, role) VALUES ('poster2@example.com','Poster2','user')")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := res.LastInsertId()

	body, _ := json.Marshal(map[string]any{
		"type": "ride_offer", "city": "Köln", "osm_id": 42, "lat": 50.9375, "lon": 6.9603,
		"persons": 1, "nickname": "Tester",
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

	listReq := httptest.NewRequest("GET", "/api/v1/events/"+strconv.Itoa(eventID)+"/contact-posts", nil)
	listReq.SetPathValue("id", strconv.Itoa(eventID))
	listW := httptest.NewRecorder()
	listContactPosts(listW, listReq)
	var posts []ContactPost
	if err := json.Unmarshal(listW.Body.Bytes(), &posts); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}
	p := posts[0]
	if p.OsmID == nil || *p.OsmID != 42 {
		t.Fatalf("OsmID = %v, want 42", p.OsmID)
	}
	if p.Lat == nil || p.Lon == nil || *p.Lat != 50.9375 || *p.Lon != 6.9603 {
		t.Fatalf("Lat/Lon = %v/%v, want 50.9375/6.9603", p.Lat, p.Lon)
	}
}

// TestCreateContactPostClearsLatLonForNonGeoTypes asserts osm_id/lat/lon are
// cleared for post types that have no departure/stay city (#1077), same as
// the existing city-clearing behavior.
func TestCreateContactPostClearsLatLonForNonGeoTypes(t *testing.T) {
	setupDedupTestDB(t)
	oldConfig := config
	config = &Config{}
	config.Server.MaxOpenTokensPerAddress = 5
	t.Cleanup(func() { config = oldConfig })

	eventID, _, _, err := insertEvent(db, EventInput{Title: "Test Event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	res, err := db.Exec("INSERT INTO users (email, display_name, role) VALUES ('poster3@example.com','Poster3','user')")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := res.LastInsertId()

	body, _ := json.Marshal(map[string]any{
		"type": "lost_item", "osm_id": 42, "lat": 50.9375, "lon": 6.9603, "nickname": "Tester",
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

	var osmID sql.NullInt64
	var lat, lon sql.NullFloat64
	if err := db.QueryRow("SELECT osm_id, lat, lon FROM contact_posts WHERE event_id=?", eventID).Scan(&osmID, &lat, &lon); err != nil {
		t.Fatalf("query inserted post: %v", err)
	}
	if osmID.Valid || lat.Valid || lon.Valid {
		t.Fatalf("expected osm_id/lat/lon cleared for lost_item, got osm_id=%v lat=%v lon=%v", osmID, lat, lon)
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

// postCapTestSetup returns a published event ID and a logged-in user ID for
// board-cap tests. It also sets a permissive MaxOpenTokensPerAddress so the
// pending-token guard never fires during cap tests.
func postCapTestSetup(t *testing.T) (eventID int, userID int) {
	t.Helper()
	setupDedupTestDB(t)
	oldConfig := config
	config = &Config{}
	config.Server.MaxOpenTokensPerAddress = 100
	t.Cleanup(func() { config = oldConfig })

	var err error
	eventID, _, _, err = insertEvent(db, EventInput{
		Title: "Cap Test Event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insertEvent: %v", err)
	}
	res, err := db.Exec("INSERT INTO users (email, display_name, role) VALUES ('cap@example.com','Cap Tester','user')")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	uid, _ := res.LastInsertId()
	return eventID, int(uid)
}

// postCapRequest fires POST /api/v1/events/{id}/contact-posts as a logged-in
// user (immediately verified, no email round-trip) and returns the HTTP status.
func postCapRequest(t *testing.T, eventID, userID int, postType string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"type": postType, "city": "Berlin", "persons": 1, "nickname": "Tester",
	})
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/events/%d/contact-posts", eventID),
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", strconv.Itoa(userID))
	req.Header.Set("X-User-Role", "user")
	req.SetPathValue("id", strconv.Itoa(eventID))
	w := httptest.NewRecorder()
	createContactPost(w, req)
	return w.Code
}

// TestBoardPostCapSingleCategory asserts the 1-post cap within a category.
// Tests: second same-type rejected, sibling-type rejected, different-event OK,
// different-user OK.
func TestBoardPostCapSingleCategory(t *testing.T) {
	eventID, userID := postCapTestSetup(t)

	// Second event for cross-event independence check.
	event2ID, _, _, err := insertEvent(db, EventInput{
		Title: "Other Event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert second event: %v", err)
	}
	res2, _ := db.Exec("INSERT INTO users (email, display_name, role) VALUES ('other@example.com','Other','user')")
	otherUID64, _ := res2.LastInsertId()
	otherUserID := int(otherUID64)

	for _, cat := range []struct {
		offer, request string
	}{
		{"ride_offer", "ride_request"},
		{"sleep_offer", "sleep_request"},
		{"ticket_offer", "ticket_request"},
	} {
		t.Run(cat.offer[:strings.IndexByte(cat.offer, '_')], func(t *testing.T) {
			// First offer → 201.
			if code := postCapRequest(t, eventID, userID, cat.offer); code != 201 {
				t.Fatalf("first %s: got %d, want 201", cat.offer, code)
			}
			// Second same offer → 409.
			if code := postCapRequest(t, eventID, userID, cat.offer); code != 409 {
				t.Fatalf("second %s: got %d, want 409", cat.offer, code)
			}
			// Sibling request → 409 (same category cap).
			if code := postCapRequest(t, eventID, userID, cat.request); code != 409 {
				t.Fatalf("sibling %s: got %d, want 409", cat.request, code)
			}
			// Different event → 201 (caps are per event).
			if code := postCapRequest(t, event2ID, userID, cat.offer); code != 201 {
				t.Fatalf("different event %s: got %d, want 201", cat.offer, code)
			}
			// Different user, same event → 201 (caps are per identity).
			if code := postCapRequest(t, eventID, otherUserID, cat.offer); code != 201 {
				t.Fatalf("other user %s: got %d, want 201", cat.offer, code)
			}
		})
	}
}

// TestBoardPostCapLostFound asserts the 5-post cap for lost/found.
func TestBoardPostCapLostFound(t *testing.T) {
	eventID, userID := postCapTestSetup(t)

	// Five posts (mix of lost and found) → all 201.
	for i := 0; i < 5; i++ {
		typ := "lost_item"
		if i%2 == 1 {
			typ = "found_item"
		}
		if code := postCapRequest(t, eventID, userID, typ); code != 201 {
			t.Fatalf("post %d (%s): got %d, want 201", i+1, typ, code)
		}
	}
	// Sixth → 409.
	if code := postCapRequest(t, eventID, userID, "lost_item"); code != 409 {
		t.Fatalf("6th lost_item: got %d, want 409", code)
	}
}

// TestBoardPostCapEmailIdentity asserts the cap is enforced by email for
// anonymous posters (no user_id), and that different emails are independent.
// Posts are inserted directly (bypassing SMTP) to avoid test-environment panics.
func TestBoardPostCapEmailIdentity(t *testing.T) {
	setupDedupTestDB(t)
	oldConfig := config
	config = &Config{}
	config.Server.MaxOpenTokensPerAddress = 100
	t.Cleanup(func() { config = oldConfig })

	eventID, _, _, err := insertEvent(db, EventInput{
		Title: "Email Cap Event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insertEvent: %v", err)
	}

	// Seed alice's sleep_offer directly (avoids SMTP goroutine in tests).
	_, err = db.Exec(
		`INSERT INTO contact_posts (event_id, type, city, persons, nickname, email, manage_token, email_verified, expires_at)
		 VALUES (?, 'sleep_offer', 'Hamburg', 1, 'Alice', 'alice@example.com', 'seed-tok-alice', 0, 9999999999)`,
		eventID,
	)
	if err != nil {
		t.Fatalf("seed alice post: %v", err)
	}

	// boardPostCapExceeded sees alice's pending post (expires_at > now, email match).
	if !boardPostCapExceeded(eventID, "sleep_offer", "alice@example.com", "", 0, 0) {
		t.Fatal("expected cap exceeded for alice's second sleep_offer")
	}
	if !boardPostCapExceeded(eventID, "sleep_request", "alice@example.com", "", 0, 0) {
		t.Fatal("expected cap exceeded for alice's sleep_request (same category)")
	}
	// Different email → not exceeded.
	if boardPostCapExceeded(eventID, "sleep_offer", "bob@example.com", "", 0, 0) {
		t.Fatal("bob should not be capped by alice's post")
	}
	// Different event → not exceeded.
	event2ID, _, _, _ := insertEvent(db, EventInput{
		Title: "Other Event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
	})
	if boardPostCapExceeded(event2ID, "sleep_offer", "alice@example.com", "", 0, 0) {
		t.Fatal("cap should not apply across events")
	}
}

// TestBoardPostCapPatchTypeChange asserts that PATCH changing a post to a type
// whose category already has a live post from the same identity is rejected.
func TestBoardPostCapPatchTypeChange(t *testing.T) {
	eventID, userID := postCapTestSetup(t)

	// Create a sleep post.
	if code := postCapRequest(t, eventID, userID, "sleep_offer"); code != 201 {
		t.Fatalf("sleep_offer: got %d, want 201", code)
	}
	// Create a ride post (different category, allowed).
	if code := postCapRequest(t, eventID, userID, "ride_offer"); code != 201 {
		t.Fatalf("ride_offer: got %d, want 201", code)
	}

	// Fetch the ride post's manage_token.
	var rideID int
	var manageToken string
	db.QueryRow(
		"SELECT id, manage_token FROM contact_posts WHERE event_id=? AND type='ride_offer'",
		eventID,
	).Scan(&rideID, &manageToken)

	// PATCH: change ride_offer → sleep_request (sleep category, cap already full) → 409.
	patch, _ := json.Marshal(map[string]any{"type": "sleep_request"})
	req := httptest.NewRequest(http.MethodPatch,
		fmt.Sprintf("/api/v1/contact-posts/%d?token=%s", rideID, manageToken),
		bytes.NewReader(patch),
	)
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.SetPathValue("id", strconv.Itoa(rideID))
	req.URL.RawQuery = "token=" + manageToken
	w := httptest.NewRecorder()
	updateContactPost(w, req)
	if w.Code != 409 {
		t.Fatalf("PATCH ride→sleep: got %d, want 409", w.Code)
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
