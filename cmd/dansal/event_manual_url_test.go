package main

import (
	"errors"
	"testing"
)

// TestManualCreateURLMatchNeverUpdates covers #1637: a manual create (no feed
// source) whose URL and start time match an existing event must not silently
// overwrite it. It is refused with duplicateURLError unless ConfirmNew is set,
// and a confirmed create inserts a new event. A feed import with the same
// inputs still updates the event it re-sends.
func TestManualCreateURLMatchNeverUpdates(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO fetch_sources (id, url, type) VALUES (1, 'http://example.com/feed.ics', 'ical')")

	const url = "https://example.com/ball"
	const start = int64(2000000000)
	existing, _, _, err := insertEvent(db, EventInput{
		Title: "Original", StartTime: start, EndTime: start + 3600,
		IsPublished: true, URL: url, FetchSourceID: 1,
	})
	if err != nil {
		t.Fatalf("insert existing event: %v", err)
	}

	manual := EventInput{
		Title: "Manual copy", StartTime: start + 600, EndTime: start + 4200,
		IsPublished: true, URL: url,
	}

	t.Run("refused without confirmation and row untouched", func(t *testing.T) {
		_, _, _, err := insertEvent(db, manual)
		var dup *duplicateURLError
		if !errors.As(err, &dup) {
			t.Fatalf("err = %v, want *duplicateURLError", err)
		}
		if dup.ExistingID != existing {
			t.Fatalf("ExistingID = %d, want %d", dup.ExistingID, existing)
		}
		var title string
		db.QueryRow("SELECT title FROM events WHERE id = ?", existing).Scan(&title)
		if title != "Original" {
			t.Fatalf("existing title = %q, want untouched %q", title, "Original")
		}
	})

	t.Run("confirmed create inserts a new event", func(t *testing.T) {
		confirmed := manual
		confirmed.ConfirmNew = true
		id, _, outcome, err := insertEvent(db, confirmed)
		if err != nil {
			t.Fatalf("insert confirmed event: %v", err)
		}
		if id == existing {
			t.Fatalf("confirmed create reused existing id %d", id)
		}
		if outcome != outcomeNew {
			t.Fatalf("outcome = %q, want %q", outcome, outcomeNew)
		}
		var n int
		db.QueryRow("SELECT COUNT(*) FROM events WHERE url = ?", url).Scan(&n)
		if n != 2 {
			t.Fatalf("events with url = %d, want 2", n)
		}
	})

	t.Run("feed import still updates the event it re-sends", func(t *testing.T) {
		feed := manual
		feed.Title = "Feed update"
		feed.FetchSourceID = 1
		id, _, outcome, err := insertEvent(db, feed)
		if err != nil {
			t.Fatalf("feed insert: %v", err)
		}
		if id != existing || outcome != outcomeUpdated {
			t.Fatalf("feed import got (id %d, %q), want (%d, %q)", id, outcome, existing, outcomeUpdated)
		}
	})
}
