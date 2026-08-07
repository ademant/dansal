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

// bookingLongExpiry returns the event's end_time + 90 days (for confirmed bookings).
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

	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// POST /api/v1/events/{id}/bookings
// Public. Creates a pending booking and sends an email verification link.
func createBooking(w http.ResponseWriter, r *http.Request) {
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}

	// Check event exists and booking is enabled.
	var bookingEnabled int
	err = db.QueryRow("SELECT COALESCE(booking_enabled,0) FROM events WHERE id=?", eventID).Scan(&bookingEnabled)
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"id":      id,
		"message": "A confirmation email has been sent. Your booking will be registered once verified.",
	})
}

// GET /api/v1/bookings/verify/{token}
// Public. Marks the booking as confirmed and generates a QR token.
func verifyBooking(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

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

	bookingID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid booking id", http.StatusBadRequest)
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
func checkinBooking(w http.ResponseWriter, r *http.Request) {
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

	if b.Status == "approved" || b.Status == "confirmed" {
		db.Exec("UPDATE bookings SET status='checked_in' WHERE id=?", b.ID)
		b.Status = "checked_in"
		log.Printf("bookings: booking %d checked in by user %d", b.ID, callerID)
		updateEventAvailability(b.EventID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(b)
}

// DELETE /api/v1/bookings/{id}
// Requires auth. Org member or admin only.
func deleteBooking(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	bookingID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid booking id", http.StatusBadRequest)
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
