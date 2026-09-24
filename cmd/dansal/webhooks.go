package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// #1370: publisher webhook subscriptions + producer. A publisher registers an
// HTTPS URL; on relevant event changes dansal POSTs a tiny signed pointer
// (never the resource body) so a client like wp-dansal can pull just that
// event within seconds instead of waiting for its next poll. Signed with the
// same scheme as #1366 API writes, using the publisher's signing secret.

const maxWebhooksPerPublisher = 5

var webhookEventTypes = map[string]bool{
	"event.create": true, "event.update": true, "event.publish": true,
	"event.cancel": true, "event.delete": true,
}

// webhookClient delivers webhooks. It blocks private/loopback targets at dial
// time (not just at create time, so DNS rebinding can't bypass the create-time
// check) and never follows redirects. Tests swap it for a plain client.
var webhookClient = &http.Client{
	Transport:     &http.Transport{DialContext: safeDialContext},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type PublisherWebhook struct {
	ID                  int    `json:"id"`
	PublisherID         int    `json:"publisher_id"`
	URL                 string `json:"url"`
	EventTypes          string `json:"event_types"`
	Active              bool   `json:"active"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	LastDeliveryAt      *int64 `json:"last_delivery_at,omitempty"`
	LastError           string `json:"last_error,omitempty"`
	CreatedAt           int64  `json:"created_at"`
	UpdatedAt           int64  `json:"updated_at"`
}

const webhookCols = "id, publisher_id, url, event_types, active, consecutive_failures, last_delivery_at, last_error, created_at, updated_at"

func scanWebhook(sc interface{ Scan(...any) error }) (PublisherWebhook, error) {
	var wh PublisherWebhook
	var active int
	var last sql.NullInt64
	err := sc.Scan(&wh.ID, &wh.PublisherID, &wh.URL, &wh.EventTypes, &active, &wh.ConsecutiveFailures, &last, &wh.LastError, &wh.CreatedAt, &wh.UpdatedAt)
	wh.Active = active != 0
	if last.Valid {
		v := last.Int64
		wh.LastDeliveryAt = &v
	}
	return wh, err
}

// ── config accessors ───────────────────────────────────────────────────────

func webhookTimeout() time.Duration {
	if config.Server.Webhooks.TimeoutSecs > 0 {
		return time.Duration(config.Server.Webhooks.TimeoutSecs) * time.Second
	}
	return 10 * time.Second
}

func webhookMaxURLLength() int {
	if config.Server.Webhooks.MaxURLLength > 0 {
		return config.Server.Webhooks.MaxURLLength
	}
	return 2048
}

func webhookBackoff() []time.Duration {
	secs := config.Server.Webhooks.RetryBackoffSecs
	if len(secs) == 0 {
		secs = []int{30, 300, 1800}
	}
	out := make([]time.Duration, len(secs))
	for i, s := range secs {
		out[i] = time.Duration(s) * time.Second
	}
	return out
}

func webhookDisableAfter() int {
	if config.Server.Webhooks.DisableAfterFailures > 0 {
		return config.Server.Webhooks.DisableAfterFailures
	}
	return 4
}

// ── validation ─────────────────────────────────────────────────────────────

// validateWebhookURL enforces HTTPS, no credentials, a length cap, and — at
// create/patch time — that the host doesn't resolve to a private/internal
// address. Delivery re-checks at dial time via safeDialContext.
func validateWebhookURL(raw string) error {
	if len(raw) > webhookMaxURLLength() {
		return fmt.Errorf("url too long (max %d)", webhookMaxURLLength())
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid url")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("url must use https")
	}
	if u.User != nil {
		return fmt.Errorf("url must not contain credentials")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("url points to a private/internal address")
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("url host does not resolve")
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("url points to a private/internal address")
		}
	}
	return nil
}

// normalizeWebhookEventTypes validates a CSV of event types ("*" = all) and
// returns it trimmed.
func normalizeWebhookEventTypes(csv string) (string, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" || csv == "*" {
		return "*", nil
	}
	var out []string
	for _, t := range strings.Split(csv, ",") {
		t = strings.TrimSpace(t)
		if !webhookEventTypes[t] {
			return "", fmt.Errorf("unknown event type %q", t)
		}
		out = append(out, t)
	}
	return strings.Join(out, ","), nil
}

func webhookWantsType(csv, eventType string) bool {
	if csv == "*" {
		return true
	}
	for _, t := range strings.Split(csv, ",") {
		if strings.TrimSpace(t) == eventType {
			return true
		}
	}
	return false
}

// ── delivery ───────────────────────────────────────────────────────────────

// publisherSigningSecret returns the plaintext signing secret of the
// publisher's newest key that has one.
func publisherSigningSecret(publisherID int) (string, error) {
	var enc string
	err := db.QueryRow(
		"SELECT signing_secret_enc FROM api_keys WHERE user_id=? AND signing_secret_enc IS NOT NULL AND signing_secret_enc != '' ORDER BY id DESC LIMIT 1",
		publisherID,
	).Scan(&enc)
	if err != nil {
		return "", fmt.Errorf("publisher has no signing secret")
	}
	return signingSecretDecrypt(enc)
}

func newDeliveryID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// postWebhook performs one signed delivery attempt. Returns the HTTP status
// (0 on transport error) and an error for anything other than 2xx.
func postWebhook(targetURL, secret string, body []byte) (int, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return 0, err
	}
	nonceBytes := make([]byte, 16)
	rand.Read(nonceBytes)
	nonce := hex.EncodeToString(nonceBytes)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sum := sha256.Sum256(body)
	payload := canonicalSigningPayload(http.MethodPost, u.Path, u.RawQuery, ts, hex.EncodeToString(sum[:]), nonce)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))

	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "dansal-webhooks/1.0")
	req.Header.Set("X-Wpd-Timestamp", ts)
	req.Header.Set("X-Wpd-Nonce", nonce)
	req.Header.Set("X-Wpd-Signature", hex.EncodeToString(mac.Sum(nil)))

	resp, err := webhookClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, nil
	}
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(excerpt)))
}

// deliverWebhook delivers body to one subscription with in-process retry
// (default 30s, 5m, 30m). No persistent queue: a restart mid-retry drops the
// delivery and the client's pull fallback covers the gap. Must run in its own
// goroutine.
func deliverWebhook(webhookID int, body []byte) {
	backoff := webhookBackoff()
	for attempt := 0; ; attempt++ {
		var pubID int
		var target string
		var active int
		if err := db.QueryRow("SELECT publisher_id, url, active FROM publisher_webhooks WHERE id=?", webhookID).Scan(&pubID, &target, &active); err != nil || active == 0 {
			return // deleted or paused since the event fired
		}
		secret, err := publisherSigningSecret(pubID)
		if err != nil {
			recordWebhookFailure(webhookID, err.Error())
			return
		}
		_, err = postWebhook(target, secret, body)
		if err == nil {
			db.Exec("UPDATE publisher_webhooks SET consecutive_failures=0, last_error='', last_delivery_at=? WHERE id=?", time.Now().Unix(), webhookID)
			return
		}
		if disabled := recordWebhookFailure(webhookID, err.Error()); disabled || attempt >= len(backoff) {
			return
		}
		time.Sleep(backoff[attempt])
	}
}

// recordWebhookFailure bumps consecutive_failures and disables the
// subscription once it reaches the configured threshold. Returns whether it
// was disabled.
func recordWebhookFailure(webhookID int, msg string) bool {
	if len(msg) > 300 {
		msg = msg[:300]
	}
	db.Exec("UPDATE publisher_webhooks SET consecutive_failures=consecutive_failures+1, last_error=?, updated_at=? WHERE id=?", msg, time.Now().Unix(), webhookID)
	var n int
	db.QueryRow("SELECT consecutive_failures FROM publisher_webhooks WHERE id=?", webhookID).Scan(&n)
	if n >= webhookDisableAfter() {
		db.Exec("UPDATE publisher_webhooks SET active=0 WHERE id=?", webhookID)
		return true
	}
	return false
}

type webhookPayload struct {
	Event           string `json:"event"`
	Resource        string `json:"resource,omitempty"`
	ResourceID      int    `json:"resource_id,omitempty"`
	Action          string `json:"action,omitempty"`
	ChangedAt       string `json:"changed_at,omitempty"`
	OrganizationID  *int   `json:"organization_id,omitempty"`
	ChangedByUserID int    `json:"changed_by_user_id,omitempty"`
	DeliveryID      string `json:"delivery_id"`
	EmittedAt       string `json:"emitted_at"`
}

// dispatchEventWebhooks fans an event change out to every active subscription
// of a publisher who belongs to the event's organization and opted into this
// event type. Fire-and-forget: each delivery runs in its own goroutine so the
// caller's request path never waits on a subscriber. actorID (the user whose
// action caused this) is included so a client can skip its own echo.
func dispatchEventWebhooks(eventID int, orgID *int, action string, actorID int) {
	if config == nil || !config.Server.Webhooks.Enabled || orgID == nil {
		return
	}
	eventType := "event." + action
	rows, err := db.Query(
		`SELECT w.id, w.event_types FROM publisher_webhooks w
		 JOIN organization_members om ON om.user_id = w.publisher_id
		 WHERE w.active = 1 AND om.organization_id = ?`, *orgID)
	if err != nil {
		log.Printf("webhooks: recipient lookup: %v", err)
		return
	}
	defer rows.Close()

	var changedEpoch int64
	db.QueryRow("SELECT COALESCE(changed_at,0) FROM events WHERE id=?", eventID).Scan(&changedEpoch)
	now := time.Now().UTC()
	changed := now
	if changedEpoch > 0 {
		changed = time.Unix(changedEpoch, 0).UTC()
	}

	for rows.Next() {
		var id int
		var types string
		if rows.Scan(&id, &types) != nil || !webhookWantsType(types, eventType) {
			continue
		}
		body, _ := json.Marshal(webhookPayload{
			Event: eventType, Resource: "event", ResourceID: eventID, Action: action,
			ChangedAt: changed.Format(time.RFC3339), OrganizationID: orgID,
			ChangedByUserID: actorID, DeliveryID: newDeliveryID(), EmittedAt: now.Format(time.RFC3339),
		})
		go deliverWebhook(id, body)
	}
}

// emitEventWebhook looks up the event's organization and dispatches. Use
// dispatchEventWebhooks directly when the org must be captured beforehand
// (deletes).
func emitEventWebhook(eventID int, action string, actorID int) {
	if config == nil || !config.Server.Webhooks.Enabled {
		return
	}
	var org sql.NullInt64
	if err := db.QueryRow("SELECT organization_id FROM events WHERE id=?", eventID).Scan(&org); err != nil || !org.Valid {
		return
	}
	o := int(org.Int64)
	dispatchEventWebhooks(eventID, &o, action, actorID)
}

// ── endpoints ──────────────────────────────────────────────────────────────

// authorizeWebhookPublisher resolves {id} and checks the caller is that
// publisher or an admin. Returns the publisher id.
func authorizeWebhookPublisher(w http.ResponseWriter, r *http.Request) (int, bool) {
	pubID, ok := requireIntPathValue(w, r, "id", "invalid id")
	if !ok {
		return 0, false
	}
	callerID, role := callerFromRequest(r)
	if role != RoleAdmin && !(role == RolePublisher && callerID == pubID) {
		writeError(w, "Forbidden", http.StatusForbidden)
		return 0, false
	}
	var targetRole string
	if err := db.QueryRow("SELECT role FROM users WHERE id=?", pubID).Scan(&targetRole); err != nil || targetRole != RolePublisher {
		writeError(w, "publisher not found", http.StatusNotFound)
		return 0, false
	}
	return pubID, true
}

// GET /api/v1/publishers/{id}/webhooks
func listPublisherWebhooks(w http.ResponseWriter, r *http.Request) {
	pubID, ok := authorizeWebhookPublisher(w, r)
	if !ok {
		return
	}
	rows, err := db.Query("SELECT "+webhookCols+" FROM publisher_webhooks WHERE publisher_id=? ORDER BY id", pubID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	out := []PublisherWebhook{}
	for rows.Next() {
		wh, err := scanWebhook(rows)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		out = append(out, wh)
	}
	writeJSON(w, out)
}

// POST /api/v1/publishers/{id}/webhooks
func createPublisherWebhook(w http.ResponseWriter, r *http.Request) {
	pubID, ok := authorizeWebhookPublisher(w, r)
	if !ok {
		return
	}
	var req struct {
		URL        string `json:"url"`
		EventTypes string `json:"event_types"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if err := validateWebhookURL(req.URL); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	types, err := normalizeWebhookEventTypes(req.EventTypes)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Deliveries are signed with the publisher's signing secret; without one
	// every delivery would fail, so refuse up front.
	if _, err := publisherSigningSecret(pubID); err != nil {
		writeError(w, "publisher has no signing secret; enable server.signing and reissue the key first", http.StatusBadRequest)
		return
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM publisher_webhooks WHERE publisher_id=?", pubID).Scan(&count)
	if count >= maxWebhooksPerPublisher {
		writeError(w, fmt.Sprintf("at most %d webhooks per publisher", maxWebhooksPerPublisher), http.StatusConflict)
		return
	}
	now := time.Now().Unix()
	res, err := db.Exec(
		"INSERT INTO publisher_webhooks (publisher_id, url, event_types, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		pubID, req.URL, types, now, now)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	id, _ := res.LastInsertId()
	wh, err := scanWebhook(db.QueryRow("SELECT "+webhookCols+" FROM publisher_webhooks WHERE id=?", id))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	// Immediate ping so the client can verify signature handling end to end.
	if config.Server.Webhooks.Enabled {
		go deliverWebhook(int(id), pingBody())
	}
	writeJSONStatus(w, http.StatusCreated, wh)
}

func pingBody() []byte {
	now := time.Now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(webhookPayload{Event: "ping", DeliveryID: newDeliveryID(), EmittedAt: now})
	return b
}

// PATCH /api/v1/publishers/{id}/webhooks/{webhook_id} (merge-patch)
func patchPublisherWebhook(w http.ResponseWriter, r *http.Request) {
	pubID, ok := authorizeWebhookPublisher(w, r)
	if !ok {
		return
	}
	whID, ok := requireIntPathValue(w, r, "webhook_id", "invalid webhook id")
	if !ok {
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/merge-patch+json" {
		writeError(w, "PATCH requires Content-Type: application/merge-patch+json", http.StatusUnsupportedMediaType)
		return
	}
	var req struct {
		URL        *string `json:"url"`
		EventTypes *string `json:"event_types"`
		Active     *bool   `json:"active"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	cur, err := scanWebhook(db.QueryRow("SELECT "+webhookCols+" FROM publisher_webhooks WHERE id=? AND publisher_id=?", whID, pubID))
	if err == sql.ErrNoRows {
		writeError(w, "webhook not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	if req.URL != nil {
		if err := validateWebhookURL(*req.URL); err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		cur.URL = *req.URL
	}
	if req.EventTypes != nil {
		t, err := normalizeWebhookEventTypes(*req.EventTypes)
		if err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		cur.EventTypes = t
	}
	if req.Active != nil {
		if *req.Active && !cur.Active {
			cur.ConsecutiveFailures = 0 // manual reactivation starts fresh
			cur.LastError = ""
		}
		cur.Active = *req.Active
	}
	active := 0
	if cur.Active {
		active = 1
	}
	if _, err := db.Exec(
		"UPDATE publisher_webhooks SET url=?, event_types=?, active=?, consecutive_failures=?, last_error=?, updated_at=? WHERE id=?",
		cur.URL, cur.EventTypes, active, cur.ConsecutiveFailures, cur.LastError, time.Now().Unix(), whID); err != nil {
		writeInternalError(w, err)
		return
	}
	cur, _ = scanWebhook(db.QueryRow("SELECT "+webhookCols+" FROM publisher_webhooks WHERE id=?", whID))
	writeJSON(w, cur)
}

// DELETE /api/v1/publishers/{id}/webhooks/{webhook_id}
func deletePublisherWebhook(w http.ResponseWriter, r *http.Request) {
	pubID, ok := authorizeWebhookPublisher(w, r)
	if !ok {
		return
	}
	whID, ok := requireIntPathValue(w, r, "webhook_id", "invalid webhook id")
	if !ok {
		return
	}
	res, err := db.Exec("DELETE FROM publisher_webhooks WHERE id=? AND publisher_id=?", whID, pubID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, "webhook not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/publishers/{id}/webhooks/{webhook_id}/test — one synchronous
// ping, reporting dansal's view of the response. Works even while
// server.webhooks.enabled is false (that's what lets an admin validate a
// subscription before enabling the producer) and doesn't touch the failure
// counters.
func testPublisherWebhook(w http.ResponseWriter, r *http.Request) {
	pubID, ok := authorizeWebhookPublisher(w, r)
	if !ok {
		return
	}
	whID, ok := requireIntPathValue(w, r, "webhook_id", "invalid webhook id")
	if !ok {
		return
	}
	var target string
	if err := db.QueryRow("SELECT url FROM publisher_webhooks WHERE id=? AND publisher_id=?", whID, pubID).Scan(&target); err != nil {
		writeError(w, "webhook not found", http.StatusNotFound)
		return
	}
	secret, err := publisherSigningSecret(pubID)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	status, derr := postWebhook(target, secret, pingBody())
	resp := map[string]any{"ok": derr == nil, "http_status": status}
	if derr != nil {
		resp["error"] = derr.Error()
	}
	writeJSON(w, resp)
}

// emitImportedEventWebhooks notifies subscribers about events a feed import
// actually created or changed (#1370). The import returns every event it
// touched, unchanged ones included, and per-event outcomes aren't kept, so
// "changed" is recovered from the row itself: changed_at at or after the
// import start means it was written this run, and a created_at inside that
// window distinguishes create from update.
func emitImportedEventWebhooks(events []Event, since int64) {
	if config == nil || !config.Server.Webhooks.Enabled {
		return
	}
	for _, ev := range events {
		var changed, created int64
		if err := db.QueryRow(
			"SELECT COALESCE(changed_at,0), COALESCE(CAST(strftime('%s', created_at) AS INTEGER),0) FROM events WHERE id=?", ev.ID,
		).Scan(&changed, &created); err != nil || changed < since {
			continue
		}
		action := "update"
		if created >= since-1 {
			action = "create"
		}
		emitEventWebhook(ev.ID, action, 0)
	}
}
