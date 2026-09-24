package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func withWebhookConfig(t *testing.T, cfg WebhooksConfig) {
	t.Helper()
	old := config
	config = &Config{Server: ServerConfig{Webhooks: cfg}}
	t.Cleanup(func() { config = old })
}

func useWebhookClient(t *testing.T, c *http.Client) {
	t.Helper()
	old := webhookClient
	webhookClient = c
	t.Cleanup(func() { webhookClient = old })
}

// seedWebhookPublisher creates a publisher user (id), a member of org orgID,
// holding a key with signing secret `secret`.
func seedWebhookPublisher(t *testing.T, id, orgID int, secret string) {
	t.Helper()
	db.Exec("INSERT OR IGNORE INTO organizations (id, name) VALUES (?, ?)", orgID, "Org "+strconv.Itoa(orgID))
	db.Exec("INSERT INTO users (id, role, display_name, password_hash, disabled) VALUES (?, 'publisher', ?, '', 0)", id, "pub"+strconv.Itoa(id))
	db.Exec("INSERT INTO organization_members (organization_id, user_id) VALUES (?, ?)", orgID, id)
	db.Exec("INSERT INTO api_keys (user_id, name, api_key, signing_secret_enc) VALUES (?, 'k', ?, ?)",
		id, "hash"+strconv.Itoa(id), signingSecretEncrypt(secret))
}

func addWebhook(t *testing.T, pubID int, url, types string) int {
	t.Helper()
	res, err := db.Exec("INSERT INTO publisher_webhooks (publisher_id, url, event_types, created_at, updated_at) VALUES (?, ?, ?, 1, 1)", pubID, url, types)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

func TestValidateWebhookURL(t *testing.T) {
	withWebhookConfig(t, WebhooksConfig{})
	for _, tc := range []struct {
		url     string
		wantErr bool
	}{
		{"https://93.184.216.34/hook", false},
		{"https://93.184.216.34/?rest_route=/wpd/v1/webhook", false},
		{"http://93.184.216.34/hook", true},
		{"https://127.0.0.1/hook", true},
		{"https://10.0.0.5/hook", true},
		{"https://169.254.169.254/latest/meta-data", true},
		{"https://user:pw@93.184.216.34/hook", true},
		{"https://[::1]/hook", true},
		{"not a url", true},
		{"https://93.184.216.34/" + strings.Repeat("a", 3000), true},
	} {
		err := validateWebhookURL(tc.url)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateWebhookURL(%.60q) err=%v, wantErr=%v", tc.url, err, tc.wantErr)
		}
	}
}

func TestWebhookEventTypes(t *testing.T) {
	if got, err := normalizeWebhookEventTypes(""); err != nil || got != "*" {
		t.Errorf("empty = (%q, %v), want *", got, err)
	}
	if _, err := normalizeWebhookEventTypes("event.publish, event.bogus"); err == nil {
		t.Error("expected error for unknown event type")
	}
	if !webhookWantsType("event.publish,event.cancel", "event.cancel") || webhookWantsType("event.publish", "event.update") {
		t.Error("type matching wrong")
	}
}

func webhookReq(method, path, body string, callerID int, role string, pathVals map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-User-ID", strconv.Itoa(callerID))
	req.Header.Set("X-User-Role", role)
	for k, v := range pathVals {
		req.SetPathValue(k, v)
	}
	return req
}

func TestWebhookEndpoints_Authorization(t *testing.T) {
	setupDedupTestDB(t)
	withWebhookConfig(t, WebhooksConfig{})
	seedWebhookPublisher(t, 1, 1, "s1")
	seedWebhookPublisher(t, 2, 2, "s2")
	addWebhook(t, 1, "https://93.184.216.34/h", "*")

	// Another publisher may not list it.
	w := httptest.NewRecorder()
	listPublisherWebhooks(w, webhookReq("GET", "/x", "", 2, RolePublisher, map[string]string{"id": "1"}))
	if w.Code != http.StatusForbidden {
		t.Errorf("other publisher: status = %d, want 403", w.Code)
	}
	// Owner and admin may.
	for _, c := range []struct {
		id   int
		role string
	}{{1, RolePublisher}, {99, RoleAdmin}} {
		w = httptest.NewRecorder()
		listPublisherWebhooks(w, webhookReq("GET", "/x", "", c.id, c.role, map[string]string{"id": "1"}))
		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", c.role, w.Code)
		}
	}
	// Cannot delete another publisher's webhook via own path.
	w = httptest.NewRecorder()
	deletePublisherWebhook(w, webhookReq("DELETE", "/x", "", 2, RolePublisher, map[string]string{"id": "2", "webhook_id": "1"}))
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-publisher delete: status = %d, want 404", w.Code)
	}
}

func TestCreateWebhook_ValidationAndSecret(t *testing.T) {
	setupDedupTestDB(t)
	withWebhookConfig(t, WebhooksConfig{})
	seedWebhookPublisher(t, 1, 1, "s1")
	db.Exec("INSERT INTO users (id, role, display_name, password_hash, disabled) VALUES (5, 'publisher', 'NoSecret', '', 0)")

	create := func(pub int, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		createPublisherWebhook(w, webhookReq("POST", "/x", body, pub, RolePublisher, map[string]string{"id": strconv.Itoa(pub)}))
		return w
	}
	if w := create(1, `{"url":"https://127.0.0.1/h"}`); w.Code != http.StatusBadRequest {
		t.Errorf("private url: %d, want 400", w.Code)
	}
	if w := create(1, `{"url":"https://93.184.216.34/h","event_types":"nope"}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad type: %d, want 400", w.Code)
	}
	if w := create(5, `{"url":"https://93.184.216.34/h"}`); w.Code != http.StatusBadRequest {
		t.Errorf("no signing secret: %d, want 400", w.Code)
	}
	if w := create(1, `{"url":"https://93.184.216.34/h","event_types":"event.publish"}`); w.Code != http.StatusCreated {
		t.Errorf("valid create: %d, want 201, body=%s", w.Code, w.Body.String())
	}
	for i := 0; i < maxWebhooksPerPublisher; i++ {
		create(1, `{"url":"https://93.184.216.34/h"}`)
	}
	if w := create(1, `{"url":"https://93.184.216.34/h"}`); w.Code != http.StatusConflict {
		t.Errorf("over cap: %d, want 409", w.Code)
	}
}

type capturedHook struct {
	header http.Header
	body   []byte
	path   string
	query  string
}

// hookServer records every delivery; status is what it answers with.
func hookServer(t *testing.T, status *int) (*httptest.Server, chan capturedHook) {
	t.Helper()
	ch := make(chan capturedHook, 16)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- capturedHook{r.Header.Clone(), b, r.URL.Path, r.URL.RawQuery}
		w.WriteHeader(*status)
	}))
	t.Cleanup(ts.Close)
	useWebhookClient(t, ts.Client())
	return ts, ch
}

func TestDispatchEventWebhooks_SignedScopedAndFiltered(t *testing.T) {
	setupDedupTestDB(t)
	withWebhookConfig(t, WebhooksConfig{Enabled: true, RetryBackoffSecs: []int{0}})
	status := 200
	ts, ch := hookServer(t, &status)

	seedWebhookPublisher(t, 1, 1, "secret-one") // member of org 1, wants everything
	seedWebhookPublisher(t, 2, 1, "secret-two") // member of org 1, publish only
	seedWebhookPublisher(t, 3, 2, "secret-three")
	addWebhook(t, 1, ts.URL+"/hook?b=2&a=1", "*")
	addWebhook(t, 2, ts.URL+"/only-publish", "event.publish")
	addWebhook(t, 3, ts.URL+"/other-org", "*") // not a member of org 1

	org := 1
	dispatchEventWebhooks(42, &org, "update", 7)

	got := map[string]capturedHook{}
	for i := 0; i < 1; i++ {
		select {
		case h := <-ch:
			got[h.path] = h
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for delivery")
		}
	}
	select {
	case h := <-ch:
		t.Fatalf("unexpected extra delivery to %s (event-type filter or org scope broken)", h.path)
	case <-time.After(300 * time.Millisecond):
	}

	h, ok := got["/hook"]
	if !ok {
		t.Fatalf("no delivery to /hook, got %v", got)
	}
	var p webhookPayload
	if err := json.Unmarshal(h.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != "event.update" || p.ResourceID != 42 || p.Action != "update" || p.ChangedByUserID != 7 || p.DeliveryID == "" || p.OrganizationID == nil || *p.OrganizationID != 1 {
		t.Errorf("payload = %+v", p)
	}

	// Signature verifies with the #1366 scheme, including the sorted query.
	sum := sha256.Sum256(h.body)
	payload := canonicalSigningPayload("POST", h.path, h.query,
		h.header.Get("X-Wpd-Timestamp"), hex.EncodeToString(sum[:]), h.header.Get("X-Wpd-Nonce"))
	mac := hmac.New(sha256.New, []byte("secret-one"))
	mac.Write([]byte(payload))
	if want := hex.EncodeToString(mac.Sum(nil)); h.header.Get("X-Wpd-Signature") != want {
		t.Errorf("signature mismatch: got %q want %q", h.header.Get("X-Wpd-Signature"), want)
	}
	if len(h.header.Get("X-Wpd-Nonce")) != 32 {
		t.Errorf("nonce = %q, want 32 chars", h.header.Get("X-Wpd-Nonce"))
	}
}

func TestDispatchEventWebhooks_DisabledProducerIsQuiet(t *testing.T) {
	setupDedupTestDB(t)
	withWebhookConfig(t, WebhooksConfig{Enabled: false})
	status := 200
	ts, ch := hookServer(t, &status)
	seedWebhookPublisher(t, 1, 1, "s")
	addWebhook(t, 1, ts.URL+"/h", "*")
	org := 1
	dispatchEventWebhooks(1, &org, "update", 0)
	select {
	case <-ch:
		t.Error("delivery fired although server.webhooks.enabled is false")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestDeliverWebhook_FailureDisablesThenSuccessResets(t *testing.T) {
	setupDedupTestDB(t)
	withWebhookConfig(t, WebhooksConfig{Enabled: true, RetryBackoffSecs: []int{0, 0, 0}, DisableAfterFailures: 4})
	status := 500
	ts, _ := hookServer(t, &status)
	seedWebhookPublisher(t, 1, 1, "s")
	id := addWebhook(t, 1, ts.URL+"/h", "*")

	deliverWebhook(id, pingBody()) // initial + 3 retries, all 500
	var active, failures int
	var lastErr string
	db.QueryRow("SELECT active, consecutive_failures, last_error FROM publisher_webhooks WHERE id=?", id).Scan(&active, &failures, &lastErr)
	if active != 0 || failures != 4 || !strings.Contains(lastErr, "HTTP 500") {
		t.Errorf("after 4 failures: active=%d failures=%d last_error=%q; want disabled, 4, HTTP 500", active, failures, lastErr)
	}

	// A disabled subscription is skipped entirely.
	deliverWebhook(id, pingBody())
	db.QueryRow("SELECT consecutive_failures FROM publisher_webhooks WHERE id=?", id).Scan(&failures)
	if failures != 4 {
		t.Errorf("disabled webhook was still attempted (failures=%d)", failures)
	}

	// Manual reactivation via PATCH resets the counters; a 2xx then records success.
	w := httptest.NewRecorder()
	req := webhookReq("PATCH", "/x", `{"active":true}`, 1, RolePublisher, map[string]string{"id": "1", "webhook_id": strconv.Itoa(id)})
	req.Header.Set("Content-Type", "application/merge-patch+json")
	patchPublisherWebhook(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body=%s", w.Code, w.Body.String())
	}
	status = 200
	deliverWebhook(id, pingBody())
	var lastErr2 string
	var lastAt int64
	db.QueryRow("SELECT active, consecutive_failures, last_error, COALESCE(last_delivery_at,0) FROM publisher_webhooks WHERE id=?", id).Scan(&active, &failures, &lastErr2, &lastAt)
	if active != 1 || failures != 0 || lastErr2 != "" || lastAt == 0 {
		t.Errorf("after success: active=%d failures=%d err=%q last_delivery_at=%d", active, failures, lastErr2, lastAt)
	}
}
