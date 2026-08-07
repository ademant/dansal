package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

// redirectBookingError redirects to the event page with a one-time flash
// (#985) carrying the generic book_error i18n key plus the API's actual
// message and error_id when available, using the same mechanism as
// redirectBoardError.
func redirectBookingError(w http.ResponseWriter, r *http.Request, eventID int, err error) {
	tok := flashToken(err)
	log.Printf("dansal-web: booking error error_id=%s path=%s err=%v", tok, r.URL.Path, err)
	flashRedirect(w, r, fmt.Sprintf("/events/%d", eventID), tok, FlashMsg{
		BookingError:    "book_error",
		BookingErrorMsg: apiErrUserMessage(err),
		BookingErrorID:  tok,
	})
}

// POST /events/{id}/book
func bookingPostHandler(cfg *Config, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		if publicThrottle.isBlocked(ip + "|" + r.UserAgent()) {
			log.Printf("%s ip=%s path=%s", publicBlock, ip, r.URL.Path)
			redirectBookingError(w, r, eventID, nil)
			return
		}
		if err := r.ParseForm(); err != nil {
			redirectBookingError(w, r, eventID, nil)
			return
		}
		if r.FormValue("honeypot") != "" {
			log.Printf("dansal-web: HONEYPOT ip=%s path=%s", ip, r.URL.Path)
			flashRedirect(w, r, fmt.Sprintf("/events/%d", eventID), flashToken(nil), FlashMsg{BookingOK: true})
			return
		}
		if !consumeFormToken(r.FormValue("_form_token"), ip, time.Second, stdFormMaxAge(cfg), cfg.FormTokenBindIP) {
			log.Printf("dansal-web: FORM_TOKEN_REJECT ip_hash=%s path=%s", hashIP(ip), r.URL.Path)
			redirectBookingError(w, r, eventID, nil)
			return
		}
		if hasPendingSubmission(ip, r.UserAgent()) {
			redirectBookingError(w, r, eventID, nil)
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

		publicThrottle.record(ip + "|" + r.UserAgent())
		setPendingSubmission(ip, r.UserAgent(), stdFormMaxAge(cfg))
		globalEmailSendRate.record()
		if err := client.CreateBooking(r.Context(), eventID, fields, cfg.publicBaseURL()); err != nil {
			clearPendingSubmission(ip, r.UserAgent())
			redirectBookingError(w, r, eventID, err)
			return
		}
		flashRedirect(w, r, fmt.Sprintf("/events/%d", eventID), flashToken(nil), FlashMsg{BookingOK: true})
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
