package main

import (
	"log"
	"net/http"
	"strings"
)

// AdminFetchurlSuggestionsData is the template data for the pending
// feed-suggestion review queue (#1333 phase 2).
type AdminFetchurlSuggestionsData struct {
	Suggestions  []PendingFetchSuggestion
	Flash        string
	FlashIsError bool
}

// adminFetchurlSuggestionsHandler lists pending feed suggestions the caller
// may review — the API applies the actual admin-vs-org-member filtering
// (listPendingFetchSuggestionsHandler), the same split
// listPendingRegsHandler already uses for pending_registrations.
func adminFetchurlSuggestionsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" && su.Role != "user" {
			forbidden(w, r)
			return
		}
		token := getSessionToken(r)
		suggestions, err := client.ListFetchSuggestions(r.Context(), token)
		if err != nil {
			log.Printf("admin fetchurl suggestions list: %v", err)
			suggestions = nil
		}
		flash := r.URL.Query().Get("flash")
		title := i18n.T(r, "admin_fetchurl_suggestions_title")
		renderTemplate(w, tmpls.adminFetchurlSuggestions, tmplData(r, cfg, i18n, title, AdminFetchurlSuggestionsData{
			Suggestions:  suggestions,
			Flash:        flash,
			FlashIsError: strings.HasSuffix(flash, "_error"),
		}))
	}
}

func adminFetchurlSuggestionApproveHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" && su.Role != "user" {
			forbidden(w, r)
			return
		}
		id, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		token := getSessionToken(r)
		flash := "fetchurl_suggestion_approved"
		if err := client.ApproveFetchSuggestion(r.Context(), token, id); err != nil {
			log.Printf("approve fetchurl suggestion %d: %v", id, err)
			flash = "fetchurl_suggestion_approve_error"
		}
		http.Redirect(w, r, "/admin/fetchurl-suggestions?flash="+flash, http.StatusSeeOther)
	}
}

func adminFetchurlSuggestionRejectHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" && su.Role != "user" {
			forbidden(w, r)
			return
		}
		id, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		token := getSessionToken(r)
		flash := "fetchurl_suggestion_rejected"
		if err := client.RejectFetchSuggestion(r.Context(), token, id); err != nil {
			log.Printf("reject fetchurl suggestion %d: %v", id, err)
			flash = "fetchurl_suggestion_reject_error"
		}
		http.Redirect(w, r, "/admin/fetchurl-suggestions?flash="+flash, http.StatusSeeOther)
	}
}
