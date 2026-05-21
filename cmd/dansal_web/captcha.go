package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// verifyTurnstile checks a Cloudflare Turnstile response token.
// Returns nil if valid or if no secret key is configured.
func verifyTurnstile(cfg *Config, token string) error {
	if cfg.CaptchaSecretKey == "" {
		return nil
	}
	resp, err := http.PostForm(turnstileVerifyURL, url.Values{
		"secret":   {cfg.CaptchaSecretKey},
		"response": {token},
	})
	if err != nil {
		return fmt.Errorf("captcha: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		Success bool     `json:"success"`
		Errors  []string `json:"error-codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("captcha: decode: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("captcha failed: %s", strings.Join(result.Errors, ", "))
	}
	return nil
}
