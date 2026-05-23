package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type AdminConfigResponse struct {
	TelegramBotToken      string `json:"telegram_bot_token"`
	TelegramBotName       string `json:"telegram_bot_name"`
	MatrixHomeserver      string `json:"matrix_homeserver"`
	MatrixAccessToken     string `json:"matrix_access_token"`
	HeartbeatIntervalMins int    `json:"heartbeat_interval_mins"`
}

type AdminConfigPatch struct {
	TelegramBotToken      *string `json:"telegram_bot_token"`
	TelegramBotName       *string `json:"telegram_bot_name"`
	MatrixHomeserver      *string `json:"matrix_homeserver"`
	MatrixAccessToken     *string `json:"matrix_access_token"`
	HeartbeatIntervalMins *int    `json:"heartbeat_interval_mins"`
}

func getAdminConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	json.NewEncoder(w).Encode(AdminConfigResponse{
		TelegramBotToken:      config.Server.TelegramBotToken,
		TelegramBotName:       config.Server.TelegramBotName,
		MatrixHomeserver:      config.Server.MatrixHomeserver,
		MatrixAccessToken:     config.Server.MatrixAccessToken,
		HeartbeatIntervalMins: config.Server.HeartbeatIntervalMins,
	})
}

func patchAdminConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	var req AdminConfigPatch
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.TelegramBotToken != nil {
		config.Server.TelegramBotToken = *req.TelegramBotToken
	}
	if req.TelegramBotName != nil {
		config.Server.TelegramBotName = *req.TelegramBotName
	}
	if req.MatrixHomeserver != nil {
		config.Server.MatrixHomeserver = *req.MatrixHomeserver
	}
	if req.MatrixAccessToken != nil {
		config.Server.MatrixAccessToken = *req.MatrixAccessToken
	}
	if req.HeartbeatIntervalMins != nil && *req.HeartbeatIntervalMins > 0 {
		config.Server.HeartbeatIntervalMins = *req.HeartbeatIntervalMins
	}
	if configFilePath != "" {
		if err := saveConfig(configFilePath); err != nil {
			writeError(w, "failed to save config", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// matrixLogin exchanges username+password for an access token via the Matrix
// Client-Server API and stores the token in config (password is never saved).
func matrixLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Homeserver string `json:"homeserver"`
		Username   string `json:"username"`
		Password   string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	hs := strings.TrimRight(strings.TrimSpace(req.Homeserver), "/")
	if hs == "" || req.Username == "" || req.Password == "" {
		writeError(w, "homeserver, username and password are required", http.StatusBadRequest)
		return
	}

	body, _ := json.Marshal(map[string]any{
		"type":     "m.login.password",
		"user":     req.Username,
		"password": req.Password,
		"identifier": map[string]string{
			"type": "m.id.user",
			"user": req.Username,
		},
	})
	hc := &http.Client{Timeout: 15 * time.Second}
	resp, err := hc.Post(hs+"/_matrix/client/v3/login", "application/json", bytes.NewReader(body))
	if err != nil {
		writeError(w, fmt.Sprintf("Matrix login request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		ErrCode     string `json:"errcode"`
		Error       string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if result.AccessToken == "" {
		msg := result.Error
		if msg == "" {
			msg = fmt.Sprintf("Matrix returned HTTP %d", resp.StatusCode)
		}
		writeError(w, "Matrix login failed: "+msg, http.StatusUnauthorized)
		return
	}

	config.Server.MatrixHomeserver = hs
	config.Server.MatrixAccessToken = result.AccessToken
	if configFilePath != "" {
		if err := saveConfig(configFilePath); err != nil {
			writeError(w, "token obtained but config save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
