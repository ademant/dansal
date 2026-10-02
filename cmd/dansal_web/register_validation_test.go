package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// #1415: a direct POST that skips the form's JS (bots) is rejected before it
// costs throttle, pending lock, email budget or an API call, and every error
// re-render keeps what the visitor typed.

func registerTestEnv(t *testing.T, apiStatus int, apiBody string) (http.HandlerFunc, *int32) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	var registerCalls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/register":
			atomic.AddInt32(&registerCalls, 1)
			w.WriteHeader(apiStatus)
			w.Write([]byte(apiBody))
		case strings.HasPrefix(r.URL.Path, "/api/v1/organizations"):
			w.Write([]byte(`[{"id":4,"name":"Folk in Neustadt"}]`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(api.Close)

	authThrottle = newSubmissionThrottle(1, time.Minute) // one recorded attempt = blocked
	globalEmailSendRate = newEmailSendThrottle(time.Minute, 1000, 2000)
	h := registerSubmitHandler(&Config{Domain: "example.test", FormTokenMaxAgeMins: 30}, loadTemplates(), &DansalClient{BaseURL: api.URL, HTTP: api.Client()}, loadI18n(""))
	return h, &registerCalls
}

func postRegister(t *testing.T, h http.HandlerFunc, ip string, form url.Values) string {
	t.Helper()
	tok := issueFormToken(ip)
	oneTimeTokens.Store(tok, oneTimeToken{createdAt: time.Now().Add(-5 * time.Second), ip: ip})
	form.Set("_form_token", tok)
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "register-test/1")
	req.RemoteAddr = ip + ":5555"
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Body.String()
}

func TestRegisterRejectsMissingOrgBeforeBookkeeping(t *testing.T) {
	cases := []struct {
		name, ip string
		form     url.Values
		wantKey  string
	}{
		{"join_org without org_id", "198.51.100.40", url.Values{"reg_type": {"join_org"}}, "register_error_no_org"},
		{"join_org with garbage org_id", "198.51.100.41", url.Values{"reg_type": {"join_org"}, "org_id": {"abc"}}, "register_error_no_org"},
		{"new_org without a name", "198.51.100.42", url.Values{"reg_type": {"new_org"}, "org_actor_name": {"folkneustadt"}}, "register_error_no_org_name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, calls := registerTestEnv(t, http.StatusOK, `{"status":"ok"}`)
			c.form.Set("email", "ada@example.test")
			c.form.Set("description", "Ich organisiere Bälle in Neustadt")
			c.form.Set("channel", "email")
			body := postRegister(t, h, c.ip, c.form)

			if n := atomic.LoadInt32(calls); n != 0 {
				t.Errorf("API register called %d times, want 0", n)
			}
			if authThrottle.isBlocked(c.ip) {
				t.Error("rejected submission counted against authThrottle")
			}
			if hasPendingSubmission(c.ip, "register-test/1", "register") {
				t.Error("rejected submission set the pending lock")
			}
			if n := len(globalEmailSendRate.times); n != 0 {
				t.Errorf("email budget used %d times", n)
			}
			want := loadI18n("").Strings("de").T(c.wantKey)
			if !strings.Contains(body, want) && !strings.Contains(body, loadI18n("").Strings("en").T(c.wantKey)) {
				t.Errorf("error %q not shown", c.wantKey)
			}
			for _, kept := range []string{`value="ada@example.test"`, `>Ich organisiere Bälle in Neustadt</textarea>`} {
				if !strings.Contains(body, kept) {
					t.Errorf("input not preserved: %s", kept)
				}
			}
			if c.form.Get("org_actor_name") != "" && !strings.Contains(body, `value="folkneustadt"`) {
				t.Error("new-org field not preserved")
			}
		})
	}
}

func TestRegisterAPIErrorKeepsInput(t *testing.T) {
	h, calls := registerTestEnv(t, http.StatusConflict, `{"error":"email already registered"}`)
	body := postRegister(t, h, "198.51.100.50", url.Values{
		"reg_type": {"join_org"}, "org_id": {"4"}, "email": {"ada@example.test"},
		"channel": {"telegram"}, "telegram": {"@ada"}, "description": {"Hallo"},
	})
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("API calls = %d, want 1", atomic.LoadInt32(calls))
	}
	for _, kept := range []string{`value="ada@example.test"`, `value="@ada"`, `>Hallo</textarea>`, `<option value="4" selected>`} {
		if !strings.Contains(body, kept) {
			t.Errorf("input not preserved after API error: %s", kept)
		}
	}
	if strings.Contains(body, honeypotField+`" value="`) {
		t.Error("honeypot echoed back")
	}
}
