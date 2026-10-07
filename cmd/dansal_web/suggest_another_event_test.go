package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #1468: "suggest another event" button on the done page, sized to the
// per-address remaining count carried via ?left=&cap=, and the same hint
// shown again at the top of the wizard when reached via that button.

func TestSuggestDoneButtonAndHints(t *testing.T) {
	setupSiteCfg(t)
	cfg := &Config{Domain: "example.test"}
	i18n := loadI18n("")
	h := suggestDoneHandler(cfg, loadTemplates(), i18n)

	render := func(query string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/events/suggest/done"+query, nil)
		req.AddCookie(&http.Cookie{Name: "dsw_lang", Value: "en"})
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// Checks for the CSS class on a rendered element (class="... foo"),
	// not just the substring "foo" -- which the <style> block's own rule
	// definition would also match regardless of whether anything uses it.
	hasClass := func(body, class string) bool {
		return strings.Contains(body, `class="suggest-done-btn `+class+`"`) ||
			strings.Contains(body, `class="`+class+`"`)
	}

	t.Run("no left param: active button, no hint", func(t *testing.T) {
		body := render("")
		if !strings.Contains(body, `href="/events/suggest?next=1"`) {
			t.Errorf("missing active 'suggest another' link, body: %s", body)
		}
		if hasClass(body, "suggest-done-btn-disabled") {
			t.Errorf("button should not be disabled with no left param")
		}
		if strings.Contains(body, `class="suggest-done-left-hint"`) {
			t.Errorf("no hint expected with no left param")
		}
	})

	t.Run("left > 0: active button carrying left/cap, remaining hint", func(t *testing.T) {
		body := render("?left=2&cap=5")
		if !strings.Contains(body, `href="/events/suggest?next=1&left=2&cap=5"`) {
			t.Errorf("link doesn't carry left/cap through, body: %s", body)
		}
		if hasClass(body, "suggest-done-btn-disabled") {
			t.Errorf("button should be active when left > 0")
		}
		if !strings.Contains(body, "suggest 2 more events") {
			t.Errorf("missing the remaining-count hint, body: %s", body)
		}
	})

	t.Run("left == 0: disabled button, cap hint", func(t *testing.T) {
		body := render("?left=0&cap=5")
		if !hasClass(body, "suggest-done-btn-disabled") {
			t.Errorf("button should be disabled when left == 0, body: %s", body)
		}
		if !strings.Contains(body, "5 suggestions waiting for review") {
			t.Errorf("missing the at-cap hint, body: %s", body)
		}
	})
}

func TestSuggestPageNextPrefillHint(t *testing.T) {
	setupSiteCfg(t)
	tokenThrottle = newSubmissionThrottle(100, time.Minute)
	globalEmailSendRate = newEmailSendThrottle(time.Minute, 1000, 2000)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer api.Close()
	cfg := &Config{Domain: "example.test", TelegramBotToken: "x", FormTokenMaxAgeMins: 30}
	client := &DansalClient{BaseURL: api.URL, HTTP: api.Client()}
	h := suggestPageHandler(cfg, loadTemplates(), client, loadI18n(""))

	req := httptest.NewRequest(http.MethodGet, "/events/suggest?next=1&left=3&cap=5", nil)
	req.AddCookie(&http.Cookie{Name: "dsw_lang", Value: "en"})
	req.RemoteAddr = "203.0.113.50:12345"
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "suggest 3 more events") {
		t.Errorf("missing the carried-over remaining-count hint, body tail: %s", body[max(0, len(body)-2000):])
	}
	if !strings.Contains(body, "dansal_suggest_prefill") {
		t.Errorf("missing the sessionStorage restore script")
	}
	checkInlineJS(t, body)
}
