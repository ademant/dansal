package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
)

type smtpConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	From        string `json:"from"`
	FromName    string `json:"from_name"`
	TLS         string `json:"tls"`
	TimeoutSecs int    `json:"timeout_secs"`
	HasPassword bool   `json:"has_password"`
	To          string `json:"to"`
}

type telegramConfig struct {
	BotToken string `json:"bot_token"`
	BotName  string `json:"bot_name"`
}

type matrixConfig struct {
	Homeserver string `json:"homeserver"`
	Username   string `json:"username"`
	HasToken   bool   `json:"has_token"`
}

type heartbeatConfig struct {
	IntervalMins int `json:"interval_mins"`
}

func getSocketData(socketPath, cmd string, out any) error {
	resp, err := sendSocket(socketPath, socketRequest{Cmd: cmd})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	return json.Unmarshal(resp.Data, out)
}

// notifStatus values drive the colour indicator: "ok" = green, "partial" = red, "missing" = grey.
type notifStatus string

const (
	statusOK      notifStatus = "ok"
	statusPartial notifStatus = "partial"
	statusMissing notifStatus = "missing"
)

type notificationsData struct {
	SMTP      *smtpConfig
	Telegram  *telegramConfig
	Matrix    *matrixConfig
	Heartbeat *heartbeatConfig
	Errors    map[string]string
	Flash     string

	SMTPStatus      notifStatus
	TelegramStatus  notifStatus
	MatrixStatus    notifStatus
	HeartbeatStatus notifStatus
}

func smtpNotifStatus(s *smtpConfig) notifStatus {
	if s == nil {
		return statusMissing
	}
	if s.Host != "" && s.From != "" && s.HasPassword && s.To != "" {
		return statusOK
	}
	if s.Host != "" || s.From != "" || s.HasPassword {
		return statusPartial
	}
	return statusMissing
}

func telegramNotifStatus(t *telegramConfig) notifStatus {
	if t == nil {
		return statusMissing
	}
	if t.BotToken != "" && t.BotName != "" {
		return statusOK
	}
	if t.BotToken != "" || t.BotName != "" {
		return statusPartial
	}
	return statusMissing
}

func matrixNotifStatus(m *matrixConfig) notifStatus {
	if m == nil {
		return statusMissing
	}
	if m.Homeserver != "" && m.HasToken {
		return statusOK
	}
	if m.Homeserver != "" || m.HasToken {
		return statusPartial
	}
	return statusMissing
}

func heartbeatNotifStatus(h *heartbeatConfig) notifStatus {
	if h == nil || h.IntervalMins == 0 {
		return statusMissing
	}
	return statusOK
}

func loadNotificationsData(socketPath string) notificationsData {
	d := notificationsData{Errors: map[string]string{}}
	var smtp smtpConfig
	if err := getSocketData(socketPath, "smtp-get", &smtp); err != nil {
		d.Errors["smtp"] = err.Error()
	} else {
		d.SMTP = &smtp
	}
	var tg telegramConfig
	if err := getSocketData(socketPath, "telegram-get", &tg); err != nil {
		d.Errors["telegram"] = err.Error()
	} else {
		d.Telegram = &tg
	}
	var mx matrixConfig
	if err := getSocketData(socketPath, "matrix-get", &mx); err != nil {
		d.Errors["matrix"] = err.Error()
	} else {
		d.Matrix = &mx
	}
	var hb heartbeatConfig
	if err := getSocketData(socketPath, "heartbeat-get", &hb); err != nil {
		d.Errors["heartbeat"] = err.Error()
	} else {
		d.Heartbeat = &hb
	}
	d.SMTPStatus      = smtpNotifStatus(d.SMTP)
	d.TelegramStatus  = telegramNotifStatus(d.Telegram)
	d.MatrixStatus    = matrixNotifStatus(d.Matrix)
	d.HeartbeatStatus = heartbeatNotifStatus(d.Heartbeat)
	return d
}

func notificationsPageHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nd := loadNotificationsData(cfg.AdminSocket)
		nd.Flash = r.URL.Query().Get("flash")
		d := tmplData(cfg, "Notifications", nd)
		d.User = getSessionUser(r)
		renderTemplate(w, tmpls.notifications, d)
	}
}

func notificationsSMTPSaveHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		port, _ := strconv.Atoi(r.FormValue("port"))
		timeoutSecs, _ := strconv.Atoi(r.FormValue("timeout_secs"))
		req := socketRequest{
			Cmd:             "smtp-set",
			SMTPHost:        r.FormValue("host"),
			SMTPPort:        port,
			SMTPUsername:    r.FormValue("username"),
			SMTPFrom:        r.FormValue("from"),
			SMTPFromName:    r.FormValue("from_name"),
			SMTPTLS:         r.FormValue("tls"),
			SMTPTimeoutSecs: timeoutSecs,
			SMTPTo:          r.FormValue("to"),
		}
		resp, err := sendSocket(cfg.AdminSocket, req)
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			log.Printf("smtp-set: %v / %s", err, msg)
			http.Redirect(w, r, "/notifications?flash="+url.QueryEscape("SMTP error: "+msg), http.StatusSeeOther)
			return
		}

		// update password if provided
		if pw := r.FormValue("password"); pw != "" {
			resp2, err2 := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "smtp-set-password", Password: pw})
			if err2 != nil || !resp2.OK {
				msg := "socket error"
				if err2 == nil {
					msg = resp2.Error
				}
				http.Redirect(w, r, "/notifications?flash="+url.QueryEscape("SMTP saved but password error: "+msg), http.StatusSeeOther)
				return
			}
		}
		http.Redirect(w, r, "/notifications?flash=SMTP+settings+saved", http.StatusSeeOther)
	}
}

func notificationsTelegramSaveHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{
			Cmd:              "telegram-set",
			TelegramBotToken: r.FormValue("bot_token"),
			TelegramBotName:  r.FormValue("bot_name"),
		})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			http.Redirect(w, r, "/notifications?flash="+url.QueryEscape("Telegram error: "+msg), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/notifications?flash=Telegram+settings+saved", http.StatusSeeOther)
	}
}

func notificationsMatrixSaveHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{
			Cmd:               "matrix-set",
			MatrixHomeserver:  r.FormValue("homeserver"),
			MatrixAccessToken: r.FormValue("access_token"),
		})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			http.Redirect(w, r, "/notifications?flash="+url.QueryEscape("Matrix error: "+msg), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/notifications?flash=Matrix+settings+saved", http.StatusSeeOther)
	}
}

// notificationsSMTPTestHandler saves settings then sends a test email.
// Accepts the same form fields as the SMTP save handler plus an optional
// override recipient (defaults to the stored "to" address).
func notificationsSMTPTestHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "bad request"})
			return
		}
		port, _ := strconv.Atoi(r.FormValue("port"))
		timeoutSecs, _ := strconv.Atoi(r.FormValue("timeout_secs"))
		saveReq := socketRequest{
			Cmd:             "smtp-set",
			SMTPHost:        r.FormValue("host"),
			SMTPPort:        port,
			SMTPUsername:    r.FormValue("username"),
			SMTPFrom:        r.FormValue("from"),
			SMTPFromName:    r.FormValue("from_name"),
			SMTPTLS:         r.FormValue("tls"),
			SMTPTimeoutSecs: timeoutSecs,
			SMTPTo:          r.FormValue("to"),
		}
		resp, err := sendSocket(cfg.AdminSocket, saveReq)
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "save failed: " + msg})
			return
		}
		if pw := r.FormValue("password"); pw != "" {
			resp2, err2 := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "smtp-set-password", Password: pw})
			if err2 != nil || !resp2.OK {
				msg := "socket error"
				if err2 == nil {
					msg = resp2.Error
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "password save failed: " + msg})
				return
			}
		}
		// Send test email to the configured recipient.
		to := r.FormValue("to")
		if to == "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "No recipient address configured — fill in the Admin To field first."})
			return
		}
		testResp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "smtp-test", SMTPTo: to})
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "socket error: " + err.Error()})
			return
		}
		if !testResp.OK {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": testResp.Error})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

func notificationsHeartbeatSaveHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mins, _ := strconv.Atoi(r.FormValue("interval_mins"))
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{
			Cmd:                   "heartbeat-set",
			HeartbeatIntervalMins: mins,
		})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			http.Redirect(w, r, "/notifications?flash="+url.QueryEscape("Heartbeat error: "+msg), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/notifications?flash=Heartbeat+interval+saved", http.StatusSeeOther)
	}
}
