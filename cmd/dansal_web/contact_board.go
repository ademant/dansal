package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// POST /events/{id}/board
func contactBoardPostHandler(cfg *Config, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
			log.Printf("dansal-web: HONEYPOT ip_hash=%s path=%s", hashIP(ip), r.URL.Path)
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_posted=1", eventID), http.StatusSeeOther)
			return
		}
		if !consumeFormToken(r.FormValue("_form_token"), ip, cfg.FormTokenMaxAgeMins, cfg.FormTokenBindIP) {
			log.Printf("dansal-web: FORM_TOKEN_REJECT ip_hash=%s path=%s", hashIP(ip), r.URL.Path)
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_error=board_form_error", eventID), http.StatusSeeOther)
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

		tgURL, firstPost, err := client.CreateContactPost(r.Context(), eventID, post, cfg.publicBaseURL(), getSessionToken(r))
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_error=board_post_error", eventID), http.StatusSeeOther)
			return
		}
		publicThrottle.record(ip + "|" + r.UserAgent())
		if firstPost {
			go triggerBoardOpenNote(cfg, db, client, eventID)
		}
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
			log.Printf("dansal-web: HONEYPOT ip_hash=%s path=%s", hashIP(ip), r.URL.Path)
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_contacted=1", eventID), http.StatusSeeOther)
			return
		}
		if !consumeFormToken(r.FormValue("_form_token"), ip, cfg.FormTokenMaxAgeMins, cfg.FormTokenBindIP) {
			log.Printf("dansal-web: FORM_TOKEN_REJECT ip_hash=%s path=%s", hashIP(ip), r.URL.Path)
			http.Redirect(w, r, fmt.Sprintf("/events/%d?board_error=board_form_error", eventID), http.StatusSeeOther)
			return
		}

		email := r.FormValue("email")
		telegram := r.FormValue("telegram")
		message := r.FormValue("message")

		tgURL, err := client.ContactPoster(r.Context(), postID, email, telegram, message, cfg.publicBaseURL(), getSessionToken(r))
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

// GET /contact-posts/verify/{token}
// Backward-compat redirect — old verification emails pointed here; now folds into manage page.
func contactBoardVerifyRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/contact-posts/manage/"+r.PathValue("token"), http.StatusMovedPermanently)
}

// GET /contact-posts/delete/{token}
// Backward-compat redirect — old delete emails pointed here; manage_token was backfilled from delete_token.
func contactBoardDeleteRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/contact-posts/manage/"+r.PathValue("token"), http.StatusMovedPermanently)
}

// ContactManageData is passed to the contact_manage template.
type ContactManageData struct {
	Token     string
	Post      ContactManageResult
	FormToken string
	Updated   bool
	Deleted   bool
	NotFound  bool
}

// GET /contact-posts/manage/{token}
func contactManageGetHandler(cfg *Config, db *sql.DB, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		title := i18n.T(r, "board_manage_title")

		post, err := client.GetContactPostByToken(r.Context(), token)
		if err != nil {
			renderTemplate(w, tmpls.contactManage, tmplData(r, cfg, i18n, title,
				ContactManageData{Token: token, NotFound: true}))
			return
		}
		if post.JustVerified && post.FirstPost {
			go triggerBoardOpenNote(cfg, db, client, post.EventID)
		}

		ip := getClientIP(r)
		data := ContactManageData{
			Token:     token,
			Post:      post,
			FormToken: issueFormToken(ip),
			Updated:   r.URL.Query().Get("updated") == "1",
			Deleted:   r.URL.Query().Get("deleted") == "1",
		}
		renderTemplate(w, tmpls.contactManage, tmplData(r, cfg, i18n, title, data))
	}
}

// POST /contact-posts/manage/{token}
func contactManagePostHandler(cfg *Config, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}
		ip := getClientIP(r)
		if !consumeFormToken(r.FormValue("_form_token"), ip, cfg.FormTokenMaxAgeMins, cfg.FormTokenBindIP) {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}

		if r.FormValue("_action") == "delete" {
			if err := client.DeleteContactPostByManageToken(r.Context(), token); err != nil {
				http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/contact-posts/manage/"+token+"?deleted=1", http.StatusSeeOther)
			return
		}

		// Edit action: read post ID from hidden field, then PATCH.
		postID, err := strconv.Atoi(r.FormValue("post_id"))
		if err != nil {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}
		persons, _ := strconv.Atoi(r.FormValue("persons"))
		if persons < 1 {
			persons = 1
		}
		fields := map[string]any{
			"type":     r.FormValue("type"),
			"city":     r.FormValue("city"),
			"persons":  persons,
			"message":  r.FormValue("message"),
			"nickname": r.FormValue("nickname"),
		}
		if err := client.UpdateContactPost(r.Context(), postID, token, fields); err != nil {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/contact-posts/manage/"+token+"?updated=1", http.StatusSeeOther)
	}
}

// triggerBoardOpenNote fetches the event from the API and delivers the AP Note
// to the org's followers. Runs in a goroutine; logs and returns silently on error.
func triggerBoardOpenNote(cfg *Config, db *sql.DB, client *DansalClient, eventID int) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	event, err := client.GetEvent(ctx, eventID)
	if err != nil || event.OrganizationID == nil {
		return
	}
	deliverBoardOpenNote(cfg, db, *event.OrganizationID, eventID, event.Title)
}
