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

// TestSuggestManageRejectsMoveIntoPast covers #1413: a manage-link edit may
// not move an event's date into the past (same rule as a new suggestion,
// #1411), but an event that is already over can still be edited as long as
// its date stays on the same day.
func TestSuggestManageRejectsMoveIntoPast(t *testing.T) {
	old := db
	defer func() { db = old }()
	conn, err := sql.Open("sqlite3", "file:managepast?mode=memory&cache=shared")
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

	now := time.Now().In(instanceTimezone)
	future := now.AddDate(0, 1, 0)
	past := now.AddDate(0, -2, 0)
	exp := now.AddDate(1, 0, 0).UTC().Format(time.RFC3339)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO events (id, title, start_time, end_time, is_published, suggestion_token, suggestion_token_expires_at) VALUES (1, 'Future', ?, ?, 0, 'tok-future', ?)`, future.Unix(), future.Unix()+3600, exp)
	mustExec(`INSERT INTO events (id, title, start_time, end_time, is_published, suggestion_token, suggestion_token_expires_at) VALUES (2, 'Over', ?, ?, 0, 'tok-past', ?)`, past.Unix(), past.Unix()+3600, exp)

	patch := func(token string, start time.Time) *httptest.ResponseRecorder {
		body, _ := json.Marshal(SuggestRequest{Title: "Edited", StartTime: start.Format(time.RFC3339)})
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/events/suggest/manage/"+token, bytes.NewReader(body))
		req.SetPathValue("token", token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		patchSuggestManageEvent(rec, req)
		return rec
	}

	t.Run("moving a future event into the past is rejected", func(t *testing.T) {
		rec := patch("tok-future", past)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Code string `json:"error_code"`
		}
		json.Unmarshal(rec.Body.Bytes(), &body)
		if body.Code != "start_time_past" {
			t.Errorf("error code = %q, want start_time_past; body=%s", body.Code, rec.Body.String())
		}
	})

	t.Run("editing an already-past event on its own day is accepted", func(t *testing.T) {
		rec := patch("tok-past", past.Add(30*time.Minute))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("moving a past event to another past day is rejected", func(t *testing.T) {
		rec := patch("tok-past", past.AddDate(0, 0, -3))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("moving to a future date is accepted", func(t *testing.T) {
		rec := patch("tok-future", future.AddDate(0, 0, 7))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
	})
}
