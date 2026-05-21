package main

import (
	"fmt"
	"net/http"
	"strconv"

)

// POST /events/{id}/book
func bookingPostHandler(cfg *Config, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, fmt.Sprintf("/events/%d?book_error=book_error", eventID), http.StatusSeeOther)
			return
		}

		persons, _ := strconv.Atoi(r.FormValue("persons"))
		if persons < 1 {
			persons = 1
		}

		fields := map[string]any{
			"name":    r.FormValue("name"),
			"email":   r.FormValue("email"),
			"persons": persons,
			"message": r.FormValue("message"),
		}

		if err := client.CreateBooking(r.Context(), eventID, fields, cfg.publicBaseURL()); err != nil {
			http.Redirect(w, r, fmt.Sprintf("/events/%d?book_error=book_error", eventID), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/events/%d?book_ok=1", eventID), http.StatusSeeOther)
	}
}

// GET /bookings/verify/{token}
func bookingVerifyHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		result, err := client.VerifyBooking(r.Context(), token)

		title := i18n.T(r, "book_verify_title")
		if err != nil {
			renderTemplate(w, tmpls.bookingVerify, tmplData(r, cfg, i18n, title, BookingVerifyData{
				ErrorKey: "book_verify_error",
			}))
			return
		}
		renderTemplate(w, tmpls.bookingVerify, tmplData(r, cfg, i18n, title, BookingVerifyData{
			Success:    true,
			QRToken:    result.QRToken,
			CheckinURL: result.CheckinURL,
		}))
	}
}

type BookingVerifyData struct {
	Success    bool
	QRToken    string
	CheckinURL string
	ErrorKey   string
}
