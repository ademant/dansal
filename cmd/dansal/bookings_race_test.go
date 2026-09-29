package main

// Concurrency regressions for the booking TOCTOU races (#1396, #1397):
// checkinBooking and verifyBooking each used to SELECT a row's status, decide
// in Go, then UPDATE with no predicate tying the two together. These tests
// fire genuinely concurrent requests at a shared in-memory DB (with a busy
// timeout so SQLite's write serialization doesn't itself flake the test) and
// assert the compare-and-set UPDATE lets exactly one request win.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// raceTestDB is bookingTestDB (phase18_checkin_window_test.go) plus a busy
// timeout: concurrent writers against a shared-cache :memory: DB otherwise
// hit SQLITE_BUSY immediately instead of queueing, which would make these
// tests flaky rather than exercising the actual interleaving.
func raceTestDB(t *testing.T) {
	t.Helper()
	old := db
	t.Cleanup(func() { db = old })

	name := fmt.Sprintf("file:bookingrace_%d?mode=memory&cache=shared&_busy_timeout=5000", time.Now().UnixNano())
	conn, err := sql.Open("sqlite3", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	db = conn
	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()

	prev := config
	config = &Config{}
	config.Server.CheckinOpensBeforeMinutes = 120
	config.Server.CheckinClosesAfterMinutes = 240
	t.Cleanup(func() { config = prev })
}

// TestCheckinBookingConcurrentScansOnlyOneWins reproduces the door-scan race
// directly: N goroutines scan the same still-valid QR code at once. Before
// the compare-and-set fix, more than one could see status='confirmed' before
// either UPDATE committed and all would report success.
func TestCheckinBookingConcurrentScansOnlyOneWins(t *testing.T) {
	raceTestDB(t)
	now := time.Now()
	eventID := seedBookingEvent(t, now.Add(-time.Hour), now.Add(time.Hour))
	id := seedBooking(t, eventID, "confirmed", "qr-race")

	const attempts = 20
	var wg sync.WaitGroup
	var successes int32
	codes := make([]int, attempts)
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		i := i
		go func() {
			defer wg.Done()
			rec := checkin(t, "qr-race")
			codes[i] = rec.Code
			if rec.Code == http.StatusOK {
				atomic.AddInt32(&successes, 1)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("%d/%d concurrent scans succeeded, want exactly 1: codes=%v", successes, attempts, codes)
	}
	if got := bookingStatus(t, id); got != "checked_in" {
		t.Errorf("status = %q, want checked_in", got)
	}
	if got := bookingQR(t, id); got != "" {
		t.Errorf("qr_token after check-in = %q, want it cleared", got)
	}
}

// TestVerifyBookingConcurrentRequestsOnlyOneWins reproduces the "prefetch
// races the real click" scenario: N goroutines verify the same token at
// once. Before the fix, every one of them could see status='pending' and each
// would generate and write its own qr_token, so the token actually emailed to
// the caller need not be the one left in the database.
func TestVerifyBookingConcurrentRequestsOnlyOneWins(t *testing.T) {
	raceTestDB(t)
	eventID := seedBookingEvent(t, time.Now(), time.Now().Add(time.Hour))
	const raw = "verify-race-token"
	if _, err := db.Exec(
		`INSERT INTO bookings (event_id, name, email, persons, status, verify_token, expires_at)
		 VALUES (?, 'Anna', 'anna@example.com', 1, 'pending', ?, ?)`,
		eventID, raw, time.Now().Add(time.Hour).Unix(),
	); err != nil {
		t.Fatal(err)
	}

	const attempts = 20
	var wg sync.WaitGroup
	var successes int32
	wonQRs := make([]string, attempts)
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		i := i
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/bookings/verify/"+raw, nil)
			r.SetPathValue("token", raw)
			rec := httptest.NewRecorder()
			verifyBooking(rec, r)
			if rec.Code == http.StatusOK {
				atomic.AddInt32(&successes, 1)
				var body struct {
					QRToken string `json:"qr_token"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err == nil {
					wonQRs[i] = body.QRToken
				}
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("%d/%d concurrent verifies succeeded, want exactly 1", successes, attempts)
	}
	var winner string
	for _, qr := range wonQRs {
		if qr != "" {
			winner = qr
		}
	}
	if winner == "" {
		t.Fatal("the one successful response carried no qr_token")
	}
	var dbQR string
	if err := db.QueryRow("SELECT qr_token FROM bookings WHERE event_id=?", eventID).Scan(&dbQR); err != nil {
		t.Fatal(err)
	}
	if dbQR != winner {
		t.Errorf("the token returned to the caller (%q) does not match the one stored (%q) — a loser's response would carry a dead token", winner, dbQR)
	}
}
