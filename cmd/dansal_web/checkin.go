package main

import (
	"net/http"
	"net/url"
	"strconv"
)

type CheckinData struct {
	QRToken string
	// result state (from redirect query params after POST)
	Done    bool // POST succeeded
	Name    string
	Persons string
	Status  string
	Err     bool // POST failed
	// session state
	CanCheckin bool // user is logged in (auth attempt will be made on POST)
}

// GET /checkin/{qr_token}
func checkinGetHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		qrToken := r.PathValue("qr_token")
		q := r.URL.Query()
		data := CheckinData{
			QRToken:    qrToken,
			Done:       q.Get("ok") == "1",
			Name:       q.Get("name"),
			Persons:    q.Get("persons"),
			Status:     q.Get("status"),
			Err:        q.Get("err") == "1",
			CanCheckin: getSessionUser(r) != nil,
		}
		title := i18n.T(r, "checkin_title")
		renderTemplate(w, tmpls.checkin, tmplData(r, cfg, i18n, title, data))
	}
}

// POST /checkin/{qr_token}
func checkinPostHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		qrToken := r.PathValue("qr_token")
		token := getSessionToken(r)

		booking, err := client.CheckinBooking(r.Context(), qrToken, token)
		if err != nil {
			http.Redirect(w, r, "/checkin/"+qrToken+"?err=1", http.StatusSeeOther)
			return
		}
		params := url.Values{}
		params.Set("ok", "1")
		params.Set("name", booking.Name)
		params.Set("persons", strconv.Itoa(booking.Persons))
		params.Set("status", booking.Status)
		http.Redirect(w, r, "/checkin/"+qrToken+"?"+params.Encode(), http.StatusSeeOther)
	}
}
