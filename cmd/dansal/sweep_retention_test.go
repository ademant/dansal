package main

import (
	"testing"
	"time"
)

// TestSweepRetentionData covers #1440 (G3): the hourly sweep's retention
// pass actually deletes rows past data_retention_days from the four stores
// that, before this fix, no code path ever swept — confirmed/approved/
// checked_in/cancelled bookings, pending_fetch_suggestions, expired-unclaimed
// contact_requests, and timetable_history — while leaving fresher rows (and
// a still-pending booking) untouched.
func TestSweepRetentionData(t *testing.T) {
	bookingTestDB(t)
	config.Server.DataRetentionDays = 30

	now := time.Now().Unix()
	old := now - 31*24*60*60  // past the 30-day retention horizon
	fresh := now - 5*24*60*60 // within it

	res, err := db.Exec("INSERT INTO events (title, start_time, end_time) VALUES ('Test Ball', ?, ?)", now, now+3600)
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := res.LastInsertId()

	if _, err := db.Exec("INSERT INTO contact_posts (event_id, type, city, nickname, expires_at) VALUES (?, 'ride_offer', 'Testville', 'tester', ?)",
		eventID, now+3600); err != nil {
		t.Fatal(err)
	}

	// Bookings: one of each terminal status past expiry, plus a still-open
	// pending one that must survive this sweep (the pre-existing pending
	// sweep in startTokenCleanup, not under test here, handles that one).
	for _, s := range []string{"confirmed", "approved", "checked_in", "cancelled"} {
		if _, err := db.Exec("INSERT INTO bookings (event_id, name, email, status, expires_at) VALUES (?, 'A', 'a@example.com', ?, ?)",
			eventID, s, old); err != nil {
			t.Fatalf("insert %s booking: %v", s, err)
		}
	}
	if _, err := db.Exec("INSERT INTO bookings (event_id, name, email, status, expires_at) VALUES (?, 'B', 'b@example.com', 'pending', ?)",
		eventID, old); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO pending_fetch_suggestions (token, email, feed_url, feed_type, created_at) VALUES
		('tok-old', 'old@example.com', 'https://old.example/feed', 'ical', datetime(?, 'unixepoch')),
		('tok-new', 'new@example.com', 'https://new.example/feed', 'ical', datetime(?, 'unixepoch'))`,
		old, fresh); err != nil {
		t.Fatal(err)
	}

	// contact_requests.expires_at is the short verify-link deadline, not the
	// retention window — a row within that window is swept once it's simply
	// expired. notYetExpired models an unclaimed reply still inside its
	// (short) verification TTL, which this sweep must leave alone.
	notYetExpired := now + 3600
	if _, err := db.Exec(`INSERT INTO contact_requests (post_id, sender_email, message, verify_token, expires_at) VALUES
		(1, 'expired@example.com', 'hi', 'vtok-old', ?),
		(1, 'pending@example.com', 'hi', 'vtok-new', ?)`,
		old, notYetExpired); err != nil {
		t.Fatal(err)
	}
	// A verified (verify_token cleared) request must survive regardless of
	// age — only an unclaimed one is this sweep's target.
	if _, err := db.Exec("INSERT INTO contact_requests (post_id, sender_email, message, expires_at) VALUES (1, 'verified@example.com', 'hi', ?)", old); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec("INSERT INTO timetable_history (event_id, changed_at, snapshot) VALUES (?, ?, '{}'), (?, ?, '{}')",
		eventID, old, eventID, fresh); err != nil {
		t.Fatal(err)
	}

	sweepRetentionData(now)

	var n int
	db.QueryRow("SELECT COUNT(*) FROM bookings WHERE status != 'pending'").Scan(&n)
	if n != 0 {
		t.Errorf("terminal-status bookings left after sweep = %d, want 0", n)
	}
	db.QueryRow("SELECT COUNT(*) FROM bookings WHERE status = 'pending'").Scan(&n)
	if n != 1 {
		t.Errorf("pending booking left after sweep = %d, want 1 (untouched by this sweep)", n)
	}

	var email string
	if err := db.QueryRow("SELECT email FROM pending_fetch_suggestions").Scan(&email); err != nil || email != "new@example.com" {
		t.Errorf("pending_fetch_suggestions after sweep: email=%q, err=%v, want \"new@example.com\"", email, err)
	}

	var emails []string
	rows, err := db.Query("SELECT sender_email FROM contact_requests ORDER BY sender_email")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		emails = append(emails, e)
	}
	rows.Close()
	if len(emails) != 2 || emails[0] != "pending@example.com" || emails[1] != "verified@example.com" {
		t.Errorf("contact_requests after sweep = %v, want [pending@example.com verified@example.com]", emails)
	}

	db.QueryRow("SELECT COUNT(*) FROM timetable_history").Scan(&n)
	if n != 1 {
		t.Errorf("timetable_history rows left after sweep = %d, want 1 (only the fresh one)", n)
	}
}
