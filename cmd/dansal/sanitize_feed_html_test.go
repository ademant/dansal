package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestSanitizeFeedHTML covers #1447 (compliance G10): script tags (and their
// content), event-handler attributes, and javascript: URLs must be stripped,
// while ordinary text -- including literal "&"/"<" that were never markup --
// survives via decodeHTMLEntities undoing bluemonday's own re-escaping.
func TestSanitizeFeedHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"script tag and content removed", `<script>alert(1)</script>Hello`, "Hello"},
		{"event handler attribute removed", `Hello <img src=x onerror=alert(1)>`, "Hello "},
		{"javascript: href removed, link text kept", `Plain text with <a href="javascript:alert(1)">click</a>`, "Plain text with click"},
		{"benign formatting tags stripped to plain text", `<p>Come dance with <b>us</b>!</p>`, "Come dance with us!"},
		{"no tags at all: untouched", `Plain text, no markup here.`, "Plain text, no markup here."},
		{"literal ampersand and angle bracket round-trip", `A & B < C`, "A & B < C"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decodeHTMLEntities(sanitizeFeedHTML(c.in))
			if got != c.want {
				t.Errorf("sanitizeFeedHTML(%q) -> decodeHTMLEntities = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestInsertEventSanitizesDescription confirms insertEvent — the single
// chokepoint every event-creation path (recurring import, admin "Import
// events" confirm, recheck_source.go, and plain admin create) funnels
// through — sanitizes Description before storage.
func TestInsertEventSanitizesDescription(t *testing.T) {
	setupDedupTestDB(t)

	eventID, _, _, err := insertEvent(db, EventInput{
		Title:       "Imported Ball",
		Description: `<script>alert(document.cookie)</script><p>Come dance with <b>us</b>!</p>`,
		StartTime:   2000000000, EndTime: 2000003600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insertEvent: %v", err)
	}

	event, err := fetchEventByID(db, eventID)
	if err != nil {
		t.Fatalf("fetchEventByID: %v", err)
	}
	if strings.Contains(event.Description, "<script") || strings.Contains(event.Description, "alert(") {
		t.Fatalf("Description still contains a script tag: %q", event.Description)
	}
	if event.Description != "Come dance with us!" {
		t.Errorf("Description = %q, want the stripped-and-cleaned text", event.Description)
	}
}

// TestSuggestHandlerSanitizesDescription covers the issue's explicit
// "suggestion free-text" case: suggestHandler builds its own INSERT
// statement rather than going through insertEvent, so it needs its own
// sanitization call — this confirms it's actually wired in, not just
// present in the shared helper.
func TestSuggestHandlerSanitizesDescription(t *testing.T) {
	old := db
	defer func() { db = old }()

	// suggestHandler fires notifyAdminsSuggestion in a background goroutine
	// -- a shared-cache DSN keeps every pooled connection on the same
	// in-memory database (see TestSuggestHandlerRejectsPastStartTime).
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

	future := time.Now().In(instanceTimezone).Add(2 * time.Hour).Format(time.RFC3339)
	body, _ := json.Marshal(SuggestRequest{
		Title:       "Test suggestion",
		Description: `<script>alert(1)</script>A nice <b>bal</b> in the park`,
		StartTime:   future,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events/suggest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.1:12345"
	rec := httptest.NewRecorder()
	suggestHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	var description string
	if err := db.QueryRow("SELECT description FROM events WHERE title=?", "Test suggestion").Scan(&description); err != nil {
		t.Fatalf("select: %v", err)
	}
	if strings.Contains(description, "<script") || strings.Contains(description, "alert(") {
		t.Fatalf("stored description still contains a script tag: %q", description)
	}
	if description != "A nice bal in the park" {
		t.Errorf("description = %q, want the stripped-and-cleaned text", description)
	}
}
