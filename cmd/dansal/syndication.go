package main

// Multi-platform event syndication framework (#971, #953).
// Stores credentials in organizations.external_syndication (JSON) and
// per-event sync status in events.external_sync (JSON).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

// ── Config types ──────────────────────────────────────────────────────────────

// SyndicationConfig is stored as JSON in organizations.external_syndication.
type SyndicationConfig struct {
	Eventbrite       *EventbriteCfg       `json:"eventbrite,omitempty"`
	SocialDanceToday *SocialDanceTodayCfg `json:"social_dance_today,omitempty"`
}

type EventbriteCfg struct {
	Enabled     bool   `json:"enabled"`
	Token       string `json:"token"`
	OrgID       string `json:"org_id"`
	AutoPublish bool   `json:"auto_publish"`
}

type SocialDanceTodayCfg struct {
	Enabled bool   `json:"enabled"`
	APIKey  string `json:"api_key"`
	OrgSlug string `json:"org_slug"`
}

// ── Sync status types ─────────────────────────────────────────────────────────

// ExternalSync is stored as JSON in events.external_sync.
type ExternalSync struct {
	Eventbrite       *PlatformSyncStatus `json:"eventbrite,omitempty"`
	SocialDanceToday *PlatformSyncStatus `json:"social_dance_today,omitempty"`
}

type PlatformSyncStatus struct {
	Status     string `json:"status"` // "pending", "synced", "error"
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
	SyncedAt   string `json:"synced_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ── DB helpers ────────────────────────────────────────────────────────────────

func loadSyndicationConfig(orgID int) (*SyndicationConfig, error) {
	var raw string
	err := db.QueryRow(
		"SELECT COALESCE(external_syndication,'{}') FROM organizations WHERE id=?", orgID,
	).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var cfg SyndicationConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return &SyndicationConfig{}, nil
	}
	return &cfg, nil
}

func saveSyndicationConfig(orgID int, cfg *SyndicationConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		"UPDATE organizations SET external_syndication=? WHERE id=?", string(b), orgID,
	)
	return err
}

func loadEventSync(eventID int) (*ExternalSync, error) {
	var raw string
	err := db.QueryRow(
		"SELECT COALESCE(external_sync,'{}') FROM events WHERE id=?", eventID,
	).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var s ExternalSync
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return &ExternalSync{}, nil
	}
	return &s, nil
}

func saveEventSync(eventID int, s *ExternalSync) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		"UPDATE events SET external_sync=? WHERE id=?", string(b), eventID,
	)
	return err
}

// ── API handlers ──────────────────────────────────────────────────────────────

// GET /api/v1/organizations/{id}/syndication
func getSyndicationConfig(w http.ResponseWriter, r *http.Request) {
	_, role := callerFromRequest(r)
	if role != RoleAdmin && role != RoleUser {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	cfg, err := loadSyndicationConfig(id)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	// Redact secrets before sending to the browser — expose only whether a
	// credential is set (has_token / has_key booleans) rather than the value.
	type eventbriteOut struct {
		HasToken    bool   `json:"has_token"`
		OrgID       string `json:"org_id"`
		AutoPublish bool   `json:"auto_publish"`
	}
	type sdtOut struct {
		HasKey  bool   `json:"has_key"`
		OrgSlug string `json:"org_slug"`
	}
	type out struct {
		Eventbrite       *eventbriteOut `json:"eventbrite,omitempty"`
		SocialDanceToday *sdtOut        `json:"social_dance_today,omitempty"`
	}
	var o out
	if cfg.Eventbrite != nil {
		o.Eventbrite = &eventbriteOut{
			HasToken:    cfg.Eventbrite.Token != "",
			OrgID:       cfg.Eventbrite.OrgID,
			AutoPublish: cfg.Eventbrite.AutoPublish,
		}
	}
	if cfg.SocialDanceToday != nil {
		o.SocialDanceToday = &sdtOut{
			HasKey:  cfg.SocialDanceToday.APIKey != "",
			OrgSlug: cfg.SocialDanceToday.OrgSlug,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(o)
}

// PUT /api/v1/organizations/{id}/syndication
func putSyndicationConfig(w http.ResponseWriter, r *http.Request) {
	_, role := callerFromRequest(r)
	if role != RoleAdmin && role != RoleUser {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var incoming SyndicationConfig
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&incoming); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	// Merge: load existing config and preserve credentials that arrive blank
	// (the admin form sends empty strings when the user hasn't touched the field).
	existing, _ := loadSyndicationConfig(id)
	if existing == nil {
		existing = &SyndicationConfig{}
	}
	if incoming.Eventbrite != nil {
		if existing.Eventbrite == nil {
			existing.Eventbrite = &EventbriteCfg{}
		}
		if incoming.Eventbrite.Token != "" {
			existing.Eventbrite.Token = incoming.Eventbrite.Token
		}
		existing.Eventbrite.OrgID       = incoming.Eventbrite.OrgID
		existing.Eventbrite.AutoPublish = incoming.Eventbrite.AutoPublish
		existing.Eventbrite.Enabled     = incoming.Eventbrite.OrgID != "" && existing.Eventbrite.Token != ""
	}
	if incoming.SocialDanceToday != nil {
		if existing.SocialDanceToday == nil {
			existing.SocialDanceToday = &SocialDanceTodayCfg{}
		}
		if incoming.SocialDanceToday.APIKey != "" {
			existing.SocialDanceToday.APIKey = incoming.SocialDanceToday.APIKey
		}
		if incoming.SocialDanceToday.OrgSlug != "" {
			existing.SocialDanceToday.OrgSlug = incoming.SocialDanceToday.OrgSlug
		}
		existing.SocialDanceToday.Enabled = existing.SocialDanceToday.APIKey != ""
	}
	if err := saveSyndicationConfig(id, existing); err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
}

// GET /api/v1/events/{id}/syndication
func getEventSyncStatus(w http.ResponseWriter, r *http.Request) {
	_, role := callerFromRequest(r)
	if role != RoleAdmin && role != RoleUser && role != RolePublisher {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	s, err := loadEventSync(id)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

// POST /api/v1/events/{id}/syndicate/eventbrite
func syndicateToEventbrite(w http.ResponseWriter, r *http.Request) {
	_, role := callerFromRequest(r)
	if role != RoleAdmin && role != RoleUser && role != RolePublisher {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Fetch event and its org.
	event, err := scanEventRow(db.QueryRow(eventListSelect+" WHERE e.id=? AND e.email_verified=1", id))
	if err != nil {
		writeError(w, "event not found", http.StatusNotFound)
		return
	}
	if event.OrganizationID == nil {
		writeError(w, "event has no organization", http.StatusUnprocessableEntity)
		return
	}

	synCfg, err := loadSyndicationConfig(*event.OrganizationID)
	if err != nil || synCfg.Eventbrite == nil || !synCfg.Eventbrite.Enabled {
		writeError(w, "Eventbrite not configured for this organization", http.StatusUnprocessableEntity)
		return
	}
	eb := synCfg.Eventbrite

	// Mark as pending immediately so the UI shows progress.
	sync, _ := loadEventSync(id)
	if sync == nil {
		sync = &ExternalSync{}
	}
	sync.Eventbrite = &PlatformSyncStatus{Status: "pending", SyncedAt: time.Now().UTC().Format(time.RFC3339)}
	saveEventSync(id, sync)

	go publishToEventbrite(id, event, eb)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
}

// POST /api/v1/events/{id}/syndicate/social-dance-today
func syndicateToSocialDanceToday(w http.ResponseWriter, r *http.Request) {
	_, role := callerFromRequest(r)
	if role != RoleAdmin && role != RoleUser && role != RolePublisher {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	event, err := scanEventRow(db.QueryRow(eventListSelect+" WHERE e.id=? AND e.email_verified=1", id))
	if err != nil {
		writeError(w, "event not found", http.StatusNotFound)
		return
	}
	if event.OrganizationID == nil {
		writeError(w, "event has no organization", http.StatusUnprocessableEntity)
		return
	}

	synCfg, err := loadSyndicationConfig(*event.OrganizationID)
	if err != nil || synCfg.SocialDanceToday == nil || !synCfg.SocialDanceToday.Enabled {
		writeError(w, "social-dance.today not configured for this organization", http.StatusUnprocessableEntity)
		return
	}
	sdt := synCfg.SocialDanceToday

	sync, _ := loadEventSync(id)
	if sync == nil {
		sync = &ExternalSync{}
	}
	sync.SocialDanceToday = &PlatformSyncStatus{Status: "pending", SyncedAt: time.Now().UTC().Format(time.RFC3339)}
	saveEventSync(id, sync)

	go pushToSocialDanceToday(id, event, sdt)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
}

// ── Eventbrite integration ────────────────────────────────────────────────────

// publishToEventbrite runs in a goroutine: creates a draft event, adds a free
// ticket class, and optionally publishes it. Results are stored in external_sync.
// Follows Eventbrite API v3 spec in issue #971.
func publishToEventbrite(eventID int, event Event, cfg *EventbriteCfg) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fail := func(msg string) {
		log.Printf("eventbrite sync event %d: %s", eventID, msg)
		sync, _ := loadEventSync(eventID)
		if sync == nil {
			sync = &ExternalSync{}
		}
		sync.Eventbrite = &PlatformSyncStatus{
			Status:   "error",
			Error:    msg,
			SyncedAt: time.Now().UTC().Format(time.RFC3339),
		}
		saveEventSync(eventID, sync)
	}

	// StartTime/EndTime are already in RFC3339 or "YYYY-MM-DD HH:MM:SS" format;
	// pass through directly. Eventbrite expects UTC RFC3339.
	startStr := event.StartTime
	endStr := event.EndTime

	// Step 1: Create draft event.
	ebPayload := map[string]any{
		"event": map[string]any{
			"name":        map[string]string{"html": event.Title},
			"description": map[string]string{"html": event.Description},
			"start":       map[string]string{"utc": startStr, "timezone": "UTC"},
			"end":         map[string]string{"utc": endStr, "timezone": "UTC"},
			"currency":    "EUR",
		},
	}
	body, _ := json.Marshal(ebPayload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("https://www.eventbriteapi.com/v3/organizations/%s/events/", cfg.OrgID),
		bytes.NewReader(body))
	if err != nil {
		fail("build request: " + err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("create draft: " + err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		fail(fmt.Sprintf("create draft: HTTP %d", resp.StatusCode))
		return
	}
	var created struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil || created.ID == "" {
		fail("parse create response")
		return
	}

	// Step 2: Add a free ticket class.
	ticketPayload := map[string]any{
		"ticket_class": map[string]any{
			"name":     "General Admission",
			"free":     true,
			"quantity_total": 100,
		},
	}
	body, _ = json.Marshal(ticketPayload)
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("https://www.eventbriteapi.com/v3/events/%s/ticket_classes/", created.ID),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	if resp2, err := http.DefaultClient.Do(req); err == nil {
		resp2.Body.Close()
	}

	// Step 3: Publish if configured.
	if cfg.AutoPublish {
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost,
			fmt.Sprintf("https://www.eventbriteapi.com/v3/events/%s/publish/", created.ID),
			nil)
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		if resp3, err := http.DefaultClient.Do(req); err == nil {
			resp3.Body.Close()
		}
	}

	// Store success.
	sync, _ := loadEventSync(eventID)
	if sync == nil {
		sync = &ExternalSync{}
	}
	status := "synced"
	if !cfg.AutoPublish {
		status = "draft"
	}
	sync.Eventbrite = &PlatformSyncStatus{
		Status:     status,
		ExternalID: created.ID,
		URL:        created.URL,
		SyncedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	saveEventSync(eventID, sync)
	log.Printf("eventbrite sync event %d: created %s (status=%s)", eventID, created.ID, status)
}

// ── social-dance.today integration ───────────────────────────────────────────

// pushToSocialDanceToday runs in a goroutine and POSTs the event in a minimal
// folkdance-json payload to the configured endpoint with Bearer auth.
func pushToSocialDanceToday(eventID int, event Event, cfg *SocialDanceTodayCfg) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fail := func(msg string) {
		log.Printf("social-dance.today sync event %d: %s", eventID, msg)
		sync, _ := loadEventSync(eventID)
		if sync == nil {
			sync = &ExternalSync{}
		}
		sync.SocialDanceToday = &PlatformSyncStatus{
			Status:   "error",
			Error:    msg,
			SyncedAt: time.Now().UTC().Format(time.RFC3339),
		}
		saveEventSync(eventID, sync)
	}

	endpoint := "https://api.social-dance.today/v1/events"
	_ = cfg.OrgSlug // OrgSlug identifies the org on social-dance.today; the endpoint is fixed

	startStr := event.StartTime
	endStr := event.EndTime

	// folkdance-json minimal payload.
	locPayload := map[string]any{}
	if event.Location != nil {
		locPayload["name"] = event.Location.Location
		locPayload["address"] = event.Location.Address
		locPayload["city"] = event.Location.Town
		locPayload["country"] = event.Location.Country
		locPayload["lat"] = event.Location.Latitude
		locPayload["lng"] = event.Location.Longitude
	}
	payload := map[string]any{
		"name":        event.Title,
		"start_time":  startStr,
		"end_time":    endStr,
		"url":         event.URL,
		"location":    locPayload,
		"tags":        event.Tags,
		"description": event.Description,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fail("build request: " + err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("post: " + err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		fail(fmt.Sprintf("HTTP %d", resp.StatusCode))
		return
	}

	var result struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	sync, _ := loadEventSync(eventID)
	if sync == nil {
		sync = &ExternalSync{}
	}
	sync.SocialDanceToday = &PlatformSyncStatus{
		Status:     "synced",
		ExternalID: result.ID,
		URL:        result.URL,
		SyncedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	saveEventSync(eventID, sync)
	log.Printf("social-dance.today sync event %d: synced (external_id=%s)", eventID, result.ID)
}
