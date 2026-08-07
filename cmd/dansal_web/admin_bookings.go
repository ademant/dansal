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
func adminBookingApproveHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		bookingID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		if err := client.UpdateBookingStatus(r.Context(), bookingID, "approved", token); err != nil {
			log.Printf("approve booking %d: %v", bookingID, err)
		}
		eventID := r.FormValue("event_id")
		http.Redirect(w, r, "/admin/events/"+eventID+"/bookings", http.StatusSeeOther)
	}
}

// POST /admin/bookings/{id}/cancel
func adminBookingCancelHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		bookingID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		if err := client.UpdateBookingStatus(r.Context(), bookingID, "cancelled", token); err != nil {
			log.Printf("cancel booking %d: %v", bookingID, err)
		}
		eventID := r.FormValue("event_id")
		http.Redirect(w, r, "/admin/events/"+eventID+"/bookings", http.StatusSeeOther)
	}
}

// POST /admin/bookings/{id}/delete
func adminBookingDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		bookingID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		if err := client.DeleteBooking(r.Context(), bookingID, token); err != nil {
			log.Printf("delete booking %d: %v", bookingID, err)
		}
		eventID := r.FormValue("event_id")
		http.Redirect(w, r, "/admin/events/"+eventID+"/bookings", http.StatusSeeOther)
	}
}
