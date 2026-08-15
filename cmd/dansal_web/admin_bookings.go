package main

import (
	"log"
	"net/http"
	"strconv"
)

// userCanManageEvent returns true if su is admin or an org member of the event's organisation.
func userCanManageEvent(r *http.Request, su *SessionUser, event Event, client *DansalClient, token string) bool {
	if su.Role == "admin" {
		return true
	}
	if event.OrganizationID == nil {
		return false
	}
	members, err := client.GetOrganizationMembers(r.Context(), *event.OrganizationID, token)
	if err != nil {
		return false
	}
	for _, m := range members {
		if m.UserID == su.ID {
			return true
		}
	}
	return false
}

type AdminBookingsData struct {
	Event         Event
	Bookings      []Booking
	ApprovedCount int
}

// GET /admin/events/{id}/bookings
func adminBookingsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		event, err := client.GetEvent(r.Context(), eventID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !userCanManageEvent(r, su, event, client, token) {
			forbidden(w, r)
			return
		}
		bookings, err := client.GetBookings(r.Context(), eventID, token)
		if err != nil {
			http.Error(w, "could not load bookings", http.StatusBadGateway)
			return
		}
		approved := 0
		for _, b := range bookings {
			if b.Status == "approved" || b.Status == "checked_in" {
				approved++
			}
		}
		title := i18n.T(r, "admin_bookings_title")
		renderTemplate(w, tmpls.adminBookings, tmplData(r, cfg, i18n, title, AdminBookingsData{
			Event:         event,
			Bookings:      bookings,
			ApprovedCount: approved,
		}))
	}
}

// POST /admin/bookings/{id}/approve
// bookingMutationAuth resolves and re-verifies the event a booking mutation
// claims to act on: the caller must be able to manage that event, and
// bookingID must actually belong to it (the event_id form value is otherwise
// just an unverified redirect target). Applies userCanManageEvent, which
// previously only guarded the GET list handler in this file — see #1109.
func bookingMutationAuth(r *http.Request, su *SessionUser, bookingID int, client *DansalClient, token string) (eventIDStr string, ok bool) {
	eventIDStr = r.FormValue("event_id")
	eventID, err := strconv.Atoi(eventIDStr)
	if err != nil {
		return eventIDStr, false
	}
	event, err := client.GetEvent(r.Context(), eventID)
	if err != nil || !userCanManageEvent(r, su, event, client, token) {
		return eventIDStr, false
	}
	bookings, err := client.GetBookings(r.Context(), eventID, token)
	if err != nil {
		return eventIDStr, false
	}
	for _, b := range bookings {
		if b.ID == bookingID {
			return eventIDStr, true
		}
	}
	return eventIDStr, false
}

func adminBookingApproveHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		bookingID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		eventID, authOK := bookingMutationAuth(r, su, bookingID, client, token)
		if !authOK {
			forbidden(w, r)
			return
		}
		if err := client.UpdateBookingStatus(r.Context(), bookingID, "approved", token); err != nil {
			log.Printf("approve booking %d: %v", bookingID, err)
		}
		http.Redirect(w, r, "/admin/events/"+eventID+"/bookings", http.StatusSeeOther)
	}
}

// POST /admin/bookings/{id}/cancel
func adminBookingCancelHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		bookingID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		eventID, authOK := bookingMutationAuth(r, su, bookingID, client, token)
		if !authOK {
			forbidden(w, r)
			return
		}
		if err := client.UpdateBookingStatus(r.Context(), bookingID, "cancelled", token); err != nil {
			log.Printf("cancel booking %d: %v", bookingID, err)
		}
		http.Redirect(w, r, "/admin/events/"+eventID+"/bookings", http.StatusSeeOther)
	}
}

// POST /admin/bookings/{id}/delete
func adminBookingDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		bookingID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		eventID, authOK := bookingMutationAuth(r, su, bookingID, client, token)
		if !authOK {
			forbidden(w, r)
			return
		}
		if err := client.DeleteBooking(r.Context(), bookingID, token); err != nil {
			log.Printf("delete booking %d: %v", bookingID, err)
		}
		http.Redirect(w, r, "/admin/events/"+eventID+"/bookings", http.StatusSeeOther)
	}
}
