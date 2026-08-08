package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

// redirectBoardError redirects to the event page with a one-time flash
// (#985) carrying both the generic board_error i18n key (fallback, always
// present) and the API's actual validation message when available (#973) —
// e.g. "message must not contain links" instead of just "Could not save
// post." The flash is keyed by the API's error_id when err carries one, so a
// URL a user shares with an admin correlates directly with a specific API
// log line; reopening the same URL later (bookmark, history) then renders a
// clean page instead of the error banner forever.
func redirectBoardError(w http.ResponseWriter, r *http.Request, eventID int, key string, err error) {
	tok := flashToken(err)
	log.Printf("dansal-web: board error error_id=%s key=%s path=%s err=%v", tok, key, r.URL.Path, err)
	flashRedirect(w, r, fmt.Sprintf("/events/%d", eventID), tok, FlashMsg{
		BoardError:    key,
		BoardErrorMsg: apiErrUserMessage(err),
		BoardErrorID:  tok,
	})
}

// boardErrorRedirect is redirectBoardError for failures the web caught
// itself (form parsing, throttling) with no API error to correlate against —
// still gets a local token so the message and its error_id both flow through
// the same one-time flash mechanism.
func boardErrorRedirect(w http.ResponseWriter, r *http.Request, eventID int, key string) {
	redirectBoardError(w, r, eventID, key, nil)
}

// boardSuccessRedirect stores msg as a one-time success flash and redirects
// to the event page (#985) — same mechanism as the error path, just without
// an error_id.
func boardSuccessRedirect(w http.ResponseWriter, r *http.Request, eventID int, msg FlashMsg) {
	flashRedirect(w, r, fmt.Sprintf("/events/%d", eventID), flashToken(nil), msg)
}

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
			boardErrorRedirect(w, r, eventID, "board_throttled")
			return
		}
		switch guardFormSubmit(w, r, cfg, ip) {
		case formGuardParseError:
			boardErrorRedirect(w, r, eventID, "board_form_error")
			return
		case formGuardHoneypot:
			log.Printf("dansal-web: HONEYPOT ip=%s path=%s", ip, r.URL.Path)
			boardSuccessRedirect(w, r, eventID, FlashMsg{BoardPosted: true})
			return
		case formGuardBadToken:
			log.Printf("dansal-web: FORM_TOKEN_REJECT ip_hash=%s path=%s", hashIP(ip), r.URL.Path)
			boardErrorRedirect(w, r, eventID, "board_form_error")
			return
		}
		if hasPendingSubmission(ip, r.UserAgent()) {
			boardErrorRedirect(w, r, eventID, "board_throttled")
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
		// osm_id (#1041) is display-only for now — populated by the Nominatim
		// city search, left unset if the visitor typed the city freehand.
		if osmID, err := strconv.ParseInt(r.FormValue("osm_id"), 10, 64); err == nil {
			post["osm_id"] = osmID
		}

		publicThrottle.record(ip + "|" + r.UserAgent())
		setPendingSubmission(ip, r.UserAgent(), stdFormMaxAge(cfg))
		globalEmailSendRate.record()
		tgURL, firstPost, err := client.CreateContactPost(r.Context(), eventID, post, cfg.publicBaseURL(), getSessionToken(r))
		if err != nil {
			log.Printf("dansal-web: board post failed ip_hash=%s path=%s type=%q city=%q message_len=%d err=%v",
				hashIP(ip), r.URL.Path, r.FormValue("type"), r.FormValue("city"), len(r.FormValue("message")), err)
			clearPendingSubmission(ip, r.UserAgent())
			redirectBoardError(w, r, eventID, "board_post_error", err)
			return
		}
		if firstPost {
			go triggerBoardOpenNote(cfg, db, client, eventID)
		}
		boardSuccessRedirect(w, r, eventID, FlashMsg{BoardPosted: true, BoardTelegramURL: tgURL})
	}
}

// GET /events/{id}/board/form-token — issues a fresh one-time form token for
// the board-post panel (#979). The page-rendered token is embedded at page
// load and can expire (max age commonly 5 min) before a visitor who reads
// the event first gets around to opening the "add a post" panel and filling
// it out; the panel's toggle handler calls this to swap in a live token
// right when it opens, without a full page reload.
func boardFormTokenHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := strconv.Atoi(r.PathValue("id")); err != nil {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		if tokenThrottle.isBlocked(ip) {
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		tokenThrottle.record(ip)

		tok := issueFormToken(ip)
		if tok == "" {
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"form_token": tok})
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
		if _, ok := requireLogin(w, r); !ok {
			return
		}
		token := getSessionToken(r)

		if err := client.DeleteContactPost(r.Context(), postID, token); err != nil {
			log.Printf("dansal-web: board delete failed post_id=%d path=%s err=%v", postID, r.URL.Path, err)
			redirectBoardError(w, r, eventID, "board_delete_error", err)
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
			boardErrorRedirect(w, r, eventID, "board_throttled")
			return
		}
		switch guardFormSubmit(w, r, cfg, ip) {
		case formGuardParseError:
			boardErrorRedirect(w, r, eventID, "board_form_error")
			return
		case formGuardHoneypot:
			log.Printf("dansal-web: HONEYPOT ip=%s path=%s", ip, r.URL.Path)
			boardSuccessRedirect(w, r, eventID, FlashMsg{BoardContacted: true})
			return
		case formGuardBadToken:
			log.Printf("dansal-web: FORM_TOKEN_REJECT ip_hash=%s path=%s", hashIP(ip), r.URL.Path)
			boardErrorRedirect(w, r, eventID, "board_form_error")
			return
		}

		email := r.FormValue("email")
		telegram := r.FormValue("telegram")
		message := r.FormValue("message")

		publicThrottle.record(ip + "|" + r.UserAgent())
		globalEmailSendRate.record()
		tgURL, err := client.ContactPoster(r.Context(), postID, email, telegram, message, cfg.publicBaseURL(), getSessionToken(r))
		if err != nil {
			log.Printf("dansal-web: board contact failed post_id=%d ip_hash=%s path=%s err=%v", postID, hashIP(ip), r.URL.Path, err)
			redirectBoardError(w, r, eventID, "board_contact_error", err)
			return
		}
		boardSuccessRedirect(w, r, eventID, FlashMsg{BoardContacted: true, BoardContactTgURL: tgURL})
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

		flash := flashTake(r.URL.Query().Get("msg"))
		ip := getClientIP(r)
		data := ContactManageData{
			Token:     token,
			Post:      post,
			FormToken: issueFormToken(ip),
			Updated:   flash.ManageUpdated,
			Deleted:   flash.ManageDeleted,
		}
		renderTemplate(w, tmpls.contactManage, tmplData(r, cfg, i18n, title, data))
	}
}

// manageRedirect stores msg as a one-time flash (#985) and redirects back to
// the contact-manage page for token, keyed by tok.
func manageRedirect(w http.ResponseWriter, r *http.Request, token, tok string, msg FlashMsg) {
	flashRedirect(w, r, "/contact-posts/manage/"+token, tok, msg)
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
		if !consumeFormToken(r.FormValue("_form_token"), ip, time.Second, stdFormMaxAge(cfg), cfg.FormTokenBindIP) {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}

		if r.FormValue("_action") == "delete" {
			if err := client.DeleteContactPostByManageToken(r.Context(), token); err != nil {
				log.Printf("dansal-web: manage delete failed path=%s err=%v", r.URL.Path, err)
				http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
				return
			}
			manageRedirect(w, r, token, flashToken(nil), FlashMsg{ManageDeleted: true})
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
			log.Printf("dansal-web: manage update failed path=%s err=%v", r.URL.Path, err)
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}
		manageRedirect(w, r, token, flashToken(nil), FlashMsg{ManageUpdated: true})
	}
}

// POST /contact-posts/manage/{token}/images — proxies a single image upload to the API.
// Reads the multipart file into memory (≤10 MB) then re-posts to the API as
// a new multipart request, using the manage_token already embedded in the URL.
func contactManageImageUploadHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		// Fetch post to get the numeric ID needed by the API endpoint.
		post, err := client.GetContactPostByToken(r.Context(), token)
		if err != nil || post.Expired {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}
		file, fh, err := r.FormFile("image")
		if err != nil {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}
		defer file.Close()
		imgData, err := io.ReadAll(io.LimitReader(file, 11<<20))
		if err != nil {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}
		if _, err := client.UploadContactPostImage(r.Context(), post.ID, token, imgData, fh.Filename); err != nil {
			log.Printf("dansal-web: manage image upload failed post_id=%d err=%v", post.ID, err)
		}
		http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
	}
}

// POST /contact-posts/manage/{token}/images/{img_id}/delete — proxies a
// per-image deletion to the API. Uses the manage_token from the URL.
func contactManageImageDeleteHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		imgID, err := strconv.Atoi(r.PathValue("img_id"))
		if err != nil {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}
		post, err := client.GetContactPostByToken(r.Context(), token)
		if err != nil {
			http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
			return
		}
		if err := client.DeleteContactPostImage(r.Context(), post.ID, imgID, token); err != nil {
			log.Printf("dansal-web: manage image delete failed post_id=%d img_id=%d err=%v", post.ID, imgID, err)
		}
		http.Redirect(w, r, "/contact-posts/manage/"+token, http.StatusSeeOther)
	}
}

// POST /board/resend-manage — accepts an email address and asks the API to
// send the manage links for all that address's live board posts. Always
// redirects to /board?resend=1 so the page can show a neutral confirmation
// banner regardless of whether posts were found (enumeration resistance).
func boardResendManageHandler(cfg *Config, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if publicThrottle.isBlocked(ip + "|" + r.UserAgent()) {
			log.Printf("%s ip=%s path=%s", publicBlock, ip, r.URL.Path)
			http.Redirect(w, r, "/board?resend=1", http.StatusSeeOther)
			return
		}
		switch guardFormSubmit(w, r, cfg, ip) {
		case formGuardParseError:
			http.Redirect(w, r, "/board", http.StatusSeeOther)
			return
		case formGuardHoneypot:
			log.Printf("dansal-web: HONEYPOT ip=%s path=%s", ip, r.URL.Path)
			http.Redirect(w, r, "/board?resend=1", http.StatusSeeOther)
			return
		case formGuardBadToken:
			log.Printf("dansal-web: FORM_TOKEN_REJECT ip_hash=%s path=%s", hashIP(ip), r.URL.Path)
			http.Redirect(w, r, "/board", http.StatusSeeOther)
			return
		}

		email := r.FormValue("email")
		publicThrottle.record(ip + "|" + r.UserAgent())
		globalEmailSendRate.record()

		if err := client.ResendManage(r.Context(), email, cfg.publicBaseURL()); err != nil {
			log.Printf("dansal-web: resend-manage failed ip_hash=%s err=%v", hashIP(ip), err)
		}
		http.Redirect(w, r, "/board?resend=1", http.StatusSeeOther)
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
