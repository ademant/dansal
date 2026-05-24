package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

var (
	suggestPreviewThrottle = newSubmissionThrottle(5, 10*time.Minute)
	suggestSubmitThrottle  = newSubmissionThrottle(3, 10*time.Minute)
)

type SuggestPageData struct {
	HintSMTP       bool // SMTP configured → show email verification hint
	PreviewEvents  []PreviewEvent
	PreviewJSON    []string
	Error          string
	CaptchaSiteKey string
	GroupedTags    []TagGroup
	FormToken      string
}

type SuggestDoneData struct{}
type SuggestVerifiedData struct {
	Error string
}

func suggestPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		title := i18n.T(r, "suggest_event_title")
		renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
			HintSMTP:       cfg.SMTPHost != "",
			CaptchaSiteKey: cfg.CaptchaSiteKey,
			FormToken:      newFormToken(),
		}))
	}
}

func suggestPreviewHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		if suggestPreviewThrottle.isBlocked(ip) {
			title := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
				HintSMTP: cfg.SMTPHost != "",
				Error:    i18n.T(r, "suggest_error_rate_limit"),
			}))
			return
		}
		suggestPreviewThrottle.record(ip)

		events, err := client.SuggestEventPreview(r.Context(), r.Body, r.Header.Get("Content-Type"))
		if err != nil {
			title := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
				HintSMTP: cfg.SMTPHost != "",
				Error:    i18n.T(r, "suggest_error_parse"),
			}))
			return
		}

		previewJSON := make([]string, len(events))
		for i, e := range events {
			b, _ := json.Marshal(e)
			previewJSON[i] = string(b)
		}

		title := i18n.T(r, "suggest_event_title")
		renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
			HintSMTP:       cfg.SMTPHost != "",
			PreviewEvents:  events,
			PreviewJSON:    previewJSON,
			CaptchaSiteKey: cfg.CaptchaSiteKey,
			FormToken:      newFormToken(),
		}))
	}
}

func suggestSubmitHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		if suggestSubmitThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/suggest", authBlock, ip)
			title := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
				HintSMTP: cfg.SMTPHost != "",
				Error:    i18n.T(r, "suggest_error_rate_limit"),
			}))
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if r.FormValue("phone2") != "" || !validFormToken(r.FormValue("_form_ts"), cfg.MinSubmitSecs) {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		// Captcha check.
		if cfg.CaptchaSiteKey != "" {
			if err := verifyTurnstile(cfg, r.FormValue("cf-turnstile-response")); err != nil {
				title := i18n.T(r, "suggest_event_title")
				renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, title, SuggestPageData{
					HintSMTP: cfg.SMTPHost != "",
					Error:    i18n.T(r, "suggest_error_captcha"),
				}))
				return
			}
		}

		title := r.FormValue("title")
		description := r.FormValue("description")

		if strings.Contains(title, "http://") || strings.Contains(title, "https://") ||
			strings.Contains(description, "http://") || strings.Contains(description, "https://") {
			pageTitle := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, pageTitle, SuggestPageData{
				HintSMTP: cfg.SMTPHost != "",
				Error:    i18n.T(r, "suggest_error_links"),
			}))
			return
		}

		tags := r.Form["tags"]
		req := SuggestEventReq{
			Title:              title,
			Description:        description,
			StartTime:          r.FormValue("start_time"),
			EndTime:            r.FormValue("end_time"),
			HasBall:            sliceContains(tags, "ball"),
			HasWorkshop:        sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop"),
			HasFestival:        sliceContains(tags, "festival"),
			Tags:               tags,
			URL:                r.FormValue("url"),
			Food:               r.FormValue("food"),
			Drink:              r.FormValue("drink"),
			Location: PreviewLoc{
				Location: r.FormValue("location"),
				Town:     r.FormValue("town"),
				Country:  r.FormValue("country"),
			},
			Email:  r.FormValue("email"),
			Phone2: r.FormValue("phone2"),
		}

		suggestSubmitThrottle.record(ip)

		if err := client.SuggestEvent(r.Context(), req, cfg.publicBaseURL()); err != nil {
			pageTitle := i18n.T(r, "suggest_event_title")
			renderTemplate(w, tmpls.suggestEvent, tmplData(r, cfg, i18n, pageTitle, SuggestPageData{
				HintSMTP: cfg.SMTPHost != "",
				Error:    i18n.T(r, "suggest_error_submit"),
			}))
			return
		}

		http.Redirect(w, r, "/events/suggest/done", http.StatusSeeOther)
	}
}

func suggestDoneHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := i18n.T(r, "suggest_done_title")
		renderTemplate(w, tmpls.suggestDone, tmplData(r, cfg, i18n, title, SuggestDoneData{}))
	}
}

func suggestVerifyHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		errKey := ""
		if err := client.VerifySuggestion(r.Context(), token); err != nil {
			errKey = "suggest_error_token"
		}
		title := i18n.T(r, "suggest_verified_title")
		renderTemplate(w, tmpls.suggestVerified, tmplData(r, cfg, i18n, title, SuggestVerifiedData{Error: errKey}))
	}
}
