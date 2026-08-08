package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// webauthnInviteProxy proxies invite-scoped WebAuthn calls, injecting the {token} path value.
func webauthnInviteProxy(cfg *Config, client *DansalClient, step string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apiPath := "/api/v1/invites/" + r.PathValue("token") + "/webauthn/" + step
		webauthnProxyDo(cfg, client, apiPath, w, r)
	}
}

// webauthnProxy forwards a request body to the dansal API and returns the JSON
// response as-is. On a successful (2xx) response that carries a session token,
// a web session cookie is set so the user is immediately logged in.
func webauthnProxy(cfg *Config, client *DansalClient, apiPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		webauthnProxyDo(cfg, client, apiPath, w, r)
	}
}

// doProxyPost reads at most 1MB from body, POSTs it as JSON to
// client.BaseURL+apiPath (with rawQuery appended if non-empty), optionally
// setting an Authorization: Bearer header when token is non-empty, and
// returns the upstream response (body already drained into respBody).
func doProxyPost(ctx context.Context, client *DansalClient, apiPath, rawQuery, token string, body []byte) (*http.Response, []byte, error) {
	apiURL := client.BaseURL + apiPath
	if rawQuery != "" {
		apiURL += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp, respBody, nil
}

// webauthnAuthedProxyDo is like webauthnProxyDo but forwards a Bearer token so
// the API endpoint can verify the caller's identity.
func webauthnAuthedProxyDo(cfg *Config, client *DansalClient, apiPath string, token string, w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundJSONBody))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	resp, respBody, err := doProxyPost(r.Context(), client, apiPath, r.URL.RawQuery, token, body)
	if err != nil {
		http.Error(w, "proxy error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

func webauthnProxyDo(cfg *Config, client *DansalClient, apiPath string, w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundJSONBody))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	resp, respBody, err := doProxyPost(r.Context(), client, apiPath, r.URL.RawQuery, "", body)
	if err != nil {
		http.Error(w, "proxy error", http.StatusBadGateway)
		return
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var result struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expires_at"`
			UserID    int    `json:"user_id"`
			Email     string `json:"email"`
			Role      string `json:"role"`
		}
		if json.Unmarshal(respBody, &result) == nil && result.Token != "" {
			expiresAt, errP := time.Parse(time.RFC3339, result.ExpiresAt)
			if errP != nil {
				expiresAt = time.Now().Add(24 * time.Hour)
			}
			setSession(w, result.Token, SessionUser{
				ID:    result.UserID,
				Email: result.Email,
				Role:  result.Role,
			}, expiresAt)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}
