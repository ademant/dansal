package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
)

// POST /events/{id}/board
func contactBoardPostHandler(cfg *Config, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		if publicThrottle.isBlocked(ip + "|" + r.UserAgent()) {
			log.Printf("%s ip=%s path=%s", publicBlock, ip, r.URL.Path)
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_error=board_throttled", eventID), http.StatusSeeOther)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_error=board_form_error", eventID), http.StatusSeeOther)
			return
		}
		if r.FormValue("honeypot") != "" {
			log.Printf("dansal-web: HONEYPOT ip=%s path=%s", ip, r.URL.Path)
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_posted=1", eventID), http.StatusSeeOther)
			return
		}

		persons, _ := strconv.Atoi(r.FormValue("persons"))
		if persons < 1 {
			persons = 1
		}

		post := map[string]any{
			"type":     r.FormValue("type"),
			"city":     r.FormValue("city"),
			"persons":  persons,
			"message":  r.FormValue("message"),
			"nickname": r.FormValue("nickname"),
			"email":    r.FormValue("email"),
			"telegram": r.FormValue("telegram"),
		}

		tgURL, err := client.CreateContactPost(r.Context(), eventID, post, cfg.publicBaseURL())
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_error=board_post_error", eventID), http.StatusSeeOther)
			return
		}
		publicThrottle.record(ip + "|" + r.UserAgent())
		if tgURL != "" {
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_posted=1&board_tg_url=%s", eventID, url.QueryEscape(tgURL)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/events/%d?board_posted=1", eventID), http.StatusSeeOther)
	}
}

// POST /events/{id}/board/{post_id}/delete
func contactBoardDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		postID, err := strconv.Atoi(r.PathValue("post_id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		_ = su
		token := getSessionToken(r)

		if err := client.DeleteContactPost(r.Context(), postID, token); err != nil {
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_error=board_delete_error", eventID), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/events/%d", eventID), http.StatusSeeOther)
	}
}

// POST /events/{id}/board/{post_id}/contact
func contactBoardContactHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		postID, err := strconv.Atoi(r.PathValue("post_id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		if publicThrottle.isBlocked(ip + "|" + r.UserAgent()) {
			log.Printf("%s ip=%s path=%s", publicBlock, ip, r.URL.Path)
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_error=board_throttled", eventID), http.StatusSeeOther)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_error=board_form_error", eventID), http.StatusSeeOther)
			return
		}
		if r.FormValue("honeypot") != "" {
			log.Printf("dansal-web: HONEYPOT ip=%s path=%s", ip, r.URL.Path)
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_contacted=1", eventID), http.StatusSeeOther)
			return
		}

		email := r.FormValue("email")
		telegram := r.FormValue("telegram")
		message := r.FormValue("message")

		tgURL, err := client.ContactPoster(r.Context(), postID, email, telegram, message, cfg.publicBaseURL())
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_error=board_contact_error", eventID), http.StatusSeeOther)
			return
		}
		publicThrottle.record(ip + "|" + r.UserAgent())
		if tgURL != "" {
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_contacted=1&board_contact_tg_url=%s", eventID, url.QueryEscape(tgURL)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/events/%d?board_contacted=1", eventID), http.StatusSeeOther)
	}
}

// GET /contact-requests/verify/{token}
func contactRequestVerifyHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		if err := client.VerifyContactRequest(r.Context(), token); err != nil {
			title := i18n.T(r, "verify_title")
			renderTemplate(w, tmpls.verify, tmplData(r, cfg, i18n, title, VerifyData{ErrorKey: "verify_error_invalid"}))
			return
		}
		title := i18n.T(r, "board_request_verify_title")
		renderTemplate(w, tmpls.verify, tmplData(r, cfg, i18n, title, VerifyData{Success: true, ContactRequestVerify: true}))
	}
}

// GET /contact-posts/delete/{token}
func contactBoardDeleteByTokenHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		if err := client.DeleteContactPostByToken(r.Context(), token); err != nil {
			title := i18n.T(r, "board_delete_title")
			renderTemplate(w, tmpls.verify, tmplData(r, cfg, i18n, title, VerifyData{ErrorKey: "board_delete_link_error"}))
			return
		}
		title := i18n.T(r, "board_delete_title")
		renderTemplate(w, tmpls.verify, tmplData(r, cfg, i18n, title, VerifyData{Success: true, BoardDelete: true}))
	}
}

// GET /contact-posts/verify/{token}
func contactBoardVerifyHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		if err := client.VerifyContactPost(r.Context(), token); err != nil {
			title := i18n.T(r, "verify_title")
			renderTemplate(w, tmpls.verify, tmplData(r, cfg, i18n, title, VerifyData{ErrorKey: "verify_error_invalid"}))
			return
		}
		title := i18n.T(r, "board_verify_title")
		renderTemplate(w, tmpls.verify, tmplData(r, cfg, i18n, title, VerifyData{Success: true, BoardVerify: true}))
	}
}
