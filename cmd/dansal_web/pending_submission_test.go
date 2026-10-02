package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// #1422: the double-submit lock is scoped per form (and per event for
// booking/board) instead of one key per browser across every public form.

func TestPendingSubmissionScope(t *testing.T) {
	ip, ua := "203.0.113.7", "test-agent/1"
	setPendingSubmission(ip, ua, "booking|1", time.Minute)
	defer clearPendingSubmission(ip, ua, "booking|1")
	if !hasPendingSubmission(ip, ua, "booking|1") {
		t.Error("same booking: want pending")
	}
	for _, other := range []string{"booking|2", "board|1", "suggest", "register"} {
		if hasPendingSubmission(ip, ua, other) {
			t.Errorf("%s: blocked by an unrelated booking", other)
		}
	}
	if hasPendingSubmission(ip, "other-agent", "booking|1") {
		t.Error("another browser must not be blocked")
	}
}

// Two bookings for different events from the same browser both go through;
// re-sending the first one is rejected with the specific message.
func TestBookingSecondEventNotBlocked(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	publicThrottle = newSubmissionThrottleForget(100, time.Minute, time.Hour)
	globalEmailSendRate = newEmailSendThrottle(time.Minute, 1000, 2000)
	cfg := &Config{Domain: "example.test", FormTokenMaxAgeMins: 30}
	h := bookingPostHandler(cfg, &DansalClient{BaseURL: api.URL, HTTP: api.Client()}, loadI18n(""))

	const ip = "198.51.100.23"
	book := func(eventID string) FlashMsg {
		t.Helper()
		tok := issueFormToken(ip)
		// Backdate past the 1s minimum fill time.
		oneTimeTokens.Store(tok, oneTimeToken{createdAt: time.Now().Add(-5 * time.Second), ip: ip})
		form := url.Values{"_form_token": {tok}, "name": {"Ada"}, "email": {"ada@example.test"}, "persons": {"1"}}
		req := httptest.NewRequest(http.MethodPost, "/events/"+eventID+"/book", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "pending-test/1")
		req.RemoteAddr = ip + ":4242"
		req.SetPathValue("id", eventID)
		rec := httptest.NewRecorder()
		h(rec, req)
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil || rec.Code != http.StatusSeeOther {
			t.Fatalf("book %s: status %d, Location %q", eventID, rec.Code, rec.Header().Get("Location"))
		}
		return flashTake(loc.Query().Get("msg"))
	}
	if f := book("101"); !f.BookingOK {
		t.Fatalf("first booking: %+v", f)
	}
	if f := book("102"); !f.BookingOK {
		t.Errorf("booking a second event was blocked: %+v", f)
	}
	if f := book("101"); f.BookingOK || f.BookingError != "form_error_pending" {
		t.Errorf("re-sending the same booking: %+v, want form_error_pending", f)
	}
}
