package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ChannelStatus struct {
	Configured  bool      `json:"configured"`
	OK          bool      `json:"ok"`
	LastChecked time.Time `json:"last_checked"`
	Error       string    `json:"error,omitempty"`
}

type HeartbeatStatus struct {
	IntervalMins int           `json:"interval_mins"`
	Email        ChannelStatus `json:"email"`
	Telegram     ChannelStatus `json:"telegram"`
	Matrix       ChannelStatus `json:"matrix"`
}

var (
	heartbeatMu      sync.RWMutex
	heartbeatResult  HeartbeatStatus
	lastHeartbeatRun time.Time
)

func runHeartbeat() {
	for {
		interval := time.Duration(config.Server.HeartbeatIntervalMins) * time.Minute
		if interval <= 0 {
			interval = 5 * time.Minute
		}
		// Sleep until the next scheduled probe, or fire immediately on first run.
		// lastHeartbeatRun (unlike the per-channel LastChecked fields) is always
		// set below regardless of which channels are configured, so an instance
		// with e.g. no Telegram/Matrix configured still paces itself correctly
		// instead of busy-looping.
		if !lastHeartbeatRun.IsZero() {
			wait := time.Until(lastHeartbeatRun.Add(interval))
			if wait > 0 {
				time.Sleep(wait)
			}
		}

		email := probeEmail()
		telegram := probeTelegram()
		matrix := probeMatrix()

		lastHeartbeatRun = time.Now()
		heartbeatMu.Lock()
		heartbeatResult = HeartbeatStatus{
			Email:    email,
			Telegram: telegram,
			Matrix:   matrix,
		}
		heartbeatMu.Unlock()
	}
}

func probeEmail() ChannelStatus {
	cfg := config.SMTP
	if cfg.Host == "" {
		return ChannelStatus{Configured: false}
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	timeout := 10 * time.Second
	conn, err := dialSMTPConn(cfg.Host, fmt.Sprintf("%d", port), timeout)
	if err != nil {
		return ChannelStatus{Configured: true, OK: false, LastChecked: time.Now(), Error: err.Error()}
	}
	conn.Close()
	return ChannelStatus{Configured: true, OK: true, LastChecked: time.Now()}
}

func probeTelegram() ChannelStatus {
	token := config.Server.TelegramBotToken
	if token == "" {
		return ChannelStatus{Configured: false}
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.telegram.org/bot" + token + "/getMe")
	if err != nil {
		return ChannelStatus{Configured: true, OK: false, LastChecked: time.Now(), Error: err.Error()}
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		return ChannelStatus{Configured: true, OK: false, LastChecked: time.Now(), Error: result.Description}
	}
	return ChannelStatus{Configured: true, OK: true, LastChecked: time.Now()}
}

func probeMatrix() ChannelStatus {
	homeserver := strings.TrimRight(config.Server.MatrixHomeserver, "/")
	accessToken := config.Server.MatrixAccessToken
	if homeserver == "" || accessToken == "" {
		return ChannelStatus{Configured: false}
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", homeserver+"/_matrix/client/v3/account/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return ChannelStatus{Configured: true, OK: false, LastChecked: time.Now(), Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var result struct {
			ErrCode string `json:"errcode"`
			Error   string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		msg := result.Error
		if result.ErrCode != "" {
			msg = result.ErrCode + ": " + msg
		}
		return ChannelStatus{Configured: true, OK: false, LastChecked: time.Now(), Error: msg}
	}
	return ChannelStatus{Configured: true, OK: true, LastChecked: time.Now()}
}
