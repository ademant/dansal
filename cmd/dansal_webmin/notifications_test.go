package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSMTPNotifStatus(t *testing.T) {
	cases := []struct {
		name string
		s    *smtpConfig
		want notifStatus
	}{
		{"nil", nil, statusMissing},
		{"empty", &smtpConfig{}, statusMissing},
		{"full", &smtpConfig{Host: "smtp.example.com", From: "a@b.de", HasPassword: true}, statusOK},
		{"host only", &smtpConfig{Host: "smtp.example.com"}, statusPartial},
		{"from only", &smtpConfig{From: "a@b.de"}, statusPartial},
		{"password only", &smtpConfig{HasPassword: true}, statusPartial},
		{"missing password", &smtpConfig{Host: "smtp.example.com", From: "a@b.de"}, statusPartial},
		{"sendmail with from", &smtpConfig{Sendmail: "/usr/sbin/sendmail", From: "a@b.de"}, statusOK},
		{"sendmail without from", &smtpConfig{Sendmail: "/usr/sbin/sendmail"}, statusPartial},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := smtpNotifStatus(c.s); got != c.want {
				t.Fatalf("smtpNotifStatus(%+v) = %q, want %q", c.s, got, c.want)
			}
		})
	}
}

func TestTelegramNotifStatus(t *testing.T) {
	cases := []struct {
		name string
		t    *telegramConfig
		want notifStatus
	}{
		{"nil", nil, statusMissing},
		{"empty", &telegramConfig{}, statusMissing},
		{"full", &telegramConfig{BotToken: "123", BotName: "bot"}, statusOK},
		{"token only", &telegramConfig{BotToken: "123"}, statusPartial},
		{"name only", &telegramConfig{BotName: "bot"}, statusPartial},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := telegramNotifStatus(c.t); got != c.want {
				t.Fatalf("telegramNotifStatus(%+v) = %q, want %q", c.t, got, c.want)
			}
		})
	}
}

func TestMatrixNotifStatus(t *testing.T) {
	cases := []struct {
		name string
		m    *matrixConfig
		want notifStatus
	}{
		{"nil", nil, statusMissing},
		{"empty", &matrixConfig{}, statusMissing},
		{"full", &matrixConfig{Homeserver: "matrix.example.com", HasToken: true}, statusOK},
		{"homeserver only", &matrixConfig{Homeserver: "matrix.example.com"}, statusPartial},
		{"token only", &matrixConfig{HasToken: true}, statusPartial},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matrixNotifStatus(c.m); got != c.want {
				t.Fatalf("matrixNotifStatus(%+v) = %q, want %q", c.m, got, c.want)
			}
		})
	}
}

func TestHeartbeatNotifStatus(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		h    *heartbeatConfig
		want notifStatus
	}{
		{"nil", nil, statusMissing},
		{"zero interval", &heartbeatConfig{}, statusMissing},
		{"no channels", &heartbeatConfig{IntervalMins: 60}, statusOK},
		{"all ok", &heartbeatConfig{IntervalMins: 60, Email: channelStatus{Configured: true, OK: true, LastChecked: now}}, statusOK},
		{"email failed", &heartbeatConfig{IntervalMins: 60, Email: channelStatus{Configured: true, OK: false, LastChecked: now}}, statusPartial},
		{"telegram failed", &heartbeatConfig{IntervalMins: 60, Telegram: channelStatus{Configured: true, OK: false, LastChecked: now}}, statusPartial},
		{"matrix failed", &heartbeatConfig{IntervalMins: 60, Matrix: channelStatus{Configured: true, OK: false, LastChecked: now}}, statusPartial},
		{"unconfigured failure ignored", &heartbeatConfig{IntervalMins: 60, Email: channelStatus{OK: false, LastChecked: now}}, statusOK},
		{"failed but never probed", &heartbeatConfig{IntervalMins: 60, Telegram: channelStatus{Configured: true, OK: false}}, statusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := heartbeatNotifStatus(c.h); got != c.want {
				t.Fatalf("heartbeatNotifStatus(%+v) = %q, want %q", c.h, got, c.want)
			}
		})
	}
}

func TestSMTPSetRequest(t *testing.T) {
	body := "host=smtp.example.com&port=587&username=u&from=a%40b.de&from_name=Folk&tls=starttls&timeout_secs=20&to=x%40y.de&sendmail="
	r := httptest.NewRequest("POST", "/notifications/smtp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	req := smtpSetRequest(r)
	if req.Cmd != "smtp-set" {
		t.Fatalf("Cmd = %q, want smtp-set", req.Cmd)
	}
	if req.SMTPHost != "smtp.example.com" || req.SMTPPort != 587 || req.SMTPUsername != "u" {
		t.Fatalf("host/port/username parsed wrong: %+v", req)
	}
	if req.SMTPFrom != "a@b.de" || req.SMTPFromName != "Folk" {
		t.Fatalf("from parsed wrong: %+v", req)
	}
	if req.SMTPTLS != "starttls" || req.SMTPTimeoutSecs != 20 {
		t.Fatalf("tls/timeout parsed wrong: %+v", req)
	}
	if req.SMTPTo != "x@y.de" {
		t.Fatalf("to parsed wrong: %+v", req)
	}
}
