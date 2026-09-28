package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Booking struct {
	ID        int    `json:"id"`
	EventID   int    `json:"event_id"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	Persons   int    `json:"persons"`
	Message   string `json:"message,omitempty"`
	Status    string `json:"status"`
	QRToken   string `json:"qr_token,omitempty"`
	CreatedAt string `json:"created_at"`
}

// updateEventAvailability recalculates events.availability from approved/checked_in booking counts.
// Skipped when tickets_total is 0 (no capacity configured).
func updateEventAvailability(eventID int) {
	var ticketsTotal int
	if err := db.QueryRow("SELECT COALESCE(tickets_total,0) FROM events WHERE id=?", eventID).Scan(&ticketsTotal); err != nil || ticketsTotal <= 0 {
		return
	}
	var approvedCount int
	db.QueryRow(
		"SELECT COUNT(*) FROM bookings WHERE event_id=? AND status IN ('approved','checked_in')",
		eventID,
	).Scan(&approvedCount)

	var avail string
	switch {
	case approvedCount >= ticketsTotal:
		avail = "sold_out"
	case approvedCount*2 >= ticketsTotal:
		avail = "limited"
	}
	db.Exec("UPDATE events SET availability=? WHERE id=?", avail, eventID)
	log.Printf("bookings: event %d availability → %q (%d/%d)", eventID, avail, approvedCount, ticketsTotal)
}

// bookingVerifyExpiry returns now + VerificationExpiryHours.
func bookingVerifyExpiry() time.Time {
	h := config.Server.VerificationExpiryHours
	if h <= 0 {
		h = 24
	}
	return time.Now().UTC().Add(time.Duration(h) * time.Hour)
}

// bookingFallbackSpan is the window used when an event has no usable end time,
// so a ticket without a schedule is still bounded rather than open-ended.
const bookingFallbackSpan = 6 * time.Hour

// checkinOpensBefore / checkinClosesAfter resolve the configurable margins
// around the event. A negative configured value disables that side of the
// window; the zero case cannot reach here because loadConfig replaces it with
// the default.
func checkinOpensBefore() time.Duration {
	return time.Duration(config.Server.CheckinOpensBeforeMinutes) * time.Minute
}

func checkinClosesAfter() time.Duration {
	return time.Duration(config.Server.CheckinClosesAfterMinutes) * time.Minute
}

// bookingCheckinExpiry returns the instant after which the QR code for
// eventID stops working, derived from the event's own start and end times. A
// missing or unparseable time falls back to start+bookingFallbackSpan, which
// still bounds the credential rather than leaving it open-ended.
func bookingCheckinExpiry(eventID int) time.Time {
	var startStr, endStr string
	err := db.QueryRow("SELECT start_time, end_time FROM events WHERE id=?", eventID).Scan(&startStr, &endStr)
	if err != nil {
		return time.Now().UTC().Add(bookingFallbackSpan)
	}
	parse := func(s string) (time.Time, bool) {
		ts, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(ts, 0).UTC(), true
	}
	grace := checkinClosesAfter()
	if grace < 0 {
		// Configured as unbounded; a ticket with no schedule still gets a
		// finite life rather than no limit at all.
		if end, ok := parse(endStr); ok {
			return end.Add(bookingFallbackSpan)
		}
		return time.Now().UTC().Add(bookingFallbackSpan)
	}
	end, endOK := parse(endStr)
	if !endOK {
		if start, ok := parse(startStr); ok {
			return start.Add(bookingFallbackSpan).Add(grace)
		}
		return time.Now().UTC().Add(bookingFallbackSpan)
	}
	return end.Add(grace)
}

// bookingCheckinOpens returns the instant before which the QR code for eventID
// may not be scanned yet, so a ticket cannot be checked in days ahead.
func bookingCheckinOpens(eventID int) time.Time {
	if checkinOpensBefore() < 0 {
		return time.Time{}
	}
	var startStr string
	if err := db.QueryRow("SELECT start_time FROM events WHERE id=?", eventID).Scan(&startStr); err != nil {
		return time.Time{}
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(startStr), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(ts, 0).UTC().Add(-checkinOpensBefore())
}

// bookingLongExpiry returns the event's end_time + 90 days (for confirmed
// bookings). This is the retention deadline for the booking row and the
// deadline for its email verification link; it deliberately says nothing about
// when the QR code may be scanned — see bookingCheckinExpiry.
func bookingLongExpiry(eventID int) time.Time {
	var endTimeStr string
	if err := db.QueryRow("SELECT end_time FROM events WHERE id=?", eventID).Scan(&endTimeStr); err == nil {
		if ts, err := strconv.ParseInt(strings.TrimSpace(endTimeStr), 10, 64); err == nil {
			return time.Unix(ts, 0).UTC().Add(90 * 24 * time.Hour)
		}
	}
	return time.Now().UTC().Add(90 * 24 * time.Hour)
}

// bookingAuthCheck fetches the event_id for a booking and verifies the caller
// is admin or org member. Returns (eventID, ok).
func bookingAuthCheck(w http.ResponseWriter, bookingID, callerID int, callerRole string) (int, bool) {
	var eventID int
	err := db.QueryRow("SELECT event_id FROM bookings WHERE id=?", bookingID).Scan(&eventID)
	if err == sql.ErrNoRows {
		writeError(w, "booking not found", http.StatusNotFound)
		return 0, false
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return 0, false
	}
	if callerRole != RoleAdmin && !isOrgMemberOfEvent(callerID, eventID) {
		writeError(w, "forbidden", http.StatusForbidden)
		return 0, false
	}
	return eventID, true
}

// GET /api/v1/events/{id}/bookings
// Requires auth. Org member or admin only.
func listBookings(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	eventID, ok := requireIntPathValue(w, r, "id", "invalid event id")
	if !ok {
		return
	}
	if callerRole != RoleAdmin && !isOrgMemberOfEvent(callerID, eventID) {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	rows, err := db.Query(
		`SELECT id, event_id, name, email, persons, COALESCE(message,''), status, COALESCE(qr_token,''), created_at
		 FROM bookings WHERE event_id=? ORDER BY created_at ASC`, eventID,
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	out := []Booking{}
	for rows.Next() {
		var b Booking
		if err := rows.Scan(&b.ID, &b.EventID, &b.Name, &b.Email, &b.Persons, &b.Message, &b.Status, &b.QRToken, &b.CreatedAt); err != nil {
			writeInternalError(w, err)
			return
		}
		out = append(out, b)
	}
	writeJSON(w, out)
}

// POST /api/v1/events/{id}/bookings
// Public. Creates a pending booking and sends an email verification link.
func createBooking(w http.ResponseWriter, r *http.Request) {
	eventID, ok := requireIntPathValue(w, r, "id", "invalid event id")
	if !ok {
		return
	}

	// Check event exists and booking is enabled.
	var bookingEnabled int
	err := db.QueryRow("SELECT COALESCE(booking_enabled,0) FROM events WHERE id=?", eventID).Scan(&bookingEnabled)
	if err == sql.ErrNoRows {
		writeError(w, "event not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if bookingEnabled == 0 {
		writeError(w, "booking is not enabled for this event", http.StatusForbidden)
		return
	}

	var req struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Persons int    `json:"persons"`
		Message string `json:"message"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)
	req.Message = strings.TrimSpace(req.Message)
	if req.Name == "" || req.Email == "" {
		writeError(w, "name and email are required", http.StatusBadRequest)
		return
	}
	if !isValidEmail(req.Email) {
		writeError(w, "invalid email address", http.StatusBadRequest)
		return
	}
	if looksLikeGmailDotSpam(req.Email) {
		writeError(w, "invalid email address", http.StatusUnprocessableEntity)
		return
	}
	{
		var open int
		db.QueryRow(
			"SELECT COUNT(*) FROM bookings WHERE LOWER(email)=LOWER(?) AND status='pending' AND expires_at>strftime('%s','now')",
			req.Email,
		).Scan(&open)
		if open >= config.Server.MaxOpenTokensPerAddress {
			writeError(w, "Too many pending verifications for this address. Please complete or expire existing ones first.", http.StatusTooManyRequests)
			return
		}
	}
	if req.Persons < 1 {
		req.Persons = 1
	}

	verifyToken, err := generateVerificationToken()
	if err != nil {
		writeError(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	expiresAt := bookingVerifyExpiry()

	lang := bookingLangFromRequest(r)

	result, err := db.Exec(
		`INSERT INTO bookings (event_id, name, email, persons, message, verify_token, expires_at, lang)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, req.Name, req.Email, req.Persons, req.Message, verifyToken, expiresAt.Unix(), lang,
	)
	if err != nil {
		writeError(w, "failed to create booking", http.StatusInternalServerError)
		return
	}
	id, _ := result.LastInsertId()

	s := bookingMailStringsFor(lang)
	base := buildBaseURL(r)
	verifyURL := base + "/api/v1/bookings/verify/" + verifyToken
	verifyBody := fmt.Sprintf(s.VerifyBody, req.Name, verifyURL, config.Server.VerificationExpiryHours)
	go func() {
		if _, err := SendEmail(req.Email, s.VerifySubject, verifyBody, false); err != nil {
			log.Printf("bookings: verify email failed for booking %d: %v", id, err)
		}
	}()

	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"id":      id,
		"message": "A confirmation email has been sent. Your booking will be registered once verified.",
	})
}

// GET /api/v1/bookings/verify/{token}
// Public. Marks the booking as confirmed and generates a QR token.
// GET /api/v1/bookings/verify/{token}
// POST /api/v1/bookings/verify   (Authorization: Bearer <token> or {"token": …})
//
// The path form is kept because the verify token arrives as an emailed
// click-through link, so for that caller the URL genuinely is the credential.
// The POST form exists so an app-driven caller never has to place a
// single-use credential in a path segment, where it lands in access logs
// (#1382). Both forms consume exactly one token, identically.
func verifyBooking(w http.ResponseWriter, r *http.Request) {
	noStoreTokenResponse(w)

	token := verifyTokenFromRequest(r)
	if token == "" {
		writeError(w, "token required", http.StatusBadRequest)
		return
	}

	var id, eventID int
	var expiresAt, name, email, lang string
	err := db.QueryRow(
		"SELECT id, event_id, expires_at, name, email, COALESCE(lang,'') FROM bookings WHERE verify_token=? AND status='pending'", token,
	).Scan(&id, &eventID, &expiresAt, &name, &email, &lang)
	if err == sql.ErrNoRows {
		writeError(w, "invalid or already used verification link", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	exp, err := parseTokenExpiration(expiresAt)
	if err != nil || time.Now().After(exp) {
		db.Exec("DELETE FROM bookings WHERE id=?", id)
		writeError(w, "verification link has expired", http.StatusGone)
		return
	}

	qrToken, err := generateVerificationToken()
	if err != nil {
		writeError(w, "failed to generate QR token", http.StatusInternalServerError)
		return
	}

	longExpiry := bookingLongExpiry(eventID)
	db.Exec(
		"UPDATE bookings SET status='confirmed', verify_token=NULL, qr_token=?, expires_at=? WHERE id=?",
		qrToken, longExpiry.Unix(), id,
	)
	log.Printf("bookings: verified booking %d for event %d", id, eventID)
	go sendBookingConfirmedEmail(name, email, lang, eventID, qrToken)

	checkinURL := buildBaseURL(r) + "/checkin/" + qrToken

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":      "confirmed",
		"qr_token":    qrToken,
		"checkin_url": checkinURL,
	})
}

// PATCH /api/v1/bookings/{id}/status
// Requires auth. Org member or admin only. Accepts {"status":"approved"|"cancelled"}.
func updateBookingStatus(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	bookingID, ok := requireIntPathValue(w, r, "id", "invalid booking id")
	if !ok {
		return
	}

	eventID, ok := bookingAuthCheck(w, bookingID, callerID, callerRole)
	if !ok {
		return
	}

	var req struct {
		Status string `json:"status"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Status != "approved" && req.Status != "cancelled" {
		writeError(w, "status must be 'approved' or 'cancelled'", http.StatusBadRequest)
		return
	}

	res, err := db.Exec("UPDATE bookings SET status=? WHERE id=?", req.Status, bookingID)
	if err != nil {
		writeError(w, "failed to update booking", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, "booking not found", http.StatusNotFound)
		return
	}
	log.Printf("bookings: booking %d set to %s by user %d", bookingID, req.Status, callerID)
	updateEventAvailability(eventID)
	if req.Status == "approved" {
		var name, email, lang, qrToken string
		if err := db.QueryRow(
			"SELECT name, email, COALESCE(lang,''), COALESCE(qr_token,'') FROM bookings WHERE id=?", bookingID,
		).Scan(&name, &email, &lang, &qrToken); err == nil && qrToken != "" {
			go sendBookingApprovedEmail(name, email, lang, eventID, qrToken)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/bookings/checkin/{qr_token}
// Requires auth. Org member or admin only. Returns booking details and marks as checked_in.
//
// Note that the QR token is a lookup key here, not the credential: the route is
// already wrapped in auth() and the caller supplies its own bearer. It is
// deliberately *not* accepted as an authenticator, even though a QR code is
// scanned in public and the value therefore does end up in shared logs
// (#1382). Letting it stand in for a session would turn one scanned sticker
// into a checkin capability for anyone holding it.
func checkinBooking(w http.ResponseWriter, r *http.Request) {
	noStoreTokenResponse(w)

	callerID, callerRole := callerFromRequest(r)

	qrToken := r.PathValue("qr_token")

	var b Booking
	err := db.QueryRow(
		`SELECT id, event_id, name, persons, COALESCE(message,''), status, created_at
		 FROM bookings WHERE qr_token=?`, qrToken,
	).Scan(&b.ID, &b.EventID, &b.Name, &b.Persons, &b.Message, &b.Status, &b.CreatedAt)
	if err == sql.ErrNoRows {
		writeError(w, "booking not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if callerRole != RoleAdmin && !isOrgMemberOfEvent(callerID, b.EventID) {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	// A cancelled booking has no live ticket. This used to fall through and
	// return 200 with the booking details, so staff scanning a withdrawn
	// ticket saw a plausible-looking record instead of a refusal.
	if b.Status == "cancelled" {
		writeError(w, "booking is cancelled", http.StatusGone)
		return
	}
	if b.Status == "pending" {
		writeError(w, "booking is not confirmed yet", http.StatusGone)
		return
	}

	// Bound the QR code to a short window around the event. Previously the
	// token was never checked at all: expires_at is only consulted for
	// status='pending' rows, and cleanup only deletes 'pending' rows too, so a
	// confirmed QR stayed valid indefinitely however long ago its event was.
	if exp := bookingCheckinExpiry(b.EventID); time.Now().After(exp) {
		writeError(w, "this ticket is past its check-in window", http.StatusGone)
		return
	}
	if opens := bookingCheckinOpens(b.EventID); !opens.IsZero() && time.Now().Before(opens) {
		writeError(w, "check-in for this event has not opened yet", http.StatusGone)
		return
	}

	if b.Status == "approved" || b.Status == "confirmed" {
		// Single-use: drop the QR token in the same statement that records the
		// check-in, so a photographed ticket cannot be replayed at the door.
		// Checked in by anyone with a photo of the code is exactly the abuse
		// this closes.
		if _, err := db.Exec(
			"UPDATE bookings SET status='checked_in', qr_token=NULL WHERE id=?", b.ID,
		); err != nil {
			writeError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		b.Status = "checked_in"
		b.QRToken = ""
		log.Printf("bookings: booking %d checked in by user %d", b.ID, callerID)
		updateEventAvailability(b.EventID)
	}

	writeJSON(w, b)
}

// DELETE /api/v1/bookings/{id}
// Requires auth. Org member or admin only.
func deleteBooking(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	bookingID, ok := requireIntPathValue(w, r, "id", "invalid booking id")
	if !ok {
		return
	}

	eventID, ok := bookingAuthCheck(w, bookingID, callerID, callerRole)
	if !ok {
		return
	}

	db.Exec("DELETE FROM bookings WHERE id=?", bookingID)
	log.Printf("bookings: booking %d deleted by user %d", bookingID, callerID)
	updateEventAvailability(eventID)
	w.WriteHeader(http.StatusNoContent)
}
