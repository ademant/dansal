package main

import (
	"bytes"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// #1417: the suggest wizard cross-checks its typed date against the dates a
// source URL names. suggestURLDatesHandler serves
// GET /events/suggest/url-dates?url=… for the wizard's website field: it
// reuses the anonymous import preview (API suggest-preview: safeClient SSRF
// protection, its own rate limit) behind the same publicThrottle as the
// import tab, and returns only the distinct start dates — never the parsed
// content. Any failure yields an empty list: the check is advisory.

const urlDatesMax = 20

type urlDatesResponse struct {
	Dates []string `json:"dates"`
}

func suggestURLDatesHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		empty := urlDatesResponse{Dates: []string{}}
		raw := strings.TrimSpace(r.URL.Query().Get("url"))
		u, err := url.Parse(raw)
		if raw == "" || len(raw) > 2000 || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeJSONResponse(w, http.StatusOK, empty)
			return
		}
		key := getClientIP(r) + "|" + r.UserAgent()
		if publicThrottle.isBlocked(key) {
			writeJSONResponse(w, http.StatusOK, empty)
			return
		}
		publicThrottle.record(key)

		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		mw.WriteField("url", raw)
		mw.Close()
		events, err := client.SuggestEventPreview(r.Context(), &body, mw.FormDataContentType())
		if err != nil {
			log.Printf("suggest url-dates: %v", err)
			writeJSONResponse(w, http.StatusOK, empty)
			return
		}
		writeJSONResponse(w, http.StatusOK, urlDatesResponse{Dates: startDates(events)})
	}
}

// startDates returns the distinct calendar dates (YYYY-MM-DD, in each
// event's own offset — the date the source itself states) of the events'
// starts, sorted, at most urlDatesMax.
func startDates(events []PreviewEvent) []string {
	out := []string{}
	for _, e := range events {
		t, ok := parseTime(e.StartTime)
		if !ok {
			continue
		}
		if d := t.Format("2006-01-02"); !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out[:min(len(out), urlDatesMax)]
}
