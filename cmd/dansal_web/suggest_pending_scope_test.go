package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// #1467: the suggest double-submit lock is keyed to the event (normalized
// title + start_time + location), not a bare "suggest" literal shared by
// every suggestion from the same visitor.

func TestSuggestPendingScopeKeyedToEvent(t *testing.T) {
	a := suggestPendingScope("Alpha Ball", "2030-05-01T20:00", "Test Hall")
	b := suggestPendingScope("Beta Ball", "2030-06-01T20:00", "Test Hall")
	aAgain := suggestPendingScope("  Alpha Ball  ", "2030-05-01T20:00", "test hall")
	if a == b {
		t.Errorf("different events produced the same scope: %q", a)
	}
	if a != aAgain {
		t.Errorf("the same event (modulo whitespace/case) produced different scopes: %q vs %q", a, aAgain)
	}
	if !strings.HasPrefix(a, "suggest|") {
		t.Errorf("scope %q missing suggest| prefix", a)
	}
}

// TestSuggestSecondEventNotBlockedButResendIs is suggest.go's analogue of
// TestBookingSecondEventNotBlocked (pending_submission_test.go): two
// different suggestions from the same visitor both go through, but
// resending the same one is rejected -- and the rejection re-renders the
// wizard with the submitted title preserved (#1467) rather than a blank
// form, using the corrected "waiting for review" wording rather than the
// stale confirm-by-email one.
func TestSuggestSecondEventNotBlockedButResendIs(t *testing.T) {
	setupSiteCfg(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/events/suggest":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
		case "/api/v1/dances":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]Dance{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	publicThrottle = newSubmissionThrottleForget(100, time.Minute, time.Hour)
	globalEmailSendRate = newEmailSendThrottle(time.Minute, 1000, 2000)
	cfg := &Config{Domain: "example.test", TelegramBotToken: "x", FormTokenMaxAgeMins: 30}
	client := &DansalClient{BaseURL: api.URL, HTTP: api.Client()}
	h := suggestSubmitHandler(cfg, loadTemplates(), client, loadI18n(""))

	const ip = "198.51.100.44"
	submit := func(title, startTime string) *httptest.ResponseRecorder {
		t.Helper()
		tok := issueFormToken(ip)
		oneTimeTokens.Store(tok, oneTimeToken{createdAt: time.Now().Add(-5 * time.Second), ip: ip})
		form := url.Values{
			"_form_token":  {tok},
			"dansal_title": {title},
			"start_time":   {startTime},
			"location":     {"Test Hall"},
		}
		req := httptest.NewRequest(http.MethodPost, "/events/suggest/submit", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "pending-test/1")
		req.AddCookie(&http.Cookie{Name: "dsw_lang", Value: "en"})
		req.RemoteAddr = ip + ":4242"
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}

	if rec := submit("Alpha Ball", "2030-05-01T20:00"); rec.Code != http.StatusSeeOther {
		t.Fatalf("first suggestion: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := submit("Beta Ball", "2030-06-01T20:00"); rec.Code != http.StatusSeeOther {
		t.Fatalf("second (different) suggestion was blocked: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := submit("Alpha Ball", "2030-05-01T20:00")
	if rec.Code != http.StatusOK {
		t.Fatalf("resending the same suggestion: status=%d, want 200 (error re-render)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "waiting for review") {
		t.Errorf("resend error body missing the corrected wording, got: %s", body)
	}
	if strings.Contains(body, "confirm it using the link") {
		t.Errorf("resend error body still has the stale confirm-by-email wording")
	}
	if !strings.Contains(body, "Alpha Ball") {
		t.Errorf("resend error body doesn't preserve the submitted title")
	}
	// The error-prefill <script> block (events_suggest.html) is new in this
	// change; checkInlineJS (js_syntax_test.go) catches a JS syntax error
	// html/template can't, since it treats script bodies as opaque text.
	checkInlineJS(t, body)
}
