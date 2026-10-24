package main

import (
	"bytes"
	"errors"
	"github.com/ademant/dansal/internal/logging"
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

// Status values for urlDatesResponse.Status. The check is advisory: only
// urlDatesNoEvents and urlDatesUnreachable show a note on the form.
const (
	urlDatesOK          = "ok"          // event data found; Dates lists its start days
	urlDatesNoEvents    = "no_events"   // page fetched, no iCal/JSON/schema.org event data
	urlDatesUnreachable = "unreachable" // fetch failed, timed out or was refused
	urlDatesSkipped     = "skipped"     // rate-limited, or not a checkable URL: no note
)

type urlDatesResponse struct {
	Dates  []string `json:"dates"`
	Status string   `json:"status"`
}

// urlDatesStatus maps a preview failure to a status: the API answers 422
// when the fetched body has no parseable events and 502 when it could not
// fetch the URL (suggestPreviewHandler); anything else is treated as
// unreachable so the suggester is told the check did not work.
func urlDatesStatus(err error) string {
	var ae *apiHTTPError
	if errors.As(err, &ae) {
		switch ae.StatusCode {
		case http.StatusUnprocessableEntity:
			return urlDatesNoEvents
		case http.StatusTooManyRequests:
			return urlDatesSkipped
		}
	}
	return urlDatesUnreachable
}

func suggestURLDatesHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		respond := func(status string, dates []string) {
			writeJSONResponse(w, http.StatusOK, urlDatesResponse{Dates: dates, Status: status})
		}
		raw := strings.TrimSpace(r.URL.Query().Get("url"))
		u, err := url.Parse(raw)
		if raw == "" || len(raw) > 2000 || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			respond(urlDatesSkipped, []string{})
			return
		}
		key := getClientIP(r) + "|" + r.UserAgent()
		if publicThrottle.isBlocked(key) {
			respond(urlDatesSkipped, []string{})
			return
		}
		publicThrottle.record(key)

		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		mw.WriteField("url", raw)
		mw.Close()
		events, err := client.SuggestEventPreview(r.Context(), &body, mw.FormDataContentType())
		if err != nil {
			logging.Errorf("suggest url-dates: %v", err)
			respond(urlDatesStatus(err), []string{})
			return
		}
		if len(events) == 0 {
			respond(urlDatesNoEvents, []string{})
			return
		}
		respond(urlDatesOK, startDates(events))
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
