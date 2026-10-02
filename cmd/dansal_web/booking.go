package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
)

// redirectBookingError redirects to the event page with a one-time flash
// (#985) carrying the generic book_error i18n key plus the API's actual
// message and error_id when available, using the same mechanism as
// redirectBoardError.
func redirectBookingError(w http.ResponseWriter, r *http.Request, eventID int, err error) {
	redirectBookingErrorKey(w, r, eventID, "book_error", err)
}

// redirectBookingErrorKey is redirectBookingError with an explicit message
// key. #1422: it only logs when there is an API error — every web-side
// rejection already logged its own reason (PUBLIC_BLOCK / FORM_REJECT), so
// this no longer adds an uninformative "err=<nil>" line.
func redirectBookingErrorKey(w http.ResponseWriter, r *http.Request, eventID int, key string, err error) {
	tok := flashToken(err)
	if err != nil {
		log.Printf("dansal-web: booking error error_id=%s path=%s err=%v", tok, r.URL.Path, err)
	}
	flashRedirect(w, r, fmt.Sprintf("/events/%d", eventID), tok, FlashMsg{
		BookingError:    key,
		BookingErrorMsg: apiErrUserMessage(err),
		BookingErrorID:  tok,
	})
}

// POST /events/{id}/book
func bookingPostHandler(cfg *Config, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventID, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		ip := getClientIP(r)
		if publicThrottle.isBlocked(ip + "|" + r.UserAgent()) {
			log.Printf("%s ip=%s path=%s", publicBlock, ip, r.URL.Path)
			redirectBookingError(w, r, eventID, nil)
			return
		}
		switch guardFormSubmit(w, r, cfg, ip) {
		case formGuardParseError:
			redirectBookingError(w, r, eventID, nil)
			return
		case formGuardHoneypot:
			log.Printf("dansal-web: HONEYPOT ip=%s path=%s", ip, r.URL.Path)
			flashRedirect(w, r, fmt.Sprintf("/events/%d", eventID), flashToken(nil), FlashMsg{BookingOK: true})
			return
		case formGuardBadToken:
			redirectBookingError(w, r, eventID, nil)
			return
		}
		pendingScope := fmt.Sprintf("booking|%d", eventID)
		if hasPendingSubmission(ip, r.UserAgent(), pendingScope) {
			logFormReject(r, "PENDING_SUBMISSION", ip, nil)
			redirectBookingErrorKey(w, r, eventID, "form_error_pending", nil)
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
		setPendingSubmission(ip, r.UserAgent(), pendingScope, stdFormMaxAge(cfg))
		globalEmailSendRate.record()
		if err := client.CreateBooking(r.Context(), eventID, fields, cfg.publicBaseURL()); err != nil {
			clearPendingSubmission(ip, r.UserAgent(), pendingScope)
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
