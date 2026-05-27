package main

import (
	"encoding/json"
	"fmt"
	"net"
)

type socketRequest struct {
	Cmd       string `json:"cmd"`
	Username  string `json:"username,omitempty"`
	Email     string `json:"email,omitempty"`
	Password  string `json:"password,omitempty"`
	Role      string `json:"role,omitempty"`
	SessionID int    `json:"session_id,omitempty"`
	Path      string `json:"path,omitempty"`
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
