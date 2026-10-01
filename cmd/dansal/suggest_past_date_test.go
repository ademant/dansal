package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestSuggestHandlerRejectsPastStartTime covers #1411: a suggested event is,
// by definition, something that hasn't happened yet. Client-side validation
// in the wizard can't be trusted alone (the whole point of #1411 was a
// client-side mistake slipping through), so this is the real enforcement.
func TestSuggestHandlerRejectsPastStartTime(t *testing.T) {
	old := db
	defer func() { db = old }()

	// suggestHandler fires notifyAdminsSuggestion in a background goroutine
	// (see TestSuggestVerifyHandlerIdempotent's comment above) -- a plain
	// ":memory:" DSN hands a second pooled connection a fresh, empty
	// database, so the shared-cache DSN is needed to keep every connection
	// on the same one.
	conn, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn
	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()
	if instanceTimezone == nil {
		instanceTimezone, _ = time.LoadLocation("Europe/Berlin")
	}
	config = &Config{}
	initSuggestRateLimiters()

	post := func(startTime, endTime string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(SuggestRequest{
			Title:     "Test suggestion",
			StartTime: startTime,
			EndTime:   endTime,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/events/suggest", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.1:12345"
		rec := httptest.NewRecorder()
		suggestHandler(rec, req)
		return rec
	}

	t.Run("start_time months in the past is rejected", func(t *testing.T) {
		past := time.Now().In(instanceTimezone).AddDate(0, -6, 0).Format(time.RFC3339)
		rec := post(past, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("start_time later today is accepted", func(t *testing.T) {
		future := time.Now().In(instanceTimezone).Add(2 * time.Hour).Format(time.RFC3339)
		rec := post(future, "")
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("start_time earlier than now but still today is accepted", func(t *testing.T) {
		now := time.Now().In(instanceTimezone)
		startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 1, 0, 0, instanceTimezone)
		rec := post(startOfToday.Format(time.RFC3339), "")
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202 (today, even if earlier than the current moment, isn't \"the past\"); body=%s", rec.Code, rec.Body.String())
		}
	})
}
