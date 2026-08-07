package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
)

type socketRequest struct {
	Cmd                   string `json:"cmd"`
	Email                 string `json:"email,omitempty"`
	Password              string `json:"password,omitempty"`
	Role                  string `json:"role,omitempty"`
	SessionID             int    `json:"session_id,omitempty"`
	Path                  string `json:"path,omitempty"`
	SMTPHost              string `json:"smtp_host,omitempty"`
	SMTPPort              int    `json:"smtp_port,omitempty"`
	SMTPUsername          string `json:"smtp_username,omitempty"`
	SMTPFrom              string `json:"smtp_from,omitempty"`
	SMTPFromName          string `json:"smtp_from_name,omitempty"`
	SMTPTLS               string `json:"smtp_tls,omitempty"`
	SMTPTimeoutSecs       int    `json:"smtp_timeout_secs,omitempty"`
	SMTPTo                string `json:"smtp_to,omitempty"`
	SMTPSendmail          string `json:"smtp_sendmail,omitempty"`
	TelegramBotToken      string `json:"telegram_bot_token,omitempty"`
	TelegramBotName       string `json:"telegram_bot_name,omitempty"`
	MatrixHomeserver      string `json:"matrix_homeserver,omitempty"`
	MatrixAccessToken     string `json:"matrix_access_token,omitempty"`
	MatrixUsername        string `json:"matrix_username,omitempty"`
	MatrixPassword        string `json:"matrix_password,omitempty"`
	HeartbeatIntervalMins int    `json:"heartbeat_interval_mins,omitempty"`
}

type socketResponse struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

func sendSocket(socketPath string, req socketRequest) (socketResponse, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return socketResponse{}, fmt.Errorf("connect to %s: %w", socketPath, err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return socketResponse{}, fmt.Errorf("send: %w", err)
	}
	var resp socketResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return socketResponse{}, fmt.Errorf("recv: %w", err)
	}
	return resp, nil
}

// getSocketData performs a socket request expecting a JSON data payload,
// returning an error on transport failure, a non-OK response, or invalid data.
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

// doSocket performs a socket request and collapses the two failure modes
// (transport error, non-OK response) into a single error, so callers no
// longer repeat the `err != nil || !resp.OK` dance. Its error text matches
// the historical "socket error" fallback: transport failures surface as the
// generic "socket error", non-OK responses carry resp.Error. Handlers that
// want the real transport error for a log line should use socketFlashRedirect
// (which logs it) or sendSocket directly.
func doSocket(cfg *Config, req socketRequest) (socketResponse, error) {
	resp, err := sendSocket(cfg.AdminSocket, req)
	if err != nil {
		return resp, fmt.Errorf("socket error")
	}
	if !resp.OK {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

// socketErrorMessage reproduces the historical error-flash text: transport
// failures surface as the generic "socket error" label, non-OK responses show
// the server's resp.Error.
func socketErrorMessage(err error, resp socketResponse) string {
	if err != nil {
		return "socket error"
	}
	return resp.Error
}

// socketFlashRedirect runs req against the admin socket and, on failure, logs
// the real error and redirects to basePath with an "<errLabel>: <message>"
// flash. On success it returns the raw response without touching the writer,
// so handlers can unmarshal resp.Data and issue their own success redirect.
func socketFlashRedirect(w http.ResponseWriter, r *http.Request, cfg *Config, basePath, errLabel string, req socketRequest) (socketResponse, bool) {
	resp, err := sendSocket(cfg.AdminSocket, req)
	if err != nil || !resp.OK {
		msg := socketErrorMessage(err, resp)
		log.Printf("%s: %v / %s", errLabel, err, msg)
		http.Redirect(w, r, basePath+"?flash="+url.QueryEscape(errLabel+": "+msg), http.StatusSeeOther)
		return resp, false
	}
	return resp, true
}
