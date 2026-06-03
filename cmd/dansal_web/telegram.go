package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

var telegramClient = &http.Client{Timeout: 10 * time.Second}

// telegramWebhookProxyHandler forwards Telegram webhook calls to the dansal API
// on loopback, keeping port 8000 off the internet.
func telegramWebhookProxyHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.TelegramWebhookSecret != "" {
			got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
			if !strings.EqualFold(got, cfg.TelegramWebhookSecret) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			log.Printf("telegram proxy: read body: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}

		target := strings.TrimRight(cfg.DansalURL, "/") + "/telegram/webhook"
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			log.Printf("telegram proxy: build request: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		req.Header.Set("Content-Type", r.Header.Get("Content-Type"))

		resp, err := telegramClient.Do(req)
		if err != nil {
			log.Printf("telegram proxy: forward: %v", err)
		} else {
			resp.Body.Close()
		}

		// Always return 200 so Telegram does not retry.
		w.WriteHeader(http.StatusOK)
	}
}
