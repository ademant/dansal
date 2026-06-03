package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func adminTelegramGet() adminResponse {
	return adminResponse{OK: true, Data: map[string]string{
		"bot_token": config.Server.TelegramBotToken,
		"bot_name":  config.Server.TelegramBotName,
	}}
}

func adminTelegramSet(req adminRequest) adminResponse {
	if req.TelegramBotToken != "" {
		config.Server.TelegramBotToken = req.TelegramBotToken
	}
	if req.TelegramBotName != "" {
		config.Server.TelegramBotName = req.TelegramBotName
	}
	if configFilePath != "" {
		if err := saveConfig(configFilePath); err != nil {
			return adminResponse{OK: false, Error: "config save failed: " + err.Error()}
		}
	}
	return adminResponse{OK: true}
}

func adminMatrixGet() adminResponse {
	tokenSet := config.Server.MatrixAccessToken != ""
	return adminResponse{OK: true, Data: map[string]any{
		"homeserver": config.Server.MatrixHomeserver,
		"has_token":  tokenSet,
	}}
}

func adminMatrixSet(req adminRequest) adminResponse {
	if req.MatrixHomeserver != "" {
		config.Server.MatrixHomeserver = req.MatrixHomeserver
	}
	if req.MatrixAccessToken != "" {
		config.Server.MatrixAccessToken = req.MatrixAccessToken
	}
	if configFilePath != "" {
		if err := saveConfig(configFilePath); err != nil {
			return adminResponse{OK: false, Error: "config save failed: " + err.Error()}
		}
	}
	return adminResponse{OK: true}
}

func adminMatrixLogin(req adminRequest) adminResponse {
	hs := strings.TrimRight(strings.TrimSpace(req.MatrixHomeserver), "/")
	if hs == "" || req.MatrixUsername == "" || req.MatrixPassword == "" {
		return adminResponse{OK: false, Error: "homeserver, username and password are required"}
	}
	body, _ := json.Marshal(map[string]any{
		"type":     "m.login.password",
		"user":     req.MatrixUsername,
		"password": req.MatrixPassword,
		"identifier": map[string]string{
			"type": "m.id.user",
			"user": req.MatrixUsername,
		},
	})
	hc := &http.Client{Timeout: 15 * time.Second}
	resp, err := hc.Post(hs+"/_matrix/client/v3/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return adminResponse{OK: false, Error: fmt.Sprintf("Matrix login request failed: %v", err)}
	}
	defer resp.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrCode     string `json:"errcode"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if result.AccessToken == "" {
		msg := result.Error
		if msg == "" {
			msg = fmt.Sprintf("Matrix returned HTTP %d", resp.StatusCode)
		}
		return adminResponse{OK: false, Error: "Matrix login failed: " + msg}
	}
	config.Server.MatrixHomeserver = hs
	config.Server.MatrixAccessToken = result.AccessToken
	if configFilePath != "" {
		if err := saveConfig(configFilePath); err != nil {
			return adminResponse{OK: false, Error: "token obtained but config save failed: " + err.Error()}
		}
	}
	return adminResponse{OK: true}
}

func adminHeartbeatGet() adminResponse {
	heartbeatMu.RLock()
	status := heartbeatResult
	heartbeatMu.RUnlock()
	status.IntervalMins = config.Server.HeartbeatIntervalMins
	return adminResponse{OK: true, Data: status}
}

func adminHeartbeatSet(req adminRequest) adminResponse {
	if req.HeartbeatIntervalMins <= 0 {
		return adminResponse{OK: false, Error: "interval must be > 0"}
	}
	config.Server.HeartbeatIntervalMins = req.HeartbeatIntervalMins
	if configFilePath != "" {
		if err := saveConfig(configFilePath); err != nil {
			return adminResponse{OK: false, Error: "config save failed: " + err.Error()}
		}
	}
	return adminResponse{OK: true}
}
