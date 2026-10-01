package main

// Admin proxy handlers for the multi-platform syndication framework (#971, #953).
// These forward requests to the dansal API and return JSON to the admin UI.

import (
	"encoding/json"
	"net/http"
)

// requireSyndicationEnabled (#1409) is the single guard all 4 syndication
// routes check first — the feature hasn't been tested end-to-end yet, so
// it's off by default (web.yaml's enable_syndication, Go zero value false)
// until explicitly opted into per-instance. Writes 404 and returns false
// when disabled, same convention as requireExistingOrgMember etc.
func requireSyndicationEnabled(w http.ResponseWriter, cfg *Config) bool {
	if !cfg.EnableSyndication {
		http.Error(w, "syndication is not enabled on this instance", http.StatusNotFound)
		return false
	}
	return true
}

// GET /admin/orgs/{id}/syndication — fetch current syndication config.
func adminSyndicationGetHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireSyndicationEnabled(w, cfg) {
			return
		}
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, ok := intPathValueOr400(w, r, "id", "invalid id")
		if !ok {
			return
		}
		if !canManageOrg(su, id, memberOrgSet(r, client, su)) {
			forbidden(w, r)
			return
		}
		token := getSessionToken(r)
		synCfg, err := client.GetSyndicationConfig(r.Context(), id, token)
		if err != nil {
			http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSONResponse(w, http.StatusOK, synCfg)
	}
}

// POST /admin/orgs/{id}/syndication — save syndication config.
func adminSyndicationSaveHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireSyndicationEnabled(w, cfg) {
			return
		}
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, ok := intPathValueOr400(w, r, "id", "invalid id")
		if !ok {
			return
		}
		if !canManageOrg(su, id, memberOrgSet(r, client, su)) {
			forbidden(w, r)
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
		writeJSONResponse(w, http.StatusOK, map[string]string{"status": "saved"})
	}
}

// POST /admin/events/{id}/syndicate/{platform} — trigger sync for one platform.
func adminSyndicatePlatformHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireSyndicationEnabled(w, cfg) {
			return
		}
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, ok := intPathValueOr400(w, r, "id", "invalid id")
		if !ok {
			return
		}
		platform := r.PathValue("platform")
		token := getSessionToken(r)
		event, err := client.GetEvent(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !userCanManageEvent(r, su, event, client, token) {
			forbidden(w, r)
			return
		}
		if err := client.SyndicateTo(r.Context(), id, platform, token); err != nil {
			http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]string{"status": "pending"})
	}
}

// GET /admin/events/{id}/syndication — fetch current sync status.
func adminGetSyncStatusHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireSyndicationEnabled(w, cfg) {
			return
		}
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, ok := intPathValueOr400(w, r, "id", "invalid id")
		if !ok {
			return
		}
		token := getSessionToken(r)
		event, err := client.GetEvent(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !userCanManageEvent(r, su, event, client, token) {
			forbidden(w, r)
			return
		}
		s, err := client.GetEventSyncStatus(r.Context(), id, token)
		if err != nil {
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		writeJSONResponse(w, http.StatusOK, s)
	}
}
