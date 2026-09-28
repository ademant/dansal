package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// bookingTestDB points the package-level db at a fresh in-memory instance.
func bookingTestDB(t *testing.T) {
	t.Helper()
	old := db
	t.Cleanup(func() { db = old })

	conn, err := sql.Open("sqlite3", ":memory:")
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
	// The window margins live in config, so tests must use the same defaults
	// loadConfig would fill in.
	config.Server.CheckinOpensBeforeMinutes = 120
	config.Server.CheckinClosesAfterMinutes = 240
	t.Cleanup(func() { config = prev })
}

// seedBookingEvent creates an event whose window is centred on now, so a
// booking against it is inside the check-in window unless a test says else.
func seedBookingEvent(t *testing.T, start, end time.Time) int {
	t.Helper()
	res, err := db.Exec(
		"INSERT INTO events (title, start_time, end_time, is_published) VALUES ('Bal', ?, ?, 1)",
		itoa(start.Unix()), itoa(end.Unix()),
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func seedBooking(t *testing.T, eventID int, status, qr string) int {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO bookings (event_id, name, email, persons, status, qr_token, expires_at)
		 VALUES (?, 'Anna', 'anna@example.com', 1, ?, ?, ?)`,
		eventID, status, qr, time.Now().Add(90*24*time.Hour).Unix(),
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

func checkin(t *testing.T, qr string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/bookings/checkin/"+qr, nil)
	r.SetPathValue("qr_token", qr)
	// Staff scan at the door: admin role, so org membership is not needed.
	r = withCallerRole(r, RoleAdmin)
	rec := httptest.NewRecorder()
	checkinBooking(rec, r)
	return rec
}

// withCallerRole sets the headers TokenMiddleware would have set. Handlers read
// the caller through callerFromRequest, so tests can stand in for auth without
// minting a session token.
func withCallerRole(r *http.Request, role string) *http.Request {
	r.Header.Set("X-User-ID", "1")
	r.Header.Set("X-User-Role", role)
	return r
}

func bookingStatus(t *testing.T, id int) string {
	t.Helper()
	var s string
	if err := db.QueryRow("SELECT status FROM bookings WHERE id=?", id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func bookingQR(t *testing.T, id int) string {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRow("SELECT qr_token FROM bookings WHERE id=?", id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s.String
}

func TestCheckinSucceedsInsideWindow(t *testing.T) {
	bookingTestDB(t)
	now := time.Now()
	eventID := seedBookingEvent(t, now.Add(-time.Hour), now.Add(time.Hour))
	seedBooking(t, eventID, "confirmed", "qr-live")

	rec := checkin(t, "qr-live")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"checked_in"`) {
		t.Errorf("body %q does not report checked_in", rec.Body.String())
	}
}

// The QR is a photographed, public, physical-world credential: once used it
// must not work again, or a picture of the ticket is a replayable pass.
func TestCheckinTokenIsSingleUse(t *testing.T) {
	bookingTestDB(t)
	now := time.Now()
	eventID := seedBookingEvent(t, now.Add(-time.Hour), now.Add(time.Hour))
	id := seedBooking(t, eventID, "confirmed", "qr-once")

	if rec := checkin(t, "qr-once"); rec.Code != http.StatusOK {
		t.Fatalf("first scan: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := bookingQR(t, id); got != "" {
		t.Errorf("qr_token after check-in = %q, want it cleared", got)
	}
	if got := bookingStatus(t, id); got != "checked_in" {
		t.Errorf("status = %q, want checked_in", got)
	}

	rec := checkin(t, "qr-once")
	if rec.Code != http.StatusNotFound {
		t.Errorf("replay: got %d, want 404", rec.Code)
	}
}

// The old code never consulted expires_at on a confirmed booking, and cleanup
// only deletes status='pending' rows, so a QR stayed valid indefinitely.
func TestCheckinRejectedAfterEventWindow(t *testing.T) {
	bookingTestDB(t)
	now := time.Now()
	// Event ended 3 days ago: far outside checkinOpensBefore/ClosesAfter.
	eventID := seedBookingEvent(t, now.Add(-73*time.Hour), now.Add(-72*time.Hour))
	seedBooking(t, eventID, "confirmed", "qr-stale")

	rec := checkin(t, "qr-stale")
	if rec.Code != http.StatusGone {
		t.Errorf("got %d, want 410: %s", rec.Code, rec.Body.String())
	}
	if got := bookingStatus(t, mustBookingID(t, eventID)); got == "checked_in" {
		t.Error("a stale ticket was checked in")
	}
}

func TestCheckinTooEarly(t *testing.T) {
	bookingTestDB(t)
	now := time.Now()
	// Starts tomorrow, so the window has not opened yet.
	eventID := seedBookingEvent(t, now.Add(24*time.Hour), now.Add(26*time.Hour))
	seedBooking(t, eventID, "confirmed", "qr-early")

	rec := checkin(t, "qr-early")
	if rec.Code != http.StatusGone {
		t.Errorf("got %d, want 410: %s", rec.Code, rec.Body.String())
	}
}

func TestCheckinJustInsideWindowEdges(t *testing.T) {
	bookingTestDB(t)
	now := time.Now()
	// Starts in 1h, so we are inside the 2h pre-event window.
	eventID := seedBookingEvent(t, now.Add(time.Hour), now.Add(3*time.Hour))
	seedBooking(t, eventID, "confirmed", "qr-edge")

	if rec := checkin(t, "qr-edge"); rec.Code != http.StatusOK {
		t.Errorf("1h before start: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestCheckinJustAfterWindowCloses(t *testing.T) {
	bookingTestDB(t)
	now := time.Now()
	// Ended 5h ago, beyond the 4h grace.
	eventID := seedBookingEvent(t, now.Add(-8*time.Hour), now.Add(-5*time.Hour))
	seedBooking(t, eventID, "confirmed", "qr-late")

	rec := checkin(t, "qr-late")
	if rec.Code != http.StatusGone {
		t.Errorf("got %d, want 410: %s", rec.Code, rec.Body.String())
	}
}

// A withdrawn ticket used to return 200 with the booking details, so staff
// scanning it saw a plausible record rather than a refusal.
func TestCheckinRejectsCancelledAndPending(t *testing.T) {
	for _, status := range []string{"cancelled", "pending"} {
		t.Run(status, func(t *testing.T) {
			bookingTestDB(t)
			now := time.Now()
			eventID := seedBookingEvent(t, now.Add(-time.Hour), now.Add(time.Hour))
			seedBooking(t, eventID, status, "qr-"+status)

			rec := checkin(t, "qr-"+status)
			if rec.Code != http.StatusGone {
				t.Errorf("got %d, want 410: %s", rec.Code, rec.Body.String())
			}
			if got := bookingStatus(t, mustBookingID(t, eventID)); got == "checked_in" {
				t.Errorf("a %s booking was checked in", status)
			}
		})
	}
}

func mustBookingID(t *testing.T, eventID int) int {
	t.Helper()
	var id int
	if err := db.QueryRow("SELECT id FROM bookings WHERE event_id=?", eventID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBookingCheckinExpiryFallsBackWhenTimesUnset(t *testing.T) {
	bookingTestDB(t)
	now := time.Now()
	// No end_time: the window must still close, not stay open-ended.
	eventID := seedBookingEvent(t, now, now)
	if _, err := db.Exec("UPDATE events SET end_time='' WHERE id=?", eventID); err != nil {
		t.Fatal(err)
	}
	exp := bookingCheckinExpiry(eventID)
	if !exp.After(now) {
		t.Errorf("expiry %v is not after now; the credential would never close", exp)
	}
	if exp.After(now.Add(24 * time.Hour)) {
		t.Errorf("expiry %v is more than a day out; the fallback is not bounding anything", exp)
	}
}

func TestCheckinWindowDefaultsMatchConfig(t *testing.T) {
	// loadConfig fills these in; assert the shipped defaults still describe the
	// behaviour the issue documented.
	bookingTestDB(t)
	old := config
	t.Cleanup(func() { config = old })
	cfg := &Config{}
	applyDefaults(cfg)
	if got := cfg.Server.CheckinOpensBeforeMinutes; got != 120 {
		t.Errorf("checkin_opens_before_minutes default = %d, want 120", got)
	}
	if got := cfg.Server.CheckinClosesAfterMinutes; got != 240 {
		t.Errorf("checkin_closes_after_minutes default = %d, want 240", got)
	}
}

func TestCheckinWindowHonoursConfig(t *testing.T) {
	now := time.Now()

	t.Run("wider closing bound accepts a later scan", func(t *testing.T) {
		bookingTestDB(t)
		// Ended 5h ago: rejected at the 4h default, accepted at 12h.
		config.Server.CheckinClosesAfterMinutes = 12 * 60
		eventID := seedBookingEvent(t, now.Add(-8*time.Hour), now.Add(-5*time.Hour))
		seedBooking(t, eventID, "confirmed", "qr-wide")

		if rec := checkin(t, "qr-wide"); rec.Code != http.StatusOK {
			t.Errorf("got %d, want 200 with a 12h closing bound: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("narrower opening bound refuses an early scan", func(t *testing.T) {
		bookingTestDB(t)
		// Starts in 1h: accepted at the 2h default, refused when the window is 30m.
		config.Server.CheckinOpensBeforeMinutes = 30
		eventID := seedBookingEvent(t, now.Add(time.Hour), now.Add(3*time.Hour))
		seedBooking(t, eventID, "confirmed", "qr-narrow")

		if rec := checkin(t, "qr-narrow"); rec.Code != http.StatusGone {
			t.Errorf("got %d, want 410 with a 30m opening bound: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("negative closing bound still bounds a schedule-less event", func(t *testing.T) {
		bookingTestDB(t)
		config.Server.CheckinClosesAfterMinutes = -1
		eventID := seedBookingEvent(t, now, now)
		if _, err := db.Exec("UPDATE events SET end_time='' WHERE id=?", eventID); err != nil {
			t.Fatal(err)
		}
		exp := bookingCheckinExpiry(eventID)
		if !exp.After(now) {
			t.Errorf("expiry %v is not after now", exp)
		}
		if exp.After(now.Add(24 * time.Hour)) {
			t.Errorf("expiry %v is unbounded in practice", exp)
		}
	})

	t.Run("negative opening bound disables the early refusal", func(t *testing.T) {
		bookingTestDB(t)
		config.Server.CheckinOpensBeforeMinutes = -1
		eventID := seedBookingEvent(t, now.Add(48*time.Hour), now.Add(50*time.Hour))
		seedBooking(t, eventID, "confirmed", "qr-noopen")

		if got := bookingCheckinOpens(eventID); !got.IsZero() {
			t.Errorf("bookingCheckinOpens = %v, want zero to disable the check", got)
		}
	})
}
