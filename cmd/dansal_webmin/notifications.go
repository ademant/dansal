package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
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
	Sendmail    string `json:"sendmail"`
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

type channelStatus struct {
	Configured  bool      `json:"configured"`
	OK          bool      `json:"ok"`
	LastChecked time.Time `json:"last_checked"`
	Error       string    `json:"error,omitempty"`
}

type heartbeatConfig struct {
	IntervalMins int           `json:"interval_mins"`
	Email        channelStatus `json:"email"`
	Telegram     channelStatus `json:"telegram"`
	Matrix       channelStatus `json:"matrix"`
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
	if s.Sendmail != "" {
		if s.From != "" {
			return statusOK
		}
		return statusPartial
	}
	if s.Host != "" && s.From != "" && s.HasPassword {
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
	// If any configured channel has a probe failure, show partial.
	for _, ch := range []channelStatus{h.Email, h.Telegram, h.Matrix} {
		if ch.Configured && !ch.OK && !ch.LastChecked.IsZero() {
			return statusPartial
		}
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
	d.SMTPStatus = smtpNotifStatus(d.SMTP)
	d.TelegramStatus = telegramNotifStatus(d.Telegram)
	d.MatrixStatus = matrixNotifStatus(d.Matrix)
	d.HeartbeatStatus = heartbeatNotifStatus(d.Heartbeat)
	return d
}

func notificationsPageHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nd := loadNotificationsData(cfg.AdminSocket)
		nd.Flash = r.URL.Query().Get("flash")
		d := tmplData(r, cfg, "Notifications", nd)
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
			SMTPSendmail:    r.FormValue("sendmail"),
		}
		if _, ok := socketFlashRedirect(w, r, cfg, "/notifications", "SMTP error", req); !ok {
			return
		}

		// update password if provided
		if pw := r.FormValue("password"); pw != "" {
			if _, ok := socketFlashRedirect(w, r, cfg, "/notifications", "SMTP saved but password error", socketRequest{Cmd: "smtp-set-password", Password: pw}); !ok {
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
		if _, ok := socketFlashRedirect(w, r, cfg, "/notifications", "Telegram error", socketRequest{
			Cmd:              "telegram-set",
			TelegramBotToken: r.FormValue("bot_token"),
			TelegramBotName:  r.FormValue("bot_name"),
		}); !ok {
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
		if _, ok := socketFlashRedirect(w, r, cfg, "/notifications", "Matrix error", socketRequest{
			Cmd:               "matrix-set",
			MatrixHomeserver:  r.FormValue("homeserver"),
			MatrixAccessToken: r.FormValue("access_token"),
		}); !ok {
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
			SMTPSendmail:    r.FormValue("sendmail"),
		}
		if _, err := doSocket(cfg, saveReq); err != nil {
			respondJSON(w, http.StatusBadGateway, false, "save failed: "+err.Error())
			return
		}
		if pw := r.FormValue("password"); pw != "" {
			if _, err := doSocket(cfg, socketRequest{Cmd: "smtp-set-password", Password: pw}); err != nil {
				respondJSON(w, http.StatusBadGateway, false, "password save failed: "+err.Error())
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

// notificationsTelegramTestHandler saves then verifies the bot token via getMe.
func notificationsTelegramTestHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			respondJSON(w, http.StatusBadRequest, false, "bad request")
			return
		}
		if _, err := doSocket(cfg, socketRequest{
			Cmd:              "telegram-set",
			TelegramBotToken: r.FormValue("bot_token"),
			TelegramBotName:  r.FormValue("bot_name"),
		}); err != nil {
			respondJSON(w, http.StatusBadGateway, false, "save failed: "+err.Error())
			return
		}
		test, err2 := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "telegram-test"})
		if err2 != nil {
			respondJSON(w, http.StatusBadGateway, false, err2.Error())
			return
		}
		if !test.OK {
			respondJSON(w, http.StatusOK, false, test.Error)
			return
		}
		respondJSON(w, http.StatusOK, true, "")
	}
}

// notificationsMatrixLoginHandler exchanges homeserver+username+password for an
// access token via the matrix-login socket command.
func notificationsMatrixLoginHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			respondJSON(w, http.StatusBadRequest, false, "bad request")
			return
		}
		if _, err := doSocket(cfg, socketRequest{
			Cmd:              "matrix-login",
			MatrixHomeserver: r.FormValue("homeserver"),
			MatrixUsername:   r.FormValue("username"),
			MatrixPassword:   r.FormValue("password"),
		}); err != nil {
			respondJSON(w, http.StatusOK, false, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, true, "")
	}
}

// notificationsMatrixTestHandler saves a manual token then verifies it.
func notificationsMatrixTestHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			respondJSON(w, http.StatusBadRequest, false, "bad request")
			return
		}
		if token := r.FormValue("access_token"); token != "" {
			if _, err := doSocket(cfg, socketRequest{
				Cmd:               "matrix-set",
				MatrixHomeserver:  r.FormValue("homeserver"),
				MatrixAccessToken: token,
			}); err != nil {
				respondJSON(w, http.StatusBadGateway, false, "save failed: "+err.Error())
				return
			}
		}
		test, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "matrix-test"})
		if err != nil {
			respondJSON(w, http.StatusBadGateway, false, err.Error())
			return
		}
		if !test.OK {
			respondJSON(w, http.StatusOK, false, test.Error)
			return
		}
		respondJSON(w, http.StatusOK, true, "")
	}
}

func respondJSON(w http.ResponseWriter, status int, ok bool, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if ok {
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	} else {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": errMsg})
	}
}

func notificationsHeartbeatSaveHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mins, _ := strconv.Atoi(r.FormValue("interval_mins"))
		if _, ok := socketFlashRedirect(w, r, cfg, "/notifications", "Heartbeat error", socketRequest{
			Cmd:                   "heartbeat-set",
			HeartbeatIntervalMins: mins,
		}); !ok {
			return
		}
		http.Redirect(w, r, "/notifications?flash=Heartbeat+interval+saved", http.StatusSeeOther)
	}
}
