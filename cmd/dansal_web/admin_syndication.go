package main

// Admin proxy handlers for the multi-platform syndication framework (#971, #953).
// These forward requests to the dansal API and return JSON to the admin UI.

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// GET /admin/orgs/{id}/syndication — fetch current syndication config.
func adminSyndicationGetHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		synCfg, err := client.GetSyndicationConfig(r.Context(), id, token)
		if err != nil {
			http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(synCfg)
	}
}

// POST /admin/orgs/{id}/syndication — save syndication config.
func adminSyndicationSaveHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		var synCfg SyndicationConfig
		if err := json.NewDecoder(r.Body).Decode(&synCfg); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := client.PutSyndicationConfig(r.Context(), id, token, synCfg); err != nil {
			http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
	}
}

// POST /admin/events/{id}/syndicate/{platform} — trigger sync for one platform.
func adminSyndicatePlatformHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		platform := r.PathValue("platform")
		token := getSessionToken(r)
		if err := client.SyndicateTo(r.Context(), id, platform, token); err != nil {
			http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
	}
}

// GET /admin/events/{id}/syndication — fetch current sync status.
func adminGetSyncStatusHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		s, err := client.GetEventSyncStatus(r.Context(), id, token)
		if err != nil {
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s)
	}
}
