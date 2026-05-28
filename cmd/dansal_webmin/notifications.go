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

type notificationsData struct {
	SMTP      *smtpConfig
	Telegram  *telegramConfig
	Matrix    *matrixConfig
	Heartbeat *heartbeatConfig
	Errors    map[string]string
	Flash     string
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
