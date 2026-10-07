package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// #1468: the suggest API reports how many more suggestions an address has
// left, so the web done page can offer a sized "suggest another event"
// hint/button.

func TestSuggestHandlerReportsRemaining(t *testing.T) {
	old := db
	defer func() { db = old }()

	// suggestHandler fires notifyAdminsSuggestion/the manage-link email in a
	// background goroutine -- shared-cache DSN keeps every pooled connection
	// on the same in-memory DB (see suggest_past_date_test.go's comment,
	// #1466).
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
	initSuggestRateLimiters()

	// suggestRateLimiter (3 requests/10min per IP, initSuggestRateLimiters
	// above) is unrelated to the per-address cap this test exercises -- each
	// call uses its own IP so it never trips.
	ipN := 0
	post := func(title, email string) *httptest.ResponseRecorder {
		t.Helper()
		ipN++
		future := time.Now().In(instanceTimezone).Add(2 * time.Hour).Format(time.RFC3339)
		body, _ := json.Marshal(SuggestRequest{
			Title:     title,
			StartTime: future,
			Email:     email,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/events/suggest", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = fmt.Sprintf("203.0.113.%d:12345", ipN)
		rec := httptest.NewRecorder()
		suggestHandler(rec, req)
		return rec
	}

	t.Run("omitted when the cap doesn't apply (SMTP unconfigured)", func(t *testing.T) {
		config = &Config{Server: ServerConfig{MaxOpenTokensPerAddress: 3}}
		rec := post("No cap event", "nocap@example.test")
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
		}
		var resp SuggestResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Remaining != nil || resp.Limit != nil {
			t.Errorf("remaining/limit should be omitted, got remaining=%v limit=%v", resp.Remaining, resp.Limit)
		}
	})

	t.Run("counts down to 0 at the cap, including the one just created", func(t *testing.T) {
		config = &Config{
			SMTP:   SMTPConfig{Host: "smtp.example.test"},
			Server: ServerConfig{MaxOpenTokensPerAddress: 3},
		}
		want := []int{2, 1, 0}
		for i, w := range want {
			rec := post("Capped event", "organiser@example.test")
			if rec.Code != http.StatusAccepted {
				t.Fatalf("suggestion %d: status = %d, want 202; body=%s", i+1, rec.Code, rec.Body.String())
			}
			var resp SuggestResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Remaining == nil || resp.Limit == nil {
				t.Fatalf("suggestion %d: remaining/limit should be present when SMTP is configured, got %+v", i+1, resp)
			}
			if *resp.Remaining != w {
				t.Errorf("suggestion %d: remaining = %d, want %d", i+1, *resp.Remaining, w)
			}
			if *resp.Limit != 3 {
				t.Errorf("suggestion %d: limit = %d, want 3", i+1, *resp.Limit)
			}
		}
		// The 4th suggestion from the same address is over the cap.
		rec := post("One too many", "organiser@example.test")
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
		}
	})
}
